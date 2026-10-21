package machine

import (
	"context"
	"database/sql"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/store"
)

// stopAt makes the machine stop at one kill point, as a crash there would.
func (w *world) stopAt(point string) {
	w.m.hook = func(p string) bool { return p == point }
}

// verbCount counts the intents of one verb on an item in a state, and the
// history rows of that verb.
func (w *world) verbCount(id, verb, state string) (intents, history int) {
	intents = w.count(`SELECT count(*) FROM intents WHERE item = ? AND verb = ? AND state = ?`, id, verb, state)
	history = w.count(`SELECT count(*) FROM transitions WHERE item = ? AND verb = ?`, id, verb)
	return intents, history
}

// runKillPoints stops a write after the intent commit, after the beads
// write and after the applied mark. Each ends, after a restart and its
// reconcile, with exactly one applied transition and one history row. A
// second restart changes nothing.
func runKillPoints(t *testing.T, mk func(t *testing.T) *world) {
	for _, point := range []string{pointRecorded, pointWritten, pointMarked} {
		t.Run(point, func(t *testing.T) {
			w := mk(t)
			id := w.create(KindTicket, "create", nil)
			w.stopAt(point)
			if res := w.apply(id, "escalate", nil); res.Refusal == nil || res.Refusal.Code != cli.CodeInternal {
				t.Fatalf("a write stopped at %s answered %+v", point, res)
			}
			for range 2 {
				w.restart()
				applied, history := w.verbCount(id, "escalate", "applied")
				if applied != 1 || history != 1 {
					t.Fatalf("after reconcile: %d applied, %d history rows; want 1 and 1", applied, history)
				}
				if n := w.count(`SELECT count(*) FROM intents WHERE state = 'pending'`); n != 0 {
					t.Fatalf("%d intents still pending", n)
				}
				if it := w.show(id); it.Status != Escalated || it.Fact(FactResume) != Todo {
					t.Fatalf("bead after reconcile: %+v", it)
				}
				if w.alerts(AlertDivergence) != 0 {
					t.Fatal("a clean replay raised a divergence alert")
				}
			}
		})
	}
}

func TestKillPointsFake(t *testing.T) { runKillPoints(t, newWorld) }

// A stale intent waits while the bead goes from A to B and back to A
// under a new epoch. The intent is refused with LEASE_NOT_HELD.
func TestStaleIntentAfterABAUnderANewEpoch(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.create(KindTicket, "create", nil)
	w.m.hook = func(p string) bool {
		if p == pointRecorded {
			w.bx.SetUnavailable(true)
		}
		return false
	}
	ref := wantCode(t, w.apply(id, "escalate", nil), cli.CodeBeadsUnavailable)
	if !ref.Retryable && ref.RetryAfterSeconds == 0 {
		t.Fatalf("exit 4 must say when to retry: %+v", ref)
	}
	w.bx.SetUnavailable(false)
	empty, e1 := "", fence(1)
	if _, err := w.bc.Update(ctx, id, beadsx.UpdateSpec{IfStatus: Todo, IfAssignee: &empty, Status: InProgress, Assignee: &e1,
		SetMetadata: map[string]string{beadsx.MetaEpoch: "1"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bc.Update(ctx, id, beadsx.UpdateSpec{IfStatus: InProgress, IfAssignee: &e1, Status: Todo}); err != nil {
		t.Fatal(err)
	}
	w.restart()
	if refused, _ := w.verbCount(id, "escalate", "refused"); refused != 1 {
		t.Fatal("the stale intent was not refused")
	}
	var result string
	if err := w.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT result FROM intents WHERE item = ? AND verb = 'escalate'`, id).Scan(&result)
	}); err != nil {
		t.Fatal(err)
	}
	env, err := cli.EnvelopeFromJSON([]byte(result))
	if err != nil || env.Refusal == nil || env.Code != cli.CodeLeaseNotHeld {
		t.Fatalf("stored answer %s", result)
	}
	if it := w.show(id); it.Status != Todo {
		t.Fatalf("the stale intent wrote: %+v", it)
	}
}

// I1 lands in beads and the server stops before it marks it. I2 then lands
// on the same item. Reconcile reports I1 as applied, not refused, because
// the bead's sequence passed I1's.
func TestAppliedIntentBehindALaterWriteIsApplied(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.create(KindTicket, "create", nil)
	w.stopAt(pointWritten)
	w.apply(id, "escalate", nil)
	empty := ""
	if _, err := w.bc.Update(ctx, id, beadsx.UpdateSpec{IfStatus: Escalated, IfAssignee: &empty,
		SetMetadata: map[string]string{beadsx.MetaIntentSeq: "3", factPrefix + FactNote: "i2"}}); err != nil {
		t.Fatal(err)
	}
	w.restart()
	if applied, history := w.verbCount(id, "escalate", "applied"); applied != 1 || history != 1 {
		t.Fatalf("I1: %d applied, %d history rows", applied, history)
	}
	var seq int64
	if err := w.st.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT last_seq FROM item_seq WHERE item = ?`, id).Scan(&seq)
	}); err != nil || seq != 3 {
		t.Fatalf("SQLite sequence = %d, %v; want 3", seq, err)
	}
}

