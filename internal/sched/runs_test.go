package sched

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// runStore is a store with the projects alpha and beta, and helpers that
// write the rows the run reads read.
type runStore struct {
	t  *testing.T
	st *store.Store
}

func newRunStore(t *testing.T) *runStore {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, p := range []string{"alpha", "beta"} {
		if err := config.StoreProjectRow(st)(ctx, config.Project{ID: p, Repo: "iantanwx/" + p}); err != nil {
			t.Fatal(err)
		}
	}
	return &runStore{t: t, st: st}
}

func (r *runStore) exec(q string, args ...any) {
	r.t.Helper()
	if err := r.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		r.t.Fatal(err)
	}
}

// run writes one run started at ms. A zero ended leaves it live.
func (r *runStore) run(id, project, stage, item string, started, ended int64) {
	r.t.Helper()
	var end any
	state := RunRunning
	if ended != 0 {
		end, state = ended, "settled"
	}
	r.exec(`INSERT INTO runs (run_id, project, stage, item, harness, model, state, outcome_class, started_at, ended_at)
		VALUES (?, ?, ?, ?, 'claude', 'opus', ?, CASE WHEN ? IS NULL THEN NULL ELSE 'failed' END, ?, ?)`,
		id, project, stage, item, state, end, started, end)
}

func (r *runStore) alert(kind, key, subject, state string, at int64) {
	r.t.Helper()
	r.exec(`INSERT INTO alerts (project, kind, subject, dedupe_key, state, first_at, last_at) VALUES ('alpha', ?, ?, ?, ?, ?, ?)`,
		kind, subject, key, state, at, at)
}

func (r *runStore) transition(item, run, verb, from, to string, at int64) {
	r.t.Helper()
	r.exec(`INSERT INTO intents (project, item, seq, kind, verb, epoch, state, created_at)
		VALUES ('alpha', ?, (SELECT coalesce(max(seq), 0) + 1 FROM intents WHERE project = 'alpha' AND item = ?), 'ticket', ?, 0, 'applied', ?)`,
		item, item, verb, at)
	r.exec(`INSERT INTO transitions (intent_id, project, item, seq, kind, verb, from_status, to_status, epoch, actor, run_id, says, at)
		SELECT id, project, item, seq, kind, verb, ?, ?, 0, 'run:' || ?, ?, 'moved', ? FROM intents ORDER BY id DESC LIMIT 1`,
		from, to, run, run, at)
}

func (r *runStore) audit(actor, action string, at int64) {
	r.t.Helper()
	r.exec(`INSERT INTO audit (at, actor, project, action, detail) VALUES (?, ?, 'alpha', ?, '{"n":1}')`, at, actor, action)
}

