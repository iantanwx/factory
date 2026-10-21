package release

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/server"
	"github.com/iantanwx/loop/internal/store"
)

type restoreWorld struct {
	*instanceWorld
	snapshot string
}

func (w *instanceWorld) openStores(t *testing.T) (*store.Store, *secrets.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, w.layout.DB())
	if err != nil {
		t.Fatal(err)
	}
	keys, err := secrets.NewFileKeyProvider(w.layout.KeyFile())
	if err != nil {
		t.Fatal(err)
	}
	return st, secrets.New(st, keys)
}

func execSQL(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *store.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

func setSecret(t *testing.T, sec *secrets.Store, project, name, value string) {
	t.Helper()
	if err := sec.Set(context.Background(), project, name, strings.NewReader(value)); err != nil {
		t.Fatal(err)
	}
}

// newRestoreWorld writes state, takes a snapshot, then changes the state:
// it rotates a secret, adds a project and its secret, raises an epoch,
// leases a new item and revokes a run token.
func newRestoreWorld(t *testing.T) *restoreWorld {
	t.Helper()
	ctx := context.Background()
	w := &restoreWorld{instanceWorld: newInstance(t)}
	st, sec := w.openStores(t)
	setSecret(t, sec, config.HostScope, "GH_KEY", "old-value")
	execSQL(t, st, `INSERT INTO projects (id, repo, created_at, updated_at) VALUES ('alpha', 'o/alpha', 1, 1)`)
	execSQL(t, st, `INSERT INTO leases (project, item, epoch, owner_run_id) VALUES ('alpha', 'T-1', 3, 'run-old')`)
	tokens := &rpc.Tokens{Store: st}
	if _, err := tokens.Issue(ctx, rpc.IssueRequest{RunID: "run-old", Kind: cli.PrincipalRun, Item: "T-1", Checkout: "/tmp/co-old"}); err != nil {
		t.Fatal(err)
	}
	w.snapshot = filepath.Join(w.layout.Snapshots(), "loop-20261021T000000.000Z-v1.0.0.db")
	if err := st.Snapshot(ctx, w.snapshot); err != nil {
		t.Fatal(err)
	}
	setSecret(t, sec, config.HostScope, "GH_KEY", "new-value")
	execSQL(t, st, `INSERT INTO projects (id, repo, created_at, updated_at) VALUES ('beta', 'o/beta', 2, 2)`)
	setSecret(t, sec, "beta", "API", "beta-secret")
	execSQL(t, st, `UPDATE leases SET epoch = 5, owner_run_id = 'run-new' WHERE item = 'T-1'`)
	execSQL(t, st, `INSERT INTO leases (project, item, epoch) VALUES ('alpha', 'T-2', 2)`)
	execSQL(t, st, `INSERT INTO projects (id, repo, created_at, updated_at) VALUES ('post', 'o/post', 3, 3)`)
	if _, err := tokens.Revoke(ctx, "run-old"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *instanceWorld) restore() *Restore {
	return &Restore{Host: Host{Layout: w.layout, Service: w.svc, Server: w.server}}
}

func epochOf(t *testing.T, st *store.Store, item string) int {
	t.Helper()
	var n int
	if err := st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT epoch FROM leases WHERE item = ?`, item).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRestoreKeepsTheCurrentSecretsAndRaisesEpochs(t *testing.T) {
	w := newRestoreWorld(t)
	ctx := context.Background()
	rep, err := w.restore().Run(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.svc.list(); !slices.Equal(got, []string{"drain", "stop", "start"}) {
		t.Fatalf("events = %q", got)
	}
	if rep.Snapshot != w.snapshot || rep.SecretsSource != "current" || !rep.Started {
		t.Fatalf("report = %+v", rep)
	}

	if ids := projectIDs(t, w.layout.DB()); !slices.Equal(ids, []string{"_host", "alpha", "beta"}) {
		t.Fatalf("projects after restore = %q; the snapshot lacks post, and beta comes with its secret", ids)
	}
	st, sec := w.openStores(t)
	defer func() { _ = st.Close() }()
	// The rotated secret keeps its new value and the current key decrypts it.
	if v, err := sec.Get(ctx, config.HostScope, "GH_KEY"); err != nil || string(v) != "new-value" {
		t.Fatalf("GH_KEY = %q %v", v, err)
	}
	if v, err := sec.Get(ctx, "beta", "API"); err != nil || string(v) != "beta-secret" {
		t.Fatalf("beta API = %q %v", v, err)
	}
	// No epoch is reused: each lease is above every epoch the replaced
	// database granted.
	if e := epochOf(t, st, "T-1"); e <= 5 {
		t.Fatalf("T-1 epoch = %d, want above 5", e)
	}
	if e := epochOf(t, st, "T-2"); e <= 2 {
		t.Fatalf("T-2 epoch = %d, want above 2", e)
	}
	// A token the replaced database revoked stays revoked.
	var revoked sql.NullInt64
	if err := st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT revoked_at FROM run_tokens WHERE run_id = 'run-old'`).Scan(&revoked)
	}); err != nil || !revoked.Valid {
		t.Fatalf("run-old token revoked = %v %v", revoked, err)
	}
	// The replaced database is kept, whole.
	kept := w.layout.DB() + beforeRestore
	if rep.Kept != kept {
		t.Fatalf("kept = %q", rep.Kept)
	}
	old, err := store.Open(ctx, kept)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = old.Close() }()
	if e := epochOf(t, old, "T-1"); e != 5 {
		t.Fatalf("the kept database has T-1 epoch %d", e)
	}
	for _, f := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(w.layout.DB() + ".restore" + f); err == nil {
			t.Errorf("a temporary %s file is left", f)
		}
	}
}