// A bd timeout after the commit: the server re-reads the bead's intent
// sequence and answers exit 0, never exit 3.
func TestUnclearWriteThatLandedAnswersDone(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	w.bx.InjectUnclear(1)
	res := w.apply(id, "escalate", nil)
	mustOK(t, res)
	if applied, history := w.verbCount(id, "escalate", "applied"); applied != 1 || history != 1 {
		t.Fatalf("%d applied, %d history rows", applied, history)
	}
}

// An unclear write that did not land stays pending, answers exit 4, and
// the retry of the same key joins it.
func TestRetryOfAnInFlightKeyJoinsIt(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	key := &Key{Key: "k-1", Fingerprint: fingerprint("r-1", "work note")}
	w.m.hook = func(p string) bool {
		if p == pointRecorded {
			w.bx.SetUnavailable(true)
		}
		return false
	}
	req := Request{Project: prj, Item: id, Verb: "note", Params: map[string]string{"body": "x"}, Key: key, Actor: Server("test")}
	wantCode(t, w.m.Apply(context.Background(), req), cli.CodeBeadsUnavailable)
	w.m.hook = nil
	w.bx.SetUnavailable(false)
	mustOK(t, w.m.Apply(context.Background(), req))
	if applied, history := w.verbCount(id, "note", "applied"); applied != 1 || history != 1 {
		t.Fatalf("%d applied, %d history rows", applied, history)
	}
	res := w.m.Apply(context.Background(), req)
	mustOK(t, res)
	if !res.Replayed {
		t.Fatal("a settled key must answer its stored result")
	}
}

func fingerprint(run, verb string) store.Fingerprint {
	return store.Fingerprint{RunID: run, Verb: verb, ArgsHash: "args"}
}

func TestOutsideStatusChangeRaisesDivergenceAndIsKept(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindTicket, "create", nil)
	intents := w.count(`SELECT count(*) FROM intents`)
	w.force(id, InReview, nil)
	w.restart()
	if w.alerts(AlertDivergence) != 1 {
		t.Fatal("no divergence alert")
	}
	if it := w.show(id); it.Status != InReview {
		t.Fatalf("reconcile overwrote the bead: %+v", it)
	}
	if n := w.count(`SELECT count(*) FROM intents`); n != intents {
		t.Fatal("reconcile recorded a write")
	}
}

// A write Dolt lost is written again through the status check.
func TestLostWriteIsWrittenAgain(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id := w.create(KindTicket, "create", nil)
	w.must(id, "escalate", nil)
	// The bead goes back to its state before the write, as a lost commit.
	empty := ""
	if _, err := w.bc.Update(ctx, id, beadsx.UpdateSpec{IfStatus: Escalated, IfAssignee: &empty, Status: Todo,
		SetMetadata:   map[string]string{beadsx.MetaIntentSeq: "1"},
		UnsetMetadata: []string{factPrefix + FactResume, factPrefix + FactResumeEntry}}); err != nil {
		t.Fatal(err)
	}
	w.restart()
	it := w.show(id)
	if it.Status != Escalated || it.Seq != 2 || it.Fact(FactResume) != Todo {
		t.Fatalf("the lost write was not written again: %+v", it)
	}
	if _, history := w.verbCount(id, "escalate", "applied"); history != 1 || w.alerts(AlertDivergence) != 0 {
		t.Fatalf("history %d, divergence %d", history, w.alerts(AlertDivergence))
	}
}
