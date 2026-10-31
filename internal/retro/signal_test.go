package retro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

const prj = "alpha"

// world is one store and fake beads under a machine whose Applied hook is
// SignalsFor, or nil.
type world struct {
	t  *testing.T
	st *store.Store
	bx *fake.Client
	m  *machine.Machine
}

func newWorld(t *testing.T, hook bool) *world {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	bx := fake.New("tk")
	if err := bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	cfg := machine.Config{Store: st, Beads: func(string) (beadsx.Client, error) { return bx, nil },
		Registry: cli.Surface(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if hook {
		cfg.Applied = SignalsFor
	}
	m, err := machine.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &world{t: t, st: st, bx: bx, m: m}
}

// item makes an item of kind in status, with the given loop facts.
func (w *world) item(kind machine.Kind, status string, facts map[string]string) string {
	w.t.Helper()
	tb, _ := machine.TableFor(kind)
	meta := map[string]string{}
	for k, v := range facts {
		meta["loop."+k] = v
	}
	is, err := w.bx.Create(context.Background(), beadsx.CreateSpec{Title: string(kind), Type: tb.BeadType, Metadata: meta})
	if err != nil {
		w.t.Fatal(err)
	}
	w.set(is.ID, status)
	return is.ID
}

// set moves an item in beads with no transition, to stage the next move.
func (w *world) set(id, status string) {
	w.t.Helper()
	ctx := context.Background()
	is, err := w.bx.Get(ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	if is.Status == status {
		return
	}
	if _, err := w.bx.Update(ctx, id, beadsx.UpdateSpec{IfStatus: is.Status, Status: status}); err != nil {
		w.t.Fatal(err)
	}
}

// apply stages id in from and applies verb as the server.
func (w *world) apply(id, from, verb string, params map[string]string) {
	w.t.Helper()
	w.set(id, from)
	res := w.m.Apply(context.Background(), machine.Request{Project: prj, Item: id, Verb: verb, Params: params, Actor: machine.Server("test")})
	if res.Refusal != nil {
		w.t.Fatalf("%s %s from %s: %+v", verb, id, from, *res.Refusal)
	}
}

// signals is the signals list work show prints for id.
func (w *world) signals(id string) []Signal {
	w.t.Helper()
	on, err := OnItem(context.Background(), w.st, prj, id)
	if err != nil {
		w.t.Fatal(err)
	}
	out := make([]Signal, 0, len(on.Signals))
	for _, raw := range on.Signals {
		var s Signal
		if err := json.Unmarshal(raw, &s); err != nil {
			w.t.Fatalf("signal %s: %v", raw, err)
		}
		out = append(out, s)
	}
	return out
}

// transitions are the ids of id's applied moves, in order.
func (w *world) transitions(id string) []CaseRef {
	w.t.Helper()
	h, err := w.m.History(context.Background(), prj, id)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []CaseRef
	for _, tr := range h {
		out = append(out, CaseRef(fmt.Sprintf("transition:%d", tr.IntentID)))
	}
	return out
}

func (w *world) status(id string) string {
	w.t.Helper()
	it, ref := w.m.Show(context.Background(), prj, id)
	if ref != nil {
		w.t.Fatal(*ref)
	}
	return it.Status
}

// C5: a ticket's first red build writes no signal. Its second writes one
// red_build signal, with both transitions as cases, while its epic is in
// execution. A third adds its case to the same row. ci_red and dequeue_red
// count together.
func TestC5SecondRedBuildWritesASignal(t *testing.T) {
	w := newWorld(t, true)
	epic := w.item(machine.KindEpic, machine.Execution, nil)
	tk := w.item(machine.KindTicket, machine.InReview, map[string]string{machine.FactEpic: epic})

	w.apply(tk, machine.InReview, machine.VerbCIRed, nil)
	if got := w.signals(tk); len(got) != 0 {
		t.Fatalf("one red build wrote %+v", got)
	}

	w.apply(tk, machine.Queued, "dequeue_red", nil)
	cases := w.transitions(tk)
	got := w.signals(tk)
	if len(got) != 1 || got[0].Kind != SignalRedBuild || got[0].Scope != "" || !reflect.DeepEqual(got[0].Cases, cases) || len(cases) != 2 {
		t.Fatalf("after two red builds: %+v, want one red_build with cases %v", got, cases)
	}
	if s := w.status(epic); s != machine.Execution {
		t.Fatalf("epic is %s, want %s", s, machine.Execution)
	}

	w.apply(tk, machine.InReview, machine.VerbCIRed, nil)
	cases = w.transitions(tk)
	got = w.signals(tk)
	if len(got) != 1 || len(cases) != 3 || !reflect.DeepEqual(got[0].Cases, cases) {
		t.Fatalf("after three red builds: %+v, want one row with cases %v", got, cases)
	}
}

// C7: the second review_reject from prd_review writes second_reject with
// scope prd, and the second from spec_review writes it with scope spec.
func TestC7SecondDocumentRejectWritesASignal(t *testing.T) {
	for _, tc := range []struct{ from, scope string }{{machine.PRDReview, "prd"}, {machine.SpecReview, "spec"}} {
		t.Run(tc.scope, func(t *testing.T) {
			w := newWorld(t, true)
			ep := w.item(machine.KindEpic, tc.from, nil)
			w.apply(ep, tc.from, "review_reject", nil)
			if got := w.signals(ep); len(got) != 0 {
				t.Fatalf("one reject wrote %+v", got)
			}
			w.apply(ep, tc.from, "review_reject", nil)
			want := w.transitions(ep)
			got := w.signals(ep)
			if len(got) != 1 || got[0].Kind != SignalSecondReject || got[0].Scope != tc.scope || !reflect.DeepEqual(got[0].Cases, want) {
				t.Fatalf("after two rejects: %+v, want one second_reject scope %s with cases %v", got, tc.scope, want)
			}
		})
	}
}

// C7: one PRD reject and one spec reject are no second reject.
func TestC7OnePRDAndOneSpecRejectWriteNone(t *testing.T) {
	w := newWorld(t, true)
	ep := w.item(machine.KindEpic, machine.PRDReview, nil)
	w.apply(ep, machine.PRDReview, "review_reject", nil)
	w.apply(ep, machine.SpecReview, "review_reject", nil)
	if got := w.signals(ep); len(got) != 0 {
		t.Fatalf("a PRD reject and a spec reject wrote %+v", got)
	}
}

// move is one step of a generated sequence: stage the item in from, then
// apply verb.
type move struct {
	epic   bool
	from   string
	verb   string
	params map[string]string
}

var moves = []move{
	{from: machine.InReview, verb: machine.VerbCIRed},
	{from: machine.Queued, verb: "dequeue_red"},
	{from: machine.Repair, verb: "note", params: map[string]string{"body": "a handoff"}},
	{epic: true, from: machine.PRDReview, verb: "review_reject"},
	{epic: true, from: machine.SpecReview, verb: "review_reject"},
}

// C8: the same moves, applied with the Applied hook on and with it nil,
// leave each item with the same status, facts and last note, and the same
// transitions. Only signals differ.
func TestC8ASignalChangesNothingElse(t *testing.T) {
	total := 0
	for seed := range uint64(20) {
		r := rand.New(rand.NewPCG(seed, 8))
		seq := make([]move, 4+r.IntN(10))
		for i := range seq {
			seq[i] = moves[r.IntN(len(moves))]
		}
		on, off := newWorld(t, true), newWorld(t, false)
		var ids [2][2]string
		for i, w := range []*world{on, off} {
			ep := w.item(machine.KindEpic, machine.PRDReview, nil)
			ids[i] = [2]string{w.item(machine.KindTicket, machine.InReview, map[string]string{machine.FactEpic: ep}), ep}
		}
		if ids[0] != ids[1] {
			t.Fatalf("seed %d: the two worlds named different items: %v", seed, ids)
		}
		for _, mv := range seq {
			id := ids[0][0]
			if mv.epic {
				id = ids[0][1]
			}
			on.apply(id, mv.from, mv.verb, mv.params)
			off.apply(id, mv.from, mv.verb, mv.params)
		}
		signals := 0
		for _, id := range ids[0] {
			a, b := on.snapshot(id), off.snapshot(id)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("seed %d, %s:\nhook on  %+v\nhook off %+v", seed, id, a, b)
			}
			if got := off.signals(id); len(got) != 0 {
				t.Fatalf("seed %d: the nil hook wrote %+v", seed, got)
			}
			signals += len(on.signals(id))
		}
		total += signals
	}
	if total == 0 {
		t.Fatal("no generated sequence wrote a signal, so the comparison proves nothing")
	}
}

// snapshot is what a signal must not change: status, facts and history.
type snap struct {
	Status  string
	Facts   map[string]string
	History []machine.Transition
}

func (w *world) snapshot(id string) snap {
	w.t.Helper()
	it, ref := w.m.Show(context.Background(), prj, id)
	if ref != nil {
		w.t.Fatal(*ref)
	}
	h, err := w.m.History(context.Background(), prj, id)
	if err != nil {
		w.t.Fatal(err)
	}
	for i := range h {
		h[i].At = time.Time{}
	}
	return snap{Status: it.Status, Facts: it.Facts, History: h}
}
