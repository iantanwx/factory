package machine

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

const prj = "alpha"

// world is one store, one beads client and a machine over both. A
// restart makes a new machine over the same store and beads, as a server
// process that starts again does.
type world struct {
	t   *testing.T
	st  *store.Store
	bc  beadsx.Client
	bx  *fake.Client // nil on real beads
	red *secrets.Redactor
	m   *Machine
}

func newWorld(t *testing.T) *world {
	t.Helper()
	bx := fake.New("tk")
	if err := bx.ConfigureStatuses(context.Background(), Statuses()); err != nil {
		t.Fatal(err)
	}
	w := newWorldOn(t, bx)
	w.bx = bx
	return w
}

func newWorldOn(t *testing.T, bc beadsx.Client) *world {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := &world{t: t, st: st, bc: bc, red: secrets.NewRedactor()}
	w.m = w.machine()
	return w
}

func (w *world) machine() *Machine {
	w.t.Helper()
	m, err := New(Config{Store: w.st, Beads: func(string) (beadsx.Client, error) { return w.bc, nil }, Redactor: w.red,
		Registry: cli.Surface(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

// restart replaces the machine, as a new server process, and reconciles.
func (w *world) restart() {
	w.t.Helper()
	w.m = w.machine()
	if err := w.m.Reconcile(context.Background()); err != nil {
		w.t.Fatalf("reconcile: %v", err)
	}
}

func (w *world) create(kind Kind, verb string, params map[string]string) string {
	w.t.Helper()
	id, res := w.m.Create(context.Background(), CreateRequest{Project: prj, Kind: kind, Title: "item", Verb: verb,
		Params: params, Actor: Server("test")})
	mustOK(w.t, res)
	return id
}

func (w *world) apply(id, verb string, params map[string]string) cli.Result {
	w.t.Helper()
	return w.m.Apply(context.Background(), Request{Project: prj, Item: id, Verb: verb, Params: params, Actor: Server("test")})
}

func (w *world) must(id, verb string, params map[string]string) {
	w.t.Helper()
	mustOK(w.t, w.apply(id, verb, params))
}

func (w *world) show(id string) Item {
	w.t.Helper()
	it, r := w.m.Show(context.Background(), prj, id)
	if r != nil {
		w.t.Fatalf("show %s: %+v", id, r)
	}
	return it
}

// force puts a bead into a status and facts as a writer outside the
// server would, so a test can start from any state.
func (w *world) force(id, status string, facts map[string]string) {
	w.t.Helper()
	is, err := w.bc.Get(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	set := map[string]string{"loop.forced": "1"}
	for k, v := range facts {
		set[factPrefix+k] = v
	}
	if _, err := w.bc.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: is.Status, Status: status, SetMetadata: set}); err != nil {
		w.t.Fatalf("force %s to %s: %v", id, status, err)
	}
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query, args...).Scan(&n) }); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) history(id string) []Transition {
	w.t.Helper()
	h, err := w.m.History(context.Background(), prj, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return h
}

func (w *world) alerts(kind string) int {
	return w.count(`SELECT count(*) FROM alerts WHERE kind = ? AND state = 'open'`, kind)
}

func mustOK(t *testing.T, res cli.Result) {
	t.Helper()
	if res.Refusal != nil || res.Exit != cli.ExitDone {
		t.Fatalf("want exit 0, got %d %+v", res.Exit, res.Refusal)
	}
}

func wantCode(t *testing.T, res cli.Result, code string) *cli.Refusal {
	t.Helper()
	if res.Refusal == nil || res.Refusal.Code != code {
		t.Fatalf("want %s, got exit %d %+v", code, res.Exit, res.Refusal)
	}
	return res.Refusal
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
