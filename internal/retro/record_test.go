package retro_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// queue puts one pending retro on item, as the Terminal hook does.
func (w *world) queue(project, item string) {
	w.t.Helper()
	ev := machine.TerminalEvent{Project: project, Item: item, Kind: machine.KindEpic, Status: machine.Done}
	if err := w.st.Write(context.Background(), func(tx *store.Tx) error { return retro.Enqueue(tx, ev) }); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) exec(query string, args ...any) {
	w.t.Helper()
	if err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
}

func (w *world) bind(runID string) (retro.Retro, bool) {
	w.t.Helper()
	r, ok, err := retro.Bind(context.Background(), w.st, prj, runID)
	if err != nil {
		w.t.Fatalf("bind %s: %v", runID, err)
	}
	return r, ok
}

// record calls `loop retro record` as a run of stage would.
func (w *world) record(stage, runID string, comments retro.CommentReader) cli.Result {
	w.t.Helper()
	svc := &retro.Service{World: retro.World{Store: w.st, Machine: w.m, Comments: comments}}
	h, ok := svc.Handlers()["retro record"]
	if !ok {
		w.t.Fatal("no handler for retro record")
	}
	return h(context.Background(), &rpc.Call{Principal: rpc.Principal{Kind: cli.PrincipalRun, RunID: runID, Project: prj, Stage: stage}})
}

