package inbox

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// routeWorld is an inbox over a store with no machine: its alerts,
// faults and questions only.
type routeWorld struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	in  *Inbox
}

func newRouteWorld(t *testing.T) *routeWorld {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &routeWorld{t: t, ctx: ctx, st: st, in: New(Config{Store: st})}
}

func (w *routeWorld) alert(project, kind, dedupe string) {
	w.t.Helper()
	if err := config.StoreAlerter(w.st).Raise(w.ctx, config.Alert{Project: project, Kind: kind, DedupeKey: dedupe, Subject: kind}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *routeWorld) question(about string) {
	w.t.Helper()
	if err := w.st.Write(w.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO questions (entry, body, run_id, created_at) VALUES (?, 'which?', 'coord-1', ?)`, about, tx.Now().UnixMilli())
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// routes lists the inbox and names the destination of each entry, by id.
func (w *routeWorld) routes(t coord.Topology) map[string]string {
	w.t.Helper()
	es, err := w.in.Entries(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range es {
		out[e.ID] = Route(e, t)
	}
	return out
}

// C7 and C21, spec work entry 3: a loop fault, an alert of a failed
// coordinator start and an entry with no project ring the loop project's
// coordinator. Any other entry of a project with a coordinator rings that
// project's. A question routes as the entry it names, and a question with
// no entry, or whose entry no longer lists, routes to the loop project.
func TestC7EachEntryRoutesToOneCoordinator(t *testing.T) {
	w := newRouteWorld(t)
	w.alert("alpha", "coordinator_start_failed", "coordinator_start_failed:alpha")
	w.alert("alpha", "coordinator_checkout_missing", "coordinator_checkout_missing:alpha")
	w.alert("alpha", "config_conflict", "config_conflict:alpha/coordinator")
	w.alert("alpha", "config_conflict", "config_conflict:alpha/implementer")
	w.alert("beta", "run_crashed", "run_crashed:r-b1")
	w.alert("", "usage_limit", "usage_limit:claude")
	f, err := (&fault.Service{Store: w.st}).Report(w.ctx, rpc.Principal{Kind: cli.PrincipalRun, RunID: "r-a1", Project: "alpha",
		Item: "ta-1", Stage: "implementer"}, "the CLI refused a valid call")
	if err != nil {
		t.Fatal(err)
	}
	w.question("alert/4")  // the implementer config_conflict of alpha
	w.question("alert/5")  // beta's crash
	w.question("")         // names no entry
	w.question("alert/99") // names an entry that no longer lists

	topo := coord.Topology{Loop: "loop", Homes: map[string]bool{"alpha": true, "beta": true}}
	want := map[string]string{
		"alert/1":       "loop",
		"alert/2":       "loop",
		"alert/3":       "loop",
		"alert/4":       "alpha",
		"alert/5":       "beta",
		"alert/6":       "loop",
		"fault/" + f.ID: "loop",
		"question/1":    "alpha",
		"question/2":    "beta",
		"question/3":    "loop",
		"question/4":    "loop",
	}
	got := w.routes(topo)
	if len(got) != len(want) {
		t.Fatalf("routes = %v, want %v", got, want)
	}
	for id, dest := range want {
		if got[id] != dest {
			t.Errorf("Route(%s) = %q, want %q", id, got[id], dest)
		}
	}
}

// Spec work entry 3: the fault entry holds the run that reported it, and
// an alert entry holds its dedupe key. Neither shows in the entry's JSON.
func TestEntriesHoldTheFaultRunAndTheAlertDedupeKey(t *testing.T) {
	w := newRouteWorld(t)
	w.alert("alpha", "config_conflict", "config_conflict:alpha/coordinator")
	if _, err := (&fault.Service{Store: w.st}).Report(w.ctx, rpc.Principal{Kind: cli.PrincipalRun, RunID: "r-a1", Project: "alpha",
		Item: "ta-1", Stage: "implementer"}, "the CLI refused a valid call"); err != nil {
		t.Fatal(err)
	}
	es, err := w.in.Entries(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 2 {
		t.Fatalf("entries = %+v", es)
	}
	for _, e := range es {
		switch e.Kind {
		case KindAlert:
			if e.Dedupe != "config_conflict:alpha/coordinator" {
				t.Errorf("alert Dedupe = %q", e.Dedupe)
			}
		case KindFault:
			if e.Run != "r-a1" {
				t.Errorf("fault Run = %q", e.Run)
			}
		}
	}
	raw, err := json.Marshal(es)
	if err != nil {
		t.Fatal(err)
	}
	var shown []map[string]any
	if err := json.Unmarshal(raw, &shown); err != nil {
		t.Fatal(err)
	}
	for _, e := range shown {
		for _, key := range []string{"run", "dedupe", "Run", "Dedupe"} {
			if _, ok := e[key]; ok {
				t.Errorf("the JSON of %v holds %q", e["entry"], key)
			}
		}
	}
}

// Spec work entry 3: Owner names the project of an entry by its id, a
// question by the entry it names, and an entry that does not list by the
// project its id names.
func TestOwnerNamesTheProjectOfAnEntry(t *testing.T) {
	w := newRouteWorld(t)
	w.alert("beta", "run_crashed", "run_crashed:r-b1")
	w.question("alert/1")
	w.question("")
	for _, tc := range []struct {
		id, project string
		ok          bool
	}{
		{"alert/1", "beta", true},
		{"question/1", "beta", true},
		{"question/2", "", true},
		{"hold/beta/tb-1", "beta", true},
		{"alert/9", "", false},
	} {
		got, ok, err := w.in.Owner(w.ctx, tc.id)
		if err != nil || got != tc.project || ok != tc.ok {
			t.Errorf("Owner(%s) = %q %v %v, want %q %v", tc.id, got, ok, err, tc.project, tc.ok)
		}
	}
}