// C2: run show answers a finished run with its row, its checkout, its
// failures row and the alerts that name it, newest first. It answers no
// alert of another run.
func TestRunShowExplainsAFinishedRun(t *testing.T) {
	r := newRunStore(t)
	r.run("r-1", "alpha", "implementer", "tk-1", 1000, 5000)
	r.exec(`INSERT INTO checkouts (id, project, item, path, run_id, state, created_at, updated_at)
		VALUES ('c-1', 'alpha', 'tk-1', '/w/loop-alpha-implementer-tk-1-fix-r-1', 'r-1', 'settled', 1000, 5000)`)
	r.exec(`INSERT INTO failures (run_id, project, item, stage, inputs_hash, class, note, recorded_at)
		VALUES ('r-1', 'alpha', 'tk-1', 'implementer', 'h1', 'failed', 'the build stayed red', 5000)`)
	r.alert("run_blocked", "run_blocked:alpha/tk-1/implementer/h1", "implementer run r-1 on tk-1 finished blocked", "open", 5000)
	r.alert("checkout_kept", "checkout_kept:/w/loop-alpha-implementer-tk-1-fix-r-1", "a checkout stays", "closed", 4000)
	r.alert("run_over_budget", "run_over_budget:r-12", "run r-12 ran long", "open", 6000)

	v, err := ShowRun(context.Background(), r.st, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if v.RunID != "r-1" || v.Project != "alpha" || v.Stage != "implementer" || v.Item != "tk-1" || v.Outcome != "failed" ||
		v.Ended == nil || !v.Ended.Equal(time.UnixMilli(5000)) || !v.Started.Equal(time.UnixMilli(1000)) {
		t.Fatalf("run show row = %+v", v)
	}
	if v.Checkout != "/w/loop-alpha-implementer-tk-1-fix-r-1" {
		t.Fatalf("checkout = %q", v.Checkout)
	}
	if v.Failure == nil || *v.Failure != (FailureView{Stage: "implementer", Class: "failed", Inputs: "h1",
		Note: "the build stayed red", At: time.UnixMilli(5000)}) {
		t.Fatalf("failure = %+v", v.Failure)
	}
	if v.Lease != nil {
		t.Fatalf("a run with no lease answers lease %+v", v.Lease)
	}
	want := []AlertRef{
		{Entry: "alert/1", Kind: "run_blocked", State: "open", Subject: "implementer run r-1 on tk-1 finished blocked"},
		{Entry: "alert/2", Kind: "checkout_kept", State: "closed", Subject: "a checkout stays"},
	}
	if !slices.Equal(v.Alerts, want) {
		t.Fatalf("alerts = %+v, want %+v", v.Alerts, want)
	}
}

// C2: a live run that owns a lease answers the lease's item and epoch.
// An unknown run is ErrNoRun.
func TestRunShowAnswersTheLeaseOfALiveRun(t *testing.T) {
	r := newRunStore(t)
	r.run("r-2", "alpha", "implementer", "tk-2", 1000, 0)
	r.exec(`INSERT INTO leases (project, item, epoch, owner_run_id, granted_at, renewed_at, ttl_ms) VALUES ('alpha', 'tk-2', 7, 'r-2', 1000, 2000, 60000)`)

	v, err := ShowRun(context.Background(), r.st, "r-2")
	if err != nil {
		t.Fatal(err)
	}
	if v.Ended != nil || v.State != RunRunning || v.Failure != nil || v.Alerts != nil {
		t.Fatalf("live run = %+v", v)
	}
	if v.Lease == nil || *v.Lease != (LeaseView{Item: "tk-2", Epoch: 7, Granted: time.UnixMilli(1000), Renewed: time.UnixMilli(2000)}) {
		t.Fatalf("lease = %+v", v.Lease)
	}
	if _, err := ShowRun(context.Background(), r.st, "r-9"); !errors.Is(err, ErrNoRun) {
		t.Fatalf("ShowRun(r-9) = %v, want ErrNoRun", err)
	}
}

// C2: run show answers at most 20 alerts, the newest first.
func TestRunShowAnswersTheTwentyNewestAlerts(t *testing.T) {
	r := newRunStore(t)
	r.run("r-1", "alpha", "implementer", "tk-1", 1000, 5000)
	for i := 1; i <= 21; i++ {
		r.alert("run_blocked", fmt.Sprintf("run_blocked:alpha/tk-1/implementer/h%d", i), fmt.Sprintf("run r-1 blocked %d", i), "closed", int64(i))
	}
	v, err := ShowRun(context.Background(), r.st, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Alerts) != 20 || v.Alerts[0].Entry != "alert/21" || v.Alerts[19].Entry != "alert/2" {
		t.Fatalf("alerts = %d, first %+v", len(v.Alerts), v.Alerts)
	}
}

func runIDs(vs []RunView) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.RunID
	}
	return out
}

