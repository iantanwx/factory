package github

import (
	"context"
	"net/http"
	"testing"

	"github.com/iantanwx/loop/internal/machine"
)

func (e *env) effect(sha string) (state string, attempts int) {
	e.t.Helper()
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		return tx.QueryRow(`SELECT state, attempts FROM outbox WHERE effect_kind = ? AND natural_key = ?`,
			EffectStatus, statusKey(testProject, sha, EligibleContext)).Scan(&state, &attempts)
	}); err != nil {
		e.t.Fatal(err)
	}
	return state, attempts
}

func (e *env) exec(query string, args ...any) {
	e.t.Helper()
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		_, err := tx.Exec(query, args...)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) enqueue() int {
	e.t.Helper()
	n, err := e.svc.EnqueueEligibility(context.Background(), e.project)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) posted(sha string) bool {
	e.t.Helper()
	ok, err := e.svc.EligiblePosted(context.Background(), testProject, sha)
	if err != nil {
		e.t.Fatal(err)
	}
	return ok
}

// A loop/eligible post that failed for good is made runnable again an
// hour after the failure, while the ticket waits at the same head. The
// post then lands, so the hand-off can go ahead.
func TestFailedEligibilityIsRevived(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	id := e.ticket(machine.ReadyToMerge, shaA)
	post := "POST /repos/" + testRepo + "/statuses/" + shaA
	ctx := context.Background()
	if e.enqueue() != 1 {
		t.Fatal("no effect enqueued")
	}
	e.exec(`UPDATE outbox SET attempts = ?`, maxEffectAttempts-1)
	e.api.World.FailNext(post, http.StatusUnprocessableEntity)
	_ = e.svc.RunStatusEffects(ctx)
	if state, _ := e.effect(shaA); state != "failed" || e.alerts(AlertEffectFailed) != 1 || e.posted(shaA) {
		t.Fatalf("after the last 422 the effect is %s, alerts %d", state, e.alerts(AlertEffectFailed))
	}

	if e.enqueue() != 0 {
		t.Fatal("the failed effect was revived before the wait")
	}
	if state, _ := e.effect(shaA); state != "failed" {
		t.Fatalf("the effect is %s before the wait", state)
	}

	e.exec(`UPDATE outbox SET updated_at = updated_at - ?`, eligibleRevive.Milliseconds()+1)
	if e.enqueue() != 1 {
		t.Fatal("the failed effect was not revived")
	}
	if state, attempts := e.effect(shaA); state != "runnable" || attempts != 0 {
		t.Fatalf("the revived effect is %s after %d attempts", state, attempts)
	}
	if err := e.svc.RunStatusEffects(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.posted(shaA) || e.status(id).Status != machine.ReadyToMerge {
		t.Fatalf("loop/eligible is not posted on %s", shaA)
	}
	if e.alerts(AlertEffectFailed) != 1 {
		t.Fatal("the revival raised another alert")
	}
}

// A 500 is transient: it never fails the effect, even on its last
// counted attempt.
func TestStatus500KeepsTheEffectRunnable(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	e.ticket(machine.ReadyToMerge, shaA)
	e.enqueue()
	e.exec(`UPDATE outbox SET attempts = ?`, maxEffectAttempts-1)
	e.api.World.FailNext("POST /repos/"+testRepo+"/statuses/"+shaA, http.StatusInternalServerError)
	_ = e.svc.RunStatusEffects(context.Background())
	if state, _ := e.effect(shaA); state != "runnable" || e.alerts(AlertEffectFailed) != 0 {
		t.Fatalf("after a 500 the effect is %s", state)
	}
}

// A restore from an older snapshot drops the review pass's history and
// its outbox row, and beads keeps the ticket in ready_to_merge. The bead's
// reviewed head still enqueues the post.
func TestEligibilityEnqueuesWithNoReviewPassHistory(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	e.ticket(machine.InReview, shaB) // history the snapshot keeps
	id := e.ticket(machine.ReadyToMerge, shaA)
	e.exec(`DELETE FROM outbox`)
	e.exec(`DELETE FROM transitions WHERE item = ?`, id)
	e.exec(`DELETE FROM intents WHERE item = ?`, id)
	if e.enqueue() != 1 {
		t.Fatal("no effect enqueued for the restored ticket")
	}
	if err := e.svc.RunStatusEffects(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.posted(shaA) {
		t.Fatalf("loop/eligible is not posted on %s", shaA)
	}
}