func TestRestoreSkipsTheDrainWhenTheServerCannotStart(t *testing.T) {
	w := newRestoreWorld(t)
	w.server.up = false
	start := time.Now()
	rep, err := w.restore().Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("restore waited %v", time.Since(start))
	}
	if got := w.svc.list(); !slices.Equal(got, []string{"stop", "start"}) {
		t.Fatalf("events = %q", got)
	}
	if rep.Drain.Skipped == "" {
		t.Fatalf("drain = %+v", rep.Drain)
	}
}

func TestRestoreOverAnUnreadableDatabaseKeepsTheSnapshotSecrets(t *testing.T) {
	w := newRestoreWorld(t)
	db := w.layout.DB()
	for _, f := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(db + f)
	}
	if err := os.WriteFile(db, bytes.Repeat([]byte("not a database "), 512), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := w.restore().Run(context.Background(), filepath.Base(w.snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if rep.SecretsSource != "snapshot" || !strings.Contains(rep.SecretsNote, "rotated") {
		t.Fatalf("report = %+v", rep)
	}
	st, sec := w.openStores(t)
	defer func() { _ = st.Close() }()
	if v, err := sec.Get(context.Background(), config.HostScope, "GH_KEY"); err != nil || string(v) != "old-value" {
		t.Fatalf("GH_KEY = %q %v", v, err)
	}
	if data, _ := os.ReadFile(db + beforeRestore); !bytes.HasPrefix(data, []byte("not a database")) {
		t.Fatal("the unreadable database was not kept")
	}
}

func TestRestoreRefusals(t *testing.T) {
	t.Run("no credential file", func(t *testing.T) {
		w := newRestoreWorld(t)
		if err := os.Remove(cli.AdminCredentialPath(w.dir)); err != nil {
			t.Fatal(err)
		}
		before := tree(t, w.dir)
		_, err := w.restore().Run(context.Background(), "")
		wantRefusal(t, err, cli.CodeCapabilityDenied, "admin_credential")
		if tree(t, w.dir) != before || len(w.svc.list()) != 0 {
			t.Fatal("a refused restore acted")
		}
	})
	t.Run("no snapshot", func(t *testing.T) {
		w := newInstance(t)
		_, err := w.restore().Run(context.Background(), "")
		wantRefusal(t, err, cli.CodeNotFound, "no_snapshot")
		_, err = w.restore().Run(context.Background(), "missing.db")
		wantRefusal(t, err, cli.CodeNotFound, "no_snapshot")
		if len(w.svc.list()) != 0 {
			t.Fatal("a refused restore acted")
		}
	})
	t.Run("unreadable snapshot", func(t *testing.T) {
		w := newInstance(t)
		bad := filepath.Join(w.layout.Snapshots(), "bad.db")
		if err := os.WriteFile(bad, []byte("garbage garbage garbage"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := w.restore().Run(context.Background(), "")
		wantRefusal(t, err, cli.CodeNotFound, "snapshot_unreadable")
	})
}

// TestRestoreAgainstARealServer runs the server in this process: the
// restore stops it, swaps the database while it holds the instance lock,
// and starts it again with no manual step.
func TestRestoreAgainstARealServer(t *testing.T) {
	w := newRestoreWorld(t)
	var cancel context.CancelFunc
	var done chan error
	startServer := func() {
		ctx, c := context.WithCancel(context.Background())
		ready := make(chan struct{})
		done, cancel = make(chan error, 1), c
		go func() {
			done <- server.Run(ctx, server.Options{Instance: w.dir, Ready: ready, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		}()
		select {
		case <-ready:
		case err := <-done:
			t.Fatalf("server: %v", err)
		case <-time.After(20 * time.Second):
			t.Fatal("the server did not start")
		}
	}
	stopServer := func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("server stop: %v", err)
		}
	}
	w.svc.onStart, w.svc.onStop = startServer, stopServer
	startServer()
	defer func() { stopServer() }()

	rep, err := w.restore().Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Started {
		t.Fatalf("report = %+v", rep)
	}
	c := &cli.Client{Instance: w.dir, Budget: 10 * time.Second}
	r := cli.Surface()
	v, _ := r.Verb("status")
	if res := c.Remote(context.Background(), cli.Invocation{Verb: v, Flags: map[string]string{}, RequestID: "restore-test"}); res.Refusal != nil {
		t.Fatalf("status after restore: %+v", res.Refusal)
	}
}

func reportFault(t *testing.T, st *store.Store, text string) string {
	t.Helper()
	f, err := (&fault.Service{Store: st, Release: "v1.0.0"}).Report(context.Background(), rpc.Principal{Kind: cli.PrincipalCoordinator}, text)
	if err != nil {
		t.Fatal(err)
	}
	return f.ID
}

// TestRestoreNeverReusesAFaultID reports F-1, takes a snapshot, reports
// F-2 and restores the snapshot. F-2 still shows, and the next report is
// F-3, so no fault id is given out twice.
func TestRestoreNeverReusesAFaultID(t *testing.T) {
	w := &restoreWorld{instanceWorld: newInstance(t)}
	ctx := context.Background()
	st, _ := w.openStores(t)
	if id := reportFault(t, st, "first"); id != "F-1" {
		t.Fatalf("first fault = %s", id)
	}
	w.snapshot = filepath.Join(w.layout.Snapshots(), "loop-20261021T000000.000Z-v1.0.0.db")
	if err := st.Snapshot(ctx, w.snapshot); err != nil {
		t.Fatal(err)
	}
	if id := reportFault(t, st, "second"); id != "F-2" {
		t.Fatalf("second fault = %s", id)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := w.restore().Run(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st, _ = w.openStores(t)
	defer func() { _ = st.Close() }()
	got, err := (&fault.Service{Store: st}).Get(ctx, "F-2")
	if err != nil || got.Text != "second" {
		t.Fatalf("F-2 after restore = %+v %v", got, err)
	}
	if id := reportFault(t, st, "third"); id != "F-3" {
		t.Fatalf("the report after restore is %s, want F-3", id)
	}
}

// TestRestoreCarriesTheFaultSequence proves the restore carries the
// AUTOINCREMENT counter and not only the rows: an id above every row
// that remains is still never given out again.
func TestRestoreCarriesTheFaultSequence(t *testing.T) {
	w := &restoreWorld{instanceWorld: newInstance(t)}
	ctx := context.Background()
	st, _ := w.openStores(t)
	reportFault(t, st, "first")
	w.snapshot = filepath.Join(w.layout.Snapshots(), "loop-20261021T000000.000Z-v1.0.0.db")
	if err := st.Snapshot(ctx, w.snapshot); err != nil {
		t.Fatal(err)
	}
	reportFault(t, st, "second")
	reportFault(t, st, "third")
	execSQL(t, st, `DELETE FROM faults WHERE n = 3`)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := w.restore().Run(ctx, ""); err != nil {
		t.Fatal(err)
	}
	st, _ = w.openStores(t)
	defer func() { _ = st.Close() }()
	if id := reportFault(t, st, "fourth"); id != "F-4" {
		t.Fatalf("the report after restore is %s, want F-4", id)
	}
}
