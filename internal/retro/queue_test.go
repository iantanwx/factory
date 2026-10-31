package retro_test

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/store"
)

const prj = "alpha"

// world is one store and one beads client, with a machine whose Terminal
// hook queues retros. A restart makes a new machine over both and runs
// the start-up reconcile, as a server that starts again does.
type world struct {
	t  *testing.T
	st *store.Store
	bx *fake.Client
	m  *machine.Machine
}

func newWorld(t *testing.T) *world {
	t.Helper()
	bx := fake.New("tk")
	if err := bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := &world{t: t, st: st, bx: bx}
	w.m = w.machine(retro.Enqueue)
	return w
}

func (w *world) machine(terminal func(*store.Tx, machine.TerminalEvent) error) *machine.Machine {
	w.t.Helper()
	m, err := machine.New(machine.Config{Store: w.st, Beads: func(string) (beadsx.Client, error) { return w.bx, nil },
		Terminal: terminal, Registry: cli.Surface(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		w.t.Fatal(err)
	}
	return m
}

func (w *world) restart() {
	w.t.Helper()
	w.m = w.machine(retro.Enqueue)
	if err := w.m.Reconcile(context.Background()); err != nil {
		w.t.Fatalf("reconcile: %v", err)
	}
}

func (w *world) create(kind machine.Kind, verb string, facts map[string]string) string {
	w.t.Helper()
	id, res := w.m.Create(context.Background(), machine.CreateRequest{Project: prj, Kind: kind, Title: "item", Verb: verb,
		Actor: machine.Server("test"), Facts: facts})
	if res.Refusal != nil {
		w.t.Fatalf("create %s: %+v", kind, res.Refusal)
	}
	return id
}

func (w *world) apply(id, verb string, params map[string]string) cli.Result {
	w.t.Helper()
	return w.m.Apply(context.Background(), machine.Request{Project: prj, Item: id, Verb: verb, Params: params, Actor: machine.Server("test")})
}

func (w *world) must(id, verb string, params map[string]string) {
	w.t.Helper()
	if res := w.apply(id, verb, params); res.Refusal != nil {
		w.t.Fatalf("%s %s: %+v", verb, id, res.Refusal)
	}
}

func (w *world) merge(id string) {
	w.t.Helper()
	res := w.m.MergeObserved(context.Background(), machine.MergeEvent{Project: prj, Item: id, Base: "main", Into: "main", SHA: "abc"})
	if res.Refusal != nil {
		w.t.Fatalf("merge %s: %+v", id, res.Refusal)
	}
}

func (w *world) show(id string) machine.Item {
	w.t.Helper()
	it, r := w.m.Show(context.Background(), prj, id)
	if r != nil {
		w.t.Fatalf("show %s: %+v", id, r)
	}
	return it
}

// force puts a bead into a status as a writer outside the server would, so
// a test can start from any state. No move applies, so no hook runs.
func (w *world) force(id, status string) {
	w.t.Helper()
	is, err := w.bx.Get(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: is.Status, Status: status,
		SetMetadata: map[string]string{"loop.forced": "1"}}); err != nil {
		w.t.Fatalf("force %s to %s: %v", id, status, err)
	}
}

type row struct{ Item, Kind, State string }

func (w *world) retros() []row {
	w.t.Helper()
	var out []row
	err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT item, kind, state FROM retros WHERE project = ? ORDER BY item`, prj)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.Item, &r.Kind, &r.State); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

func (w *world) wantRetros(want ...row) {
	w.t.Helper()
	if got := w.retros(); !slices.Equal(got, want) {
		w.t.Fatalf("retros = %v, want %v", got, want)
	}
}

// epicWithChild is an epic in execution with one open child ticket.
func (w *world) epicWithChild() (epic, child string) {
	epic = w.create(machine.KindEpic, "create_architecture", nil)
	w.force(epic, machine.Execution)
	return epic, w.create(machine.KindTicket, "create", map[string]string{machine.FactEpic: epic})
}

// C1, by completeEpic: the last child's merge lands its epic in done, and
// the epic holds one pending retro. A restart and its reconcile, which
// applies complete again, leave that one row.
func TestC1EpicDoneByItsLastChildGetsOneRetro(t *testing.T) {
	w := newWorld(t)
	epic, child := w.epicWithChild()
	w.merge(child)
	if got := w.show(epic).Status; got != machine.Done {
		t.Fatalf("the epic is %s after its last child merged", got)
	}
	w.wantRetros(row{epic, "epic", "pending"})
	w.restart()
	w.restart()
	w.wantRetros(row{epic, "epic", "pending"})
}

// C1, by CompleteEpics and by the start-up reconcile: an epic whose last
// child finished with no complete write closes on the sweep, and holds one
// pending retro whichever caller won the move.
func TestC1EpicDoneBySweepOrReconcileGetsOneRetro(t *testing.T) {
	w := newWorld(t)
	swept, sweptChild := w.epicWithChild()
	started, startedChild := w.epicWithChild()
	w.force(sweptChild, machine.Done)
	if err := w.m.CompleteEpics(context.Background(), prj); err != nil {
		t.Fatal(err)
	}
	w.wantRetros(row{swept, "epic", "pending"})

	w.force(startedChild, machine.Done)
	w.restart()
	if got := w.show(started).Status; got != machine.Done {
		t.Fatalf("the epic is %s after the reconcile", got)
	}
	if err := w.m.CompleteEpics(context.Background(), prj); err != nil {
		t.Fatal(err)
	}
	w.restart()
	want := []row{{swept, "epic", "pending"}, {started, "epic", "pending"}}
	slices.SortFunc(want, func(a, b row) int { return strings.Compare(a.Item, b.Item) })
	w.wantRetros(want...)
}

// A second insert for one item does nothing: the first row stays, in its
// state.
func TestEnqueueTwiceKeepsOneRow(t *testing.T) {
	w := newWorld(t)
	ev := machine.TerminalEvent{Project: prj, Item: "tk-9", Kind: machine.KindEpic, Status: machine.Done}
	for range 2 {
		if err := w.st.Write(context.Background(), func(tx *store.Tx) error { return retro.Enqueue(tx, ev) }); err != nil {
			t.Fatal(err)
		}
	}
	w.wantRetros(row{"tk-9", "epic", "pending"})
}

// C2: the closed bug states are the ones the operator chose on 2026-10-31.
// A canceled bug is closed. A shaped bug is closed once its ticket is done.
// A duplicate is never closed, and no open state is.
func TestC2BugClosedHoldsTheOperatorsSet(t *testing.T) {
	cases := []struct {
		status, ticket string
		want           bool
	}{
		{machine.Canceled, "", true},
		{machine.Shaped, machine.Done, true},
		{machine.Shaped, "", false},
		{machine.Shaped, machine.Todo, false},
		{machine.Shaped, machine.Canceled, false},
		{machine.Duplicate, "", false},
		{machine.Duplicate, machine.Done, false},
		{machine.Reported, "", false},
		{machine.Reproducing, "", false},
		{machine.Asked, "", false},
		{machine.AwaitingBuild, machine.Done, false},
		{machine.Escalated, "", false},
	}
	for _, c := range cases {
		if got := machine.BugClosed(c.status, c.ticket); got != c.want {
			t.Errorf("BugClosed(%q, %q) = %v, want %v", c.status, c.ticket, got, c.want)
		}
	}
}

// C2: a canceled bug gets one retro at once. A repeat of the cancel, with
// the same key, replays and leaves one row.
func TestC2CanceledBugGetsOneRetro(t *testing.T) {
	w := newWorld(t)
	bug := w.create(machine.KindBug, "file", nil)
	w.force(bug, machine.Reproducing)
	req := machine.Request{Project: prj, Item: bug, Verb: "cancel", Params: map[string]string{"reason": "a loop fault"},
		Key: &machine.Key{Key: "cancel-1", Fingerprint: store.Fingerprint{RunID: "r-1", Verb: "cancel"}}, Actor: machine.Server("test")}
	for range 2 {
		if res := w.m.Apply(context.Background(), req); res.Refusal != nil {
			t.Fatalf("cancel: %+v", res.Refusal)
		}
	}
	w.wantRetros(row{bug, "bug", "pending"})
}

// C2: a shaped bug gets one retro when the ticket it became reaches done,
// and none before. The retro names the bug, not the ticket.
func TestC2ShapedBugGetsOneRetroWhenItsTicketIsDone(t *testing.T) {
	w := newWorld(t)
	bug := w.create(machine.KindBug, "file", nil)
	w.force(bug, machine.Reproducing)
	ticket := w.create(machine.KindTicket, "create", map[string]string{machine.FactBug: bug})
	w.must(bug, "shape", map[string]string{"ticket": ticket})
	w.wantRetros()

	w.merge(ticket)
	w.wantRetros(row{bug, "bug", "pending"})
	w.restart()
	w.wantRetros(row{bug, "bug", "pending"})
}

// C3: a duplicate bug, a shaped bug whose ticket was canceled, a ticket in
// done, and an escalated epic whose last child finished get no retro.
func TestC3NoRetroForAnItemThatDidNotFinish(t *testing.T) {
	w := newWorld(t)
	dup := w.create(machine.KindBug, "file", nil)
	w.force(dup, machine.Reproducing)
	w.must(dup, "duplicate", map[string]string{"of": "tk-0"})

	shaped := w.create(machine.KindBug, "file", nil)
	w.force(shaped, machine.Reproducing)
	dropped := w.create(machine.KindTicket, "create", map[string]string{machine.FactBug: shaped})
	w.must(shaped, "shape", map[string]string{"ticket": dropped})
	w.force(dropped, machine.Escalated)
	w.must(dropped, "cancel", map[string]string{"reason": "dropped"})

	w.merge(w.create(machine.KindTicket, "create", nil))

	epic, child := w.epicWithChild()
	w.force(child, machine.Escalated)
	w.force(epic, machine.Escalated)
	w.must(child, "cancel", map[string]string{"reason": "dropped"})
	w.wantRetros()
}

// params is one set of parameters that satisfies every row's needCopy: a
// row ignores the ones it does not read.
func params(ticket string) map[string]string {
	return map[string]string{"run": "r-1", "stage": "", "reason": "r", "ticket": ticket, "of": "tk-0", "sha": "abc",
		"body": "b", "note": "n", "answer": "a", "question": "q", "head": "h", "evidence": machine.EvidenceCommit, "ref": "abc",
		"title": "t", "path": "docs/x.md", "spec_path": "docs/plans/x.md", "verdict": "pass"}
}

// C3: over random sequences of legal moves of epics, tickets and bugs,
// each retros row names an epic in done or a bug that BugClosed accepts,
// with its kind. No ticket and no other item holds one. A sequence starts
// each item in a random open state, and takes only moves the table allows.
// Five worlds run 100 sequences each, in parallel.
func TestC3EveryRetroNamesAFinishedEpicOrClosedBug(t *testing.T) {
	var epics, bugs atomic.Int64
	t.Run("worlds", func(t *testing.T) {
		for seed := range uint64(5) {
			t.Run(fmt.Sprint(seed), func(t *testing.T) {
				t.Parallel()
				e, b := checkSequences(t, seed, 100)
				epics.Add(e)
				bugs.Add(b)
			})
		}
	})
	// The property reads rows that exist only when moves reached them.
	if epics.Load() == 0 || bugs.Load() == 0 {
		t.Fatalf("the sequences reached %d done epics and %d closed bugs; the property tested nothing", epics.Load(), bugs.Load())
	}
}

// checkSequences runs n random sequences in one world, then checks each
// retros row. It returns the epic rows and the bug rows it checked.
func checkSequences(t *testing.T, seed uint64, n int) (epics, bugs int64) {
	w := newWorld(t)
	rng := rand.New(rand.NewPCG(seed, 1))
	open := func(k machine.Kind) string {
		tb, _ := machine.TableFor(k)
		var names []string
		for _, s := range tb.States {
			if !s.Terminal {
				names = append(names, s.Name)
			}
		}
		return names[rng.IntN(len(names))]
	}
	stages := []string{machine.StageImplementer, machine.StageReproducer, "code-reviewer", "prd-author", "planner", "decomposer"}
	for range n {
		epic := w.create(machine.KindEpic, "create_architecture", nil)
		w.force(epic, open(machine.KindEpic))
		bug := w.create(machine.KindBug, "file", nil)
		w.force(bug, open(machine.KindBug))
		items := []string{epic, bug}
		for range 1 + rng.IntN(2) {
			tk := w.create(machine.KindTicket, "create", map[string]string{machine.FactEpic: epic})
			w.force(tk, open(machine.KindTicket))
			items = append(items, tk)
		}
		for range 12 {
			id := items[rng.IntN(len(items))]
			it := w.show(id)
			next := it.AllowedNext
			if len(next) == 0 {
				continue
			}
			verb := next[rng.IntN(len(next))]
			p := params("")
			p["stage"] = stages[rng.IntN(len(stages))]
			if it.Kind == machine.KindBug && verb == "shape" {
				// The server makes the ticket a bug becomes, then shapes
				// the bug into it.
				tk := w.create(machine.KindTicket, "create", map[string]string{machine.FactBug: id})
				items = append(items, tk)
				p["ticket"] = tk
			}
			if it.Kind == machine.KindTicket && rng.IntN(3) == 0 {
				_ = w.m.MergeObserved(context.Background(), machine.MergeEvent{Project: prj, Item: id, Base: "main", Into: "main", SHA: "abc"})
				continue
			}
			_ = w.apply(id, verb, p)
		}
	}

	seen := map[string]bool{}
	for _, r := range w.retros() {
		if seen[r.Item] {
			t.Fatalf("%s holds two retros", r.Item)
		}
		seen[r.Item] = true
		it := w.show(r.Item)
		if r.State != "pending" || r.Kind != string(it.Kind) {
			t.Fatalf("retro %v names a %s", r, it.Kind)
		}
		switch it.Kind {
		case machine.KindEpic:
			if it.Status != machine.Done {
				t.Fatalf("epic %s holds a retro in %s", it.ID, it.Status)
			}
			epics++
		case machine.KindBug:
			ticket := ""
			if tk := it.Facts[machine.FactTicket]; tk != "" {
				ticket = w.show(tk).Status
			}
			if !machine.BugClosed(it.Status, ticket) {
				t.Fatalf("bug %s holds a retro in %s with its ticket in %q", it.ID, it.Status, ticket)
			}
			bugs++
		default:
			t.Fatalf("a %s holds a retro: %v", it.Kind, r)
		}
	}
	return epics, bugs
}

// C4: an item that finished under a server with no retro trigger gets no
// retro after a server with the trigger starts, and after its reconcile
// and sweep. The migration created the table empty.
func TestC4NoRetroForAnItemThatFinishedBeforeTheTrigger(t *testing.T) {
	w := newWorld(t)
	w.m = w.machine(nil)
	epic, child := w.epicWithChild()
	w.merge(child)
	bug := w.create(machine.KindBug, "file", nil)
	w.force(bug, machine.Reproducing)
	w.must(bug, "cancel", map[string]string{"reason": "a loop fault"})
	shaped := w.create(machine.KindBug, "file", nil)
	w.force(shaped, machine.Reproducing)
	ticket := w.create(machine.KindTicket, "create", map[string]string{machine.FactBug: shaped})
	w.must(shaped, "shape", map[string]string{"ticket": ticket})
	w.merge(ticket)
	if got := w.show(epic).Status; got != machine.Done {
		t.Fatalf("the epic is %s", got)
	}
	w.wantRetros()

	w.restart()
	if err := w.m.CompleteEpics(context.Background(), prj); err != nil {
		t.Fatal(err)
	}
	w.wantRetros()
}
