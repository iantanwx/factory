package secrets

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"sort"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// ErrVaultNotDeclared means a manifest reference names a 1Password vault
// that host config does not declare (KTD28).
var ErrVaultNotDeclared = errors.New("secrets: source reference names an undeclared vault")

// importAuditAction marks the audit row that records each import's
// reference set. The next import compares against it.
const importAuditAction = "secret.import"

// Importer fills a project's secrets from its manifest with the 1Password
// CLI (R34). It runs only at the operator's hand: nothing reads 1Password at
// run time.
type Importer struct {
	Store  *Store
	Vaults []string     // the vaults host config declares
	OpPath string       // the op binary; "op" on PATH when empty
	Logger *slog.Logger // optional; it receives references, never values
	Actor  string       // the audit actor; "operator" when empty
}

// ImportResult reports one import.
type ImportResult struct {
	Added    []string // source references absent from the previous import, sorted
	Imported []string // the names stored, in manifest order
}

// Import checks every reference against the declared vaults before it runs
// op, then reads each value with `op read`, once for each entry. The
// reference goes in argv because it is not secret, and the value comes back
// on stdout only. Every value and the reference set of this import are
// stored in one transaction, so a failed import stores nothing.
func (im *Importer) Import(ctx context.Context, project string, m *config.Manifest) (*ImportResult, error) {
	// An entry with no source is set by hand with loop secret set.
	sourced := &config.Manifest{}
	for _, e := range m.Secrets {
		if e.Source != "" {
			sourced.Secrets = append(sourced.Secrets, e)
		}
	}
	m = sourced
	declared := map[string]bool{}
	for _, v := range im.Vaults {
		declared[v] = true
	}
	var undeclared []string
	for _, e := range m.Secrets {
		if !declared[e.Vault()] {
			undeclared = append(undeclared, e.Source)
		}
	}
	if len(undeclared) > 0 {
		return nil, fmt.Errorf("%w: %s; declare the vault in host config or change the manifest", ErrVaultNotDeclared, strings.Join(undeclared, ", "))
	}

	previous, err := im.previousRefs(ctx, project)
	if err != nil {
		return nil, err
	}
	res := &ImportResult{}
	refs := make([]string, 0, len(m.Secrets))
	for _, e := range m.Secrets {
		refs = append(refs, e.Source)
		if !previous[e.Source] {
			res.Added = append(res.Added, e.Source)
		}
	}
	sort.Strings(res.Added)
	sort.Strings(refs)

	values := make(map[string][]byte, len(m.Secrets))
	defer func() {
		for _, v := range values {
			clear(v)
		}
	}()
	for _, e := range m.Secrets {
		im.log(ctx, "op read", "project", project, "name", e.Name, "source", e.Source)
		v, err := im.read(ctx, e.Source)
		if err != nil {
			return nil, err
		}
		values[e.Name] = v
		res.Imported = append(res.Imported, e.Name)
	}

	detail, err := json.Marshal(map[string][]string{"refs": refs})
	if err != nil {
		return nil, err
	}
	actor := im.Actor
	if actor == "" {
		actor = "operator"
	}
	err = im.Store.put(ctx, project, values, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO audit (at, actor, project, action, detail) VALUES (?, ?, ?, ?, ?)`,
			tx.Now().UnixMilli(), actor, project, importAuditAction, string(detail))
		return err
	})
	if err != nil {
		return nil, err
	}
	im.log(ctx, "secret import stored", "project", project, "count", len(values), "added", len(res.Added))
	return res, nil
}

func (im *Importer) log(ctx context.Context, msg string, args ...any) {
	if im.Logger != nil {
		im.Logger.InfoContext(ctx, msg, args...)
	}
}

// previousRefs returns the reference set of the project's last import.
func (im *Importer) previousRefs(ctx context.Context, project string) (map[string]bool, error) {
	var detail string
	err := im.Store.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT detail FROM audit WHERE project = ? AND action = ? ORDER BY id DESC LIMIT 1`,
			project, importAuditAction).Scan(&detail)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: read the previous import: %w", err)
	}
	var d struct {
		Refs []string `json:"refs"`
	}
	if err := json.Unmarshal([]byte(detail), &d); err != nil {
		return nil, fmt.Errorf("secrets: decode the previous import: %w", err)
	}
	out := make(map[string]bool, len(d.Refs))
	for _, r := range d.Refs {
		out[r] = true
	}
	return out, nil
}

// cappedBuffer keeps at most limit bytes and records that more arrived.
type cappedBuffer struct {
	buf      bytes.Buffer
	limit    int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if len(p) > room {
		c.overflow = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return c.buf.Write(p)
}

// read runs `op read --no-newline <ref>`. A failure names the reference and
// op's first stderr line, with any stdout bytes cut out of that line, so a
// value op printed before it failed stays out of the error.
func (im *Importer) read(ctx context.Context, ref string) ([]byte, error) {
	opPath := im.OpPath
	if opPath == "" {
		opPath = "op"
	}
	stdout := &cappedBuffer{limit: MaxValueSize + 1}
	stderr := &cappedBuffer{limit: 4 << 10}
	cmd := exec.CommandContext(ctx, opPath, "read", "--no-newline", ref)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	runErr := cmd.Run()
	out := stdout.buf.Bytes()
	if runErr != nil {
		msg := stderr.buf.String()
		if len(out) > 0 {
			msg = strings.ReplaceAll(msg, string(out), "[REDACTED]")
		}
		msg = firstLine(msg)
		clear(out)
		return nil, fmt.Errorf("secrets: op read %s: %w: %s", ref, runErr, msg)
	}
	if stdout.overflow || len(out) > MaxValueSize {
		clear(out)
		return nil, fmt.Errorf("secrets: op read %s: the value is larger than %d bytes", ref, MaxValueSize)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("secrets: op read %s: op returned an empty value", ref)
	}
	v := bytes.Clone(out)
	clear(out)
	return v, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