// C3: run list answers the newest runs first. Project, stage and live
// each narrow the list, and they combine.
func TestRunListFiltersAndOrders(t *testing.T) {
	r := newRunStore(t)
	r.run("r-a", "alpha", "implementer", "tk-1", 1000, 2000)
	r.run("r-b", "alpha", "code-reviewer", "tk-1", 3000, 0)
	r.run("r-c", "beta", "implementer", "tk-9", 4000, 0)
	r.run("r-d", "alpha", "implementer", "tk-2", 5000, 0)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		f    RunFilter
		want []string
	}{
		{"every run", RunFilter{Limit: 50}, []string{"r-d", "r-c", "r-b", "r-a"}},
		{"a project", RunFilter{Project: "alpha", Limit: 50}, []string{"r-d", "r-b", "r-a"}},
		{"a stage", RunFilter{Stage: "implementer", Limit: 50}, []string{"r-d", "r-c", "r-a"}},
		{"live runs", RunFilter{Live: true, Limit: 50}, []string{"r-d", "r-c", "r-b"}},
		{"all three", RunFilter{Project: "alpha", Stage: "implementer", Live: true, Limit: 50}, []string{"r-d"}},
		{"a limit", RunFilter{Limit: 1}, []string{"r-d"}},
		{"no match", RunFilter{Project: "gamma", Limit: 50}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ListRuns(ctx, r.st, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if ids := runIDs(got); !slices.Equal(ids, tc.want) {
				t.Fatalf("ListRuns(%+v) = %q, want %q", tc.f, ids, tc.want)
			}
		})
	}
}

// C3: the limit bounds the list at 50 and at 500, and a list answers no
// lease, failure or alerts.
func TestRunListStopsAtItsLimit(t *testing.T) {
	r := newRunStore(t)
	for i := 1; i <= 501; i++ {
		r.run(fmt.Sprintf("r-%03d", i), "alpha", "implementer", "tk-1", int64(i), int64(i)+1)
	}
	r.exec(`INSERT INTO failures (run_id, project, item, stage, inputs_hash, class, recorded_at) VALUES ('r-501', 'alpha', 'tk-1', 'implementer', 'h', 'failed', 1)`)
	ctx := context.Background()
	for _, tc := range []struct {
		limit       int
		n           int
		first, last string
	}{{50, 50, "r-501", "r-452"}, {500, 500, "r-501", "r-002"}} {
		got, err := ListRuns(ctx, r.st, RunFilter{Limit: tc.limit})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != tc.n || got[0].RunID != tc.first || got[len(got)-1].RunID != tc.last {
			t.Fatalf("limit %d: %d runs, %s to %s", tc.limit, len(got), got[0].RunID, got[len(got)-1].RunID)
		}
		if got[0].Failure != nil || got[0].Lease != nil || got[0].Alerts != nil {
			t.Fatalf("a listed run = %+v", got[0])
		}
	}
}

// C4: run events merges the run's transitions and its audit rows, oldest
// first. At one time a transition comes first. Another run's rows are
// absent. A known run with no event answers none, and an unknown run is
// ErrNoRun.
func TestRunEventsMergesTheTwoSourcesInOneOrder(t *testing.T) {
	r := newRunStore(t)
	r.run("r-1", "alpha", "implementer", "tk-1", 1000, 0)
	r.run("r-2", "alpha", "implementer", "tk-2", 1000, 0)
	r.audit("run:r-1", "host_verb_from_checkout", 3000)
	r.transition("tk-1", "r-1", "claim", "todo", "in_progress", 2000)
	r.transition("tk-1", "r-1", "pr_open", "in_progress", "in_review", 3000)
	r.audit("run:r-2", "secret_check", 2500)
	r.transition("tk-2", "r-2", "claim", "todo", "in_progress", 2500)
	ctx := context.Background()

	got, err := RunEvents(ctx, r.st, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range got {
		lines = append(lines, fmt.Sprintf("%d %s %s %s %s>%s %s %s %s", e.At.UnixMilli(), e.Source, e.Item, e.Verb, e.From, e.To, e.Says, e.Action, e.Detail))
	}
	want := []string{
		"2000 transition tk-1 claim todo>in_progress moved  ",
		"3000 transition tk-1 pr_open in_progress>in_review moved  ",
		`3000 audit   >  host_verb_from_checkout {"n":1}`,
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("run events =\n%q\nwant\n%q", lines, want)
	}

	r.run("r-3", "alpha", "implementer", "tk-3", 1000, 0)
	if got, err := RunEvents(ctx, r.st, "r-3"); err != nil || len(got) != 0 {
		t.Fatalf("RunEvents(r-3) = %v, %v; want none", got, err)
	}
	if _, err := RunEvents(ctx, r.st, "r-9"); !errors.Is(err, ErrNoRun) {
		t.Fatalf("RunEvents(r-9) = %v, want ErrNoRun", err)
	}
}
