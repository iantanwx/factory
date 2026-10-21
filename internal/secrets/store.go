// Package secrets is the server's encrypted secret store (R34, KTD17). Each
// value is sealed under its own AES-256-GCM data key, and a key-encryption
// key from a KeyProvider wraps that data key. The package also imports values
// from 1Password, writes a run's .env file, and redacts every live value from
// outbound text (KTD27).
package secrets

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// MaxValueSize bounds one stored value. A PEM private key is far below it.
const MaxValueSize = 64 << 10

// ErrSecretNotFound means no value is stored under the name.
var ErrSecretNotFound = errors.New("secrets: no value stored")

// Store keeps envelope-encrypted values in the store's secrets table. A
// project's values live under its id. Platform credentials live under
// config.HostScope.
type Store struct {
	db   *store.Store
	keys KeyProvider
}

// New returns the secret store over db with keys.
func New(db *store.Store, keys KeyProvider) *Store { return &Store{db: db, keys: keys} }

func checkName(project, name string) error {
	if project == "" {
		return errors.New("secrets: project is empty")
	}
	if !config.ValidSecretName(name) {
		return fmt.Errorf("secrets: %q is not an environment variable name", name)
	}
	return nil
}

// Set reads one value from r and stores it under project and name. It trims
// one trailing newline, because a value piped from stdin usually ends in
// one. It refuses an empty value and a value larger than MaxValueSize. No
// error holds the value.
func (s *Store) Set(ctx context.Context, project, name string, r io.Reader) error {
	if err := checkName(project, name); err != nil {
		return err
	}
	value, err := io.ReadAll(io.LimitReader(r, MaxValueSize+3))
	if err != nil {
		return fmt.Errorf("secrets: read value for %s/%s: %w", project, name, err)
	}
	defer clear(value)
	value = bytes.TrimSuffix(value, []byte("\n"))
	value = bytes.TrimSuffix(value, []byte("\r"))
	return s.put(ctx, project, map[string][]byte{name: value}, nil)
}

// put seals and stores every value in one transaction, and then runs after
// inside the same transaction when after is not nil.
func (s *Store) put(ctx context.Context, project string, values map[string][]byte, after func(*store.Tx) error) error {
	k, err := s.keys.Current()
	if err != nil {
		return fmt.Errorf("secrets: current key: %w", err)
	}
	rows := make(map[string]sealed, len(values))
	for name, v := range values {
		if err := checkName(project, name); err != nil {
			return err
		}
		if len(v) == 0 {
			return fmt.Errorf("secrets: refuse an empty value for %s/%s", project, name)
		}
		if len(v) > MaxValueSize {
			return fmt.Errorf("secrets: the value for %s/%s is larger than %d bytes", project, name, MaxValueSize)
		}
		if rows[name], err = seal(k, project, name, v); err != nil {
			return err
		}
	}
	return s.db.Write(ctx, func(tx *store.Tx) error {
		if err := ensureScope(tx, project); err != nil {
			return err
		}
		now := tx.Now().UnixMilli()
		for name, r := range rows {
			_, err := tx.Exec(`INSERT INTO secrets (project, name, key_id, key_version, wrapped_key, nonce, ciphertext, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (project, name) DO UPDATE SET key_id = excluded.key_id, key_version = excluded.key_version,
					wrapped_key = excluded.wrapped_key, nonce = excluded.nonce, ciphertext = excluded.ciphertext,
					updated_at = excluded.updated_at`,
				project, name, r.KeyID, r.KeyVersion, r.WrappedKey, r.Nonce, r.Ciphertext, now, now)
			if err != nil {
				return fmt.Errorf("secrets: store %s/%s: %w", project, name, err)
			}
		}
		if after != nil {
			return after(tx)
		}
		return nil
	})
}

