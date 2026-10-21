package machine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/secrets"
)

// A platform credential stored under the host scope creates the scope's
// projects row. That row names no project and holds no beads workspace,
// so reconcile must never resolve beads for it: a resolver that refuses
// the scope, as beadsx.EnsureWorkspace does, would stop every later start.
func TestReconcileSkipsTheHostScope(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	keyPath := filepath.Join(t.TempDir(), "keys", "kek")
	if err := secrets.EnsureKeyFile(keyPath); err != nil {
		t.Fatal(err)
	}
	keys, err := secrets.NewFileKeyProvider(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	sec := secrets.New(w.st, keys)
	if err := sec.Set(context.Background(), config.HostScope, "GITHUB_APP_KEY", strings.NewReader("key")); err != nil {
		t.Fatal(err)
	}
	var asked []string
	m, err := New(Config{Store: w.st, Redactor: w.red, Registry: cli.Surface(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Beads: func(project string) (beadsx.Client, error) {
			asked = append(asked, project)
			if project == config.HostScope {
				return nil, errors.New("project id \"_host\" is not a beads workspace")
			}
			return w.bc, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile with a host-scope secret: %v", err)
	}
	for _, p := range asked {
		if p == config.HostScope {
			t.Fatalf("reconcile resolved beads for the host scope: %v", asked)
		}
	}
	if got := w.show(id).Status; got != Todo {
		t.Fatalf("status = %s", got)
	}
}
