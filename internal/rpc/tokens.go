package rpc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// Token errors. Callers test them with errors.Is.
var (
	ErrTokenUnknown = errors.New("rpc: unknown run token")
	ErrTokenRevoked = errors.New("rpc: run token revoked; its run settled")
)

// tokenPrefix marks a run token, so a person who finds one in a log knows
// what it is.
const tokenPrefix = "loopt_"

// Principal is the caller of one call (KTD12). Every transition the call
// makes records Actor() as its actor.
type Principal struct {
	Kind     string // cli.PrincipalNone, Run, Room, Coordinator or Host
	RunID    string
	Project  string
	Item     string
	Stage    string
	Checkout string
	Epoch    int64
	Release  string // the release the run started under
	Peer     PeerInfo
	// CwdUnverified is true when the platform could not read the caller's
	// working directory, so the checkout checks did not run.
	CwdUnverified bool
}

// Actor names the principal in audit rows and transitions.
func (p Principal) Actor() string {
	switch p.Kind {
	case cli.PrincipalRun, cli.PrincipalRoom, cli.PrincipalCoordinator:
		return p.Kind + ":" + p.RunID
	case "":
		return cli.PrincipalNone
	}
	return p.Kind
}

type principalKey struct{}

// PrincipalFrom returns the principal of the call in ctx. The server sets
// it before a handler runs.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

func withPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// IssueRequest binds a new token (KTD12).
type IssueRequest struct {
	RunID    string
	Kind     string // cli.PrincipalRun, cli.PrincipalRoom or cli.PrincipalCoordinator
	Project  string
	Item     string
	Stage    string
	Checkout string // absolute path of the run's checkout
	Epoch    int64  // the lease epoch the run holds
	Release  string // the server release that starts the run; cli.Version when empty
}

// Tokens issues, verifies and revokes run tokens. A token is 256 random
// bits. The store keeps only its sha256. The redactor matches every token
// this process issued or loaded (KTD27).
type Tokens struct {
	Store    *store.Store
	Redactor *secrets.Redactor // optional
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func liveName(runID, hash string) string { return "run_token/" + runID + "/" + hash[:8] }

// Issue creates a token for r and returns it. The caller writes it into
// the checkout with cli.WriteTokenFile, or calls IssueToCheckout.
func (t *Tokens) Issue(ctx context.Context, r IssueRequest) (string, error) {
	switch r.Kind {
	case cli.PrincipalRun, cli.PrincipalRoom, cli.PrincipalCoordinator:
	default:
		return "", fmt.Errorf("rpc: a token kind must be run, room or coordinator, not %q", r.Kind)
	}
	if r.RunID == "" || r.Checkout == "" {
		return "", errors.New("rpc: a token needs a run id and a checkout")
	}
	if r.Release == "" {
		r.Release = cli.Version
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := tokenPrefix + hex.EncodeToString(raw[:])
	hash := hashToken(token)
	err := t.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO run_tokens
			(token_hash, run_id, kind, project, item, stage, checkout, epoch, release, issued_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			hash, r.RunID, r.Kind, r.Project, r.Item, r.Stage, r.Checkout, r.Epoch, r.Release, tx.Now().UnixMilli())
		return err
	})
	if err != nil {
		return "", fmt.Errorf("rpc: store the token of %s: %w", r.RunID, err)
	}
	if t.Redactor != nil {
		t.Redactor.SetLive(liveName(r.RunID, hash), token)
	}
	return token, nil
}

// IssueToCheckout issues a token and writes .loop/token into the checkout.
func (t *Tokens) IssueToCheckout(ctx context.Context, r IssueRequest) (cli.TokenFile, error) {
	token, err := t.Issue(ctx, r)
	if err != nil {
		return cli.TokenFile{}, err
	}
	tf := cli.TokenFile{Token: token, RunID: r.RunID, Kind: r.Kind, Project: r.Project, Item: r.Item}
	if err := cli.WriteTokenFile(r.Checkout, tf); err != nil {
		return cli.TokenFile{}, fmt.Errorf("rpc: write the token file: %w", err)
	}
	return tf, nil
}

// Verify returns the principal a token binds. It returns ErrTokenUnknown
// when the token admits nothing. With ErrTokenRevoked it still returns the
// principal, so the server can replay the settled run's stored answers. Release is the run
// record's release, or the token's when the run has no record.
func (t *Tokens) Verify(ctx context.Context, token string) (Principal, error) {
	if len(token) != len(tokenPrefix)+64 {
		return Principal{}, ErrTokenUnknown
	}
	hash := hashToken(token)
	var (
		p       Principal
		stored  string
		revoked sql.NullInt64
	)
	err := t.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT t.token_hash, t.run_id, t.kind, t.project, t.item, t.stage, t.checkout,
				t.epoch, coalesce(nullif(r.release, ''), t.release), t.revoked_at
			FROM run_tokens t LEFT JOIN runs r ON r.run_id = t.run_id
			WHERE t.token_hash = ?`, hash).
			Scan(&stored, &p.RunID, &p.Kind, &p.Project, &p.Item, &p.Stage, &p.Checkout, &p.Epoch, &p.Release, &revoked)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Principal{}, ErrTokenUnknown
	}
	if err != nil {
		return Principal{}, fmt.Errorf("rpc: verify a token: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1 {
		return Principal{}, ErrTokenUnknown
	}
	if revoked.Valid {
		return p, ErrTokenRevoked
	}
	return p, nil
}

// Revoke revokes every token of runID at settlement. In the same
// transaction it sets the expiry of the run's idempotency keys to now plus
// the CLI wait budget (KTD13). The redactor keeps matching the revoked
// tokens until the server restarts, so a late echo of one is still caught.
func (t *Tokens) Revoke(ctx context.Context, runID string) (int64, error) {
	var n int64
	err := t.Store.Write(ctx, func(tx *store.Tx) error {
		res, err := tx.Exec(`UPDATE run_tokens SET revoked_at = ? WHERE run_id = ? AND revoked_at IS NULL`,
			tx.Now().UnixMilli(), runID)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		_, err = tx.ExpireRunKeys(runID, tx.Now().Add(cli.DefaultWaitBudget))
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("rpc: revoke the tokens of %s: %w", runID, err)
	}
	return n, nil
}

// LoadLive reads the token file of every unrevoked token and gives each
// token whose hash matches to the redactor. A restarted server knows the
// tokens only by hash, so this restores KTD27's match on live tokens.
func (t *Tokens) LoadLive(ctx context.Context) error {
	if t.Redactor == nil {
		return nil
	}
	type row struct{ hash, runID, checkout string }
	var rows []row
	err := t.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT token_hash, run_id, checkout FROM run_tokens WHERE revoked_at IS NULL`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.hash, &r.runID, &r.checkout); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return rs.Err()
	})
	if err != nil {
		return fmt.Errorf("rpc: list live tokens: %w", err)
	}
	for _, r := range rows {
		tf, err := cli.ReadTokenFile(r.checkout)
		if err != nil || hashToken(tf.Token) != r.hash {
			continue
		}
		t.Redactor.SetLive(liveName(r.runID, r.hash), tf.Token)
	}
	return nil
}

// LiveCheckouts lists the checkout of every unrevoked token.
func (t *Tokens) LiveCheckouts(ctx context.Context) ([]string, error) {
	var out []string
	err := t.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT DISTINCT checkout FROM run_tokens WHERE revoked_at IS NULL
			UNION SELECT path FROM checkouts`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var p string
			if err := rs.Scan(&p); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rs.Err()
	})
	return out, err
}