// ensureScope checks that the project row exists. The host scope's row is
// created on first use, because no project add creates it.
func ensureScope(tx *store.Tx, project string) error {
	if project == config.HostScope {
		now := tx.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES (?, '', ?, ?) ON CONFLICT (id) DO NOTHING`,
			project, now, now)
		return err
	}
	var one int
	err := tx.QueryRow(`SELECT 1 FROM projects WHERE id = ?`, project).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("secrets: project %s is not registered: %w", project, config.ErrProjectNotFound)
	}
	return err
}

// Get decrypts the value under project and name. A caller must never print
// it. It returns ErrSecretNotFound when no value is stored.
func (s *Store) Get(ctx context.Context, project, name string) ([]byte, error) {
	var r sealed
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT key_id, key_version, wrapped_key, nonce, ciphertext FROM secrets WHERE project = ? AND name = ?`,
			project, name).Scan(&r.KeyID, &r.KeyVersion, &r.WrappedKey, &r.Nonce, &r.Ciphertext)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s/%s", ErrSecretNotFound, project, name)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: read %s/%s: %w", project, name, err)
	}
	return open(s.keys, project, name, r)
}

// Check reports whether a value is stored under project and name. It never
// decrypts, so it never touches the value.
func (s *Store) Check(ctx context.Context, project, name string) (bool, error) {
	var n int
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM secrets WHERE project = ? AND name = ?`, project, name).Scan(&n)
	})
	if err != nil {
		return false, fmt.Errorf("secrets: check %s/%s: %w", project, name, err)
	}
	return n > 0, nil
}

// Delete removes the value under project and name. It is not an error when
// none is stored.
func (s *Store) Delete(ctx context.Context, project, name string) error {
	return s.db.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM secrets WHERE project = ? AND name = ?`, project, name)
		return err
	})
}

// Names lists the names stored for project, sorted.
func (s *Store) Names(ctx context.Context, project string) ([]string, error) {
	var names []string
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT name FROM secrets WHERE project = ? ORDER BY name`, project)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				return err
			}
			names = append(names, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("secrets: list %s: %w", project, err)
	}
	return names, nil
}

// PurgeProject deletes every value of p. It fits config.RemoveHook, so a
// project id used again later starts with no secrets.
func (s *Store) PurgeProject(ctx context.Context, p config.Project) error {
	if p.ID == config.HostScope || p.ID == "" {
		return fmt.Errorf("secrets: refuse to purge scope %q", p.ID)
	}
	return s.db.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM secrets WHERE project = ?`, p.ID)
		return err
	})
}

// HostCredentials decrypts every platform credential that cfg names, keyed
// by CredentialRef.Purpose. A missing credential returns a
// MissingSecretsError naming each store name.
func (s *Store) HostCredentials(ctx context.Context, cfg *config.HostConfig) (map[string][]byte, error) {
	out := map[string][]byte{}
	var missing []string
	for _, ref := range cfg.CredentialRefs() {
		v, err := s.Get(ctx, config.HostScope, ref.Name)
		switch {
		case errors.Is(err, ErrSecretNotFound):
			missing = append(missing, ref.Name)
		case err != nil:
			return nil, err
		default:
			out[ref.Purpose] = v
		}
	}
	if len(missing) > 0 {
		return nil, &MissingSecretsError{Project: config.HostScope, Names: missing}
	}
	return out, nil
}

// NamedValue is one value the redactor matches, with the name a redaction
// shows.
type NamedValue struct {
	Name  string
	Value string
}

// RedactionValues decrypts every stored value in every scope, named
// "<project>/<NAME>". The redactor matches all of them, so one project's
// value cannot leak through another project's channel.
func (s *Store) RedactionValues(ctx context.Context) ([]NamedValue, error) {
	type row struct {
		project, name string
		s             sealed
	}
	var rows []row
	err := s.db.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT project, name, key_id, key_version, wrapped_key, nonce, ciphertext FROM secrets`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.project, &r.name, &r.s.KeyID, &r.s.KeyVersion, &r.s.WrappedKey, &r.s.Nonce, &r.s.Ciphertext); err != nil {
				return err
			}
			rows = append(rows, r)
		}
		return rs.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("secrets: read values for the redactor: %w", err)
	}
	out := make([]NamedValue, 0, len(rows))
	for _, r := range rows {
		v, err := open(s.keys, r.project, r.name, r.s)
		if err != nil {
			return nil, err
		}
		out = append(out, NamedValue{Name: r.project + "/" + r.name, Value: string(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