// C24: two runs that bind one pending retro at once bind it to one run.
// The other run gets no retro.
func TestC24ConcurrentBindsBindOnePendingRetroToOneRun(t *testing.T) {
	w := newWorld(t)
	w.queue(prj, "ep-1")
	const runs = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		won  []string
		errs []error
	)
	for i := range runs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r, ok, err := retro.Bind(context.Background(), w.st, prj, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if ok {
				if r.RunID != id || r.Item != "ep-1" || r.State != retro.StateRunning {
					errs = append(errs, fmt.Errorf("run %s bound %+v", id, r))
				}
				won = append(won, id)
			}
		}(fmt.Sprintf("r-%d", i))
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(won) != 1 {
		t.Fatalf("%d runs bound the one retro: %v", len(won), won)
	}
	var runID, state string
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT run_id, state FROM retros WHERE project = ? AND item = 'ep-1'`, prj).Scan(&runID, &state)
	}); err != nil {
		t.Fatal(err)
	}
	if runID != won[0] || state != "running" {
		t.Fatalf("the row names run %q in %s, and run %s won", runID, state, won[0])
	}
}

// C24: a second bind by the holding run returns the same retro, though an
// older pending one now waits, so a run never holds two. Another run binds
// the oldest pending retro of its own project only.
func TestC24ARepeatedBindReturnsTheRetroTheRunHolds(t *testing.T) {
	w := newWorld(t)
	w.queue(prj, "ep-b")
	w.queue("beta", "ep-other")
	w.queue(prj, "ep-c")
	if r, ok := w.bind("r-1"); !ok || r.Item != "ep-b" {
		t.Fatalf("first bind of r-1 = %+v %v, want ep-b", r, ok)
	}
	// ep-a is older than every row, as a retro queued before ep-b would
	// be. A bind by the holding run still returns ep-b.
	w.exec(`INSERT INTO retros (project, item, kind, state, created_at) VALUES (?, 'ep-a', 'epic', 'pending', 0)`, prj)
	want := retro.Retro{Project: prj, Item: "ep-b", Kind: machine.KindEpic, State: retro.StateRunning, RunID: "r-1"}
	if r, ok := w.bind("r-1"); !ok || r != want {
		t.Fatalf("second bind of r-1 = %+v %v, want %+v", r, ok, want)
	}
	if r, ok := w.bind("r-2"); !ok || r.Item != "ep-a" {
		t.Fatalf("bind of r-2 = %+v %v, want ep-a, the oldest pending", r, ok)
	}
	if r, ok := w.bind("r-3"); !ok || r.Item != "ep-c" {
		t.Fatalf("bind of r-3 = %+v %v, want ep-c", r, ok)
	}
	if r, ok := w.bind("r-4"); ok {
		t.Fatalf("bind of r-4 = %+v, and the project holds no pending retro", r)
	}
	w.wantRetros(row{"ep-a", "epic", "running"}, row{"ep-b", "epic", "running"}, row{"ep-c", "epic", "running"})
}

// With no pending retro, `loop retro record` exits NOTHING and binds
// nothing.
func TestRetroRecordWithNoPendingRetroIsNothing(t *testing.T) {
	w := newWorld(t)
	w.queue("beta", "ep-other")
	res := w.record(retro.Stage, "r-1", nil)
	if res.Refusal != nil || res.Exit != cli.ExitNothing {
		t.Fatalf("record = %+v, want exit NOTHING", res)
	}
}

// Only a retro run reads a record: a run of another stage is refused and
// binds nothing.
func TestRetroRecordRefusesARunOfAnotherStage(t *testing.T) {
	w := newWorld(t)
	w.queue(prj, "ep-1")
	res := w.record("implementer", "r-1", nil)
	if res.Refusal == nil || res.Refusal.Code != cli.CodeCapabilityDenied {
		t.Fatalf("record by an implementer = %+v, want CAPABILITY_DENIED", res)
	}
	w.wantRetros(row{"ep-1", "epic", "pending"})
}

// C9: for an epic with two tickets, `loop retro record` holds, for each of
// the three items, every review round and its findings, every run and its
// outcome class, every escalation and its route, every loop fault whose
// item it is, every comment on its pull requests, every note and every
// signal. Each element carries a case reference that Parse accepts. An
// item outside the epic adds nothing.
func TestC9RecordHoldsTheWholeHistoryOfAnEpicAndItsTickets(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	epic := w.create(machine.KindEpic, "create_architecture", nil)
	t1 := w.create(machine.KindTicket, "create", map[string]string{machine.FactEpic: epic})
	t2 := w.create(machine.KindTicket, "create", map[string]string{machine.FactEpic: epic})
	stray := w.create(machine.KindTicket, "create", nil)

	// Review rounds: a PRD reject on the epic, a reject and a pass on t1,
	// a pass on t2.
	w.force(epic, machine.PRDReview)
	w.must(epic, "review_reject", map[string]string{"body": "the PRD names no user", "head": "e1"})
	w.force(epic, machine.Execution)
	w.force(t1, machine.InReview)
	w.must(t1, "review_reject", map[string]string{"body": "C3 has no test", "head": "a1"})
	w.force(t1, machine.InReview)
	w.must(t1, "review_pass", map[string]string{"head": "a2"})
	w.force(t2, machine.InReview)
	w.must(t2, "review_pass", map[string]string{"body": "clean", "head": "b1"})

	// Notes.
	w.must(epic, machine.VerbNote, map[string]string{"body": "the spec waits on vlle"})
	w.must(t2, machine.VerbNote, map[string]string{"body": "handoff: rebase done"})
	w.must(stray, machine.VerbNote, map[string]string{"body": "not in the epic"})

	// Runs, with one failure note.
	at := func(m int) int64 { return time.Date(2026, 10, 31, 9, m, 0, 0, time.UTC).UnixMilli() }
	w.exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES (?, 'o/r', 0, 0)`, prj)
	run := func(id, stage, item, state, class string, m int) {
		w.exec(`INSERT INTO runs (run_id, project, stage, item, state, outcome_class, started_at, ended_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, prj, stage, item, state, class, at(m), at(m+1))
	}
	run("r-prd", "prd-author", epic, "settled", "done", 1)
	run("r-1", "implementer", t1, "settled", "failed", 2)
	run("r-2", "implementer", t1, "settled", "done", 4)
	run("r-3", "implementer", t2, "settled", "done", 6)
	run("r-stray", "implementer", stray, "settled", "done", 8)
	w.exec(`INSERT INTO failures (run_id, project, item, stage, inputs_hash, class, note, recorded_at) VALUES ('r-1', ?, ?, 'implementer', 'h', 'failed', 'gates red three times', ?)`,
		prj, t1, at(3))

	// Escalations, each with its route.
	w.exec(`INSERT INTO escalations (project, item, run_id, stage, level, choice, finding, state, created_at, updated_at) VALUES (?, ?, 'r-1', 'implementer', 4, 'inside', 'the contract reads two ways', 'person', ?, ?)`,
		prj, t1, at(10), at(11))
	w.exec(`INSERT INTO escalations (project, item, run_id, stage, level, choice, finding, state, created_at, updated_at) VALUES (?, ?, 'r-prd', 'prd-author', 5, 'outside', 'a vendor bill', 'answered', ?, ?)`,
		prj, epic, at(12), at(13))

	// Loop faults whose item is one of the three.
	w.exec(`INSERT INTO faults (text, reporter_kind, run_id, project, item, stage, status, reported_at, updated_at) VALUES ('loop pr open timed out', 'run', 'r-3', ?, ?, 'implementer', 'open', ?, ?)`,
		prj, t2, at(14), at(14))
	w.exec(`INSERT INTO faults (text, reporter_kind, run_id, project, item, stage, status, reported_at, updated_at) VALUES ('the stray fault', 'run', 'r-stray', ?, ?, 'implementer', 'open', ?, ?)`,
		prj, stray, at(15), at(15))

	// A signal on t1.
	if err := retro.RecordAttempts(ctx, w.st, prj, t1, "implementer", []string{"r-1", "r-2"}); err != nil {
		t.Fatal(err)
	}

	// Comments on each item's pull requests.
	said := map[string][]retro.Comment{
		epic:  {{ID: 501, Kind: "conversation", Author: "owner", Body: "split the spec", At: time.UnixMilli(at(20))}},
		t1:    {{ID: 601, Kind: "review", Author: "owner", Body: "rename this", At: time.UnixMilli(at(21))}, {ID: 602, Kind: "review", Author: "loop-app", Body: "renamed", At: time.UnixMilli(at(22))}},
		stray: {{ID: 701, Kind: "conversation", Author: "owner", Body: "stray", At: time.UnixMilli(at(23))}},
	}
	var asked []string
	comments := func(_ context.Context, project string, it machine.Item) ([]retro.Comment, *cli.Refusal) {
		if project != prj {
			t.Errorf("comments read project %s", project)
		}
		asked = append(asked, it.ID)
		return said[it.ID], nil
	}

	w.queue(prj, epic)
	res := w.record(retro.Stage, "r-retro", comments)
	if res.Refusal != nil || res.Exit != cli.ExitDone {
		t.Fatalf("record = %+v", res)
	}
	raw, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatal(err)
	}
	var got retro.Record
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	// The machine stamps transitions and signals with its own clock, so
	// each such case must carry a time, and the comparison drops it.
	for _, ir := range append([]retro.ItemRecord{got.Item}, got.Children...) {
		for _, cs := range [][]retro.Case{ir.Rounds, ir.Notes, ir.Signals} {
			for i := range cs {
				if cs[i].At.IsZero() {
					t.Errorf("%s case %s has no time", ir.ID, cs[i].Ref)
				}
				cs[i].At = time.Time{}
			}
		}
		for _, cs := range [][]retro.Case{ir.Rounds, ir.Runs, ir.Escalations, ir.Faults, ir.Comments, ir.Notes, ir.Signals} {
			for _, c := range cs {
				if _, err := retro.Parse(string(c.Ref)); err != nil {
					t.Errorf("%s: %v", ir.ID, err)
				}
			}
		}
	}

	note := func(item, body string) retro.Case {
		t.Helper()
		hist, err := w.m.History(ctx, prj, item)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hist {
			if h.Verb == machine.VerbNote && h.Fact(machine.FactNote) == body {
				return retro.Case{Ref: retro.CaseRef(fmt.Sprintf("transition:%d", h.IntentID)), Text: "server:test: " + body}
			}
		}
		t.Fatalf("no note %q on %s", body, item)
		return retro.Case{}
	}
	ms := func(m int) time.Time { return time.UnixMilli(at(m)) }
	want := retro.Record{
		Retro: retro.Retro{Project: prj, Item: epic, Kind: machine.KindEpic, State: retro.StateRunning, RunID: "r-retro"},
		Item: retro.ItemRecord{ID: epic, Kind: "epic", Title: "item", Status: machine.Execution,
			Rounds: []retro.Case{{Ref: retro.CaseRef("round:" + epic + ":1"), Text: "reject at e1: the PRD names no user"}},
			Runs:   []retro.Case{{Ref: "run:r-prd", At: ms(1), Text: "prd-author run settled, outcome done"}},
			Escalations: []retro.Case{{Ref: "escalation:2", At: ms(12),
				Text: "level 5, outside, from prd-author, route answered: a vendor bill"}},
			Comments: []retro.Case{{Ref: retro.CaseRef("comment:" + epic + ":501"), At: ms(20), Text: "owner, conversation: split the spec"}},
			Notes:    []retro.Case{note(epic, "the spec waits on vlle")},
		},
		Children: []retro.ItemRecord{
			{ID: t1, Kind: "ticket", Title: "item", Status: machine.ReadyToMerge,
				Rounds: []retro.Case{
					{Ref: retro.CaseRef("round:" + t1 + ":1"), Text: "reject at a1: C3 has no test"},
					{Ref: retro.CaseRef("round:" + t1 + ":2"), Text: "pass at a2"},
				},
				Runs: []retro.Case{
					{Ref: "run:r-1", At: ms(2), Text: "implementer run settled, outcome failed: gates red three times"},
					{Ref: "run:r-2", At: ms(4), Text: "implementer run settled, outcome done"},
				},
				Escalations: []retro.Case{{Ref: "escalation:1", At: ms(10),
					Text: "level 4, inside, from implementer, route person: the contract reads two ways"}},
				Comments: []retro.Case{
					{Ref: retro.CaseRef("comment:" + t1 + ":601"), At: ms(21), Text: "owner, review: rename this"},
					{Ref: retro.CaseRef("comment:" + t1 + ":602"), At: ms(22), Text: "loop-app, review: renamed"},
				},
				Signals: []retro.Case{{Ref: "signal:1", Text: "attempts implementer: run:r-1, run:r-2"}},
			},
			{ID: t2, Kind: "ticket", Title: "item", Status: machine.ReadyToMerge,
				Rounds: []retro.Case{{Ref: retro.CaseRef("round:" + t2 + ":1"), Text: "pass at b1: clean"}},
				Runs:   []retro.Case{{Ref: "run:r-3", At: ms(6), Text: "implementer run settled, outcome done"}},
				Faults: []retro.Case{{Ref: "fault:F-1", At: ms(14), Text: "open, from implementer: loop pr open timed out"}},
				Notes:  []retro.Case{note(t2, "handoff: rebase done")},
			},
		},
	}
	// Each list shares its elements with the record, so the loop writes
	// through it.
	normalise := func(r retro.Record) {
		for _, ir := range append([]retro.ItemRecord{r.Item}, r.Children...) {
			for _, cs := range [][]retro.Case{ir.Rounds, ir.Runs, ir.Escalations, ir.Faults, ir.Comments, ir.Notes, ir.Signals} {
				for i := range cs {
					cs[i].At = cs[i].At.UTC()
				}
			}
		}
	}
	normalise(got)
	normalise(want)
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", "  ")
		x, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("record:\n%s\nwant:\n%s", g, x)
	}
	if !reflect.DeepEqual(asked, []string{epic, t1, t2}) {
		t.Fatalf("comments read for %v, want the epic and its two tickets", asked)
	}
}
