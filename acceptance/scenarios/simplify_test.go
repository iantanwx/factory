//go:build acceptance

package scenarios

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
)

// simplifyWorld makes a world whose config does not list the simplify
// stage, and checks by `loop settings show` that the project runs the
// stage by its default (C9).
func simplifyWorld(t *testing.T) *acceptance.World {
	t.Helper()
	w := newWorld(t, acceptance.Options{Config: strings.Replace(acceptance.DefaultConfig, "  simplify: {enabled: false}\n", "", 1)})
	for _, r := range w.Must(w.Coordinator("settings", "show", acceptance.Project)).Data()["settings"].([]any) {
		row := r.(map[string]any)
		if row["key"] == "stages.simplify.enabled" {
			if row["value"] != "true" || row["source"] != "default" {
				t.Fatalf("stages.simplify.enabled = %v from %v, want true from default", row["value"], row["source"])
			}
			return w
		}
	}
	t.Fatal("settings show names no stages.simplify.enabled")
	return nil
}

// simplifyRun starts a simplify run on item, brings the pull request head
// in, commits files over it and pushes. The server holds the commit. It
// returns the run and the held head.
func simplifyRun(w *acceptance.World, item string, files map[string]string) (acceptance.Run, string) {
	w.T.Helper()
	wantStatus(w, item, machine.InSimplify)
	run := w.Dispatch(machine.StageSimplify)
	if run.Item != item {
		w.T.Fatalf("the simplify stage took %s, want %s", run.Item, item)
	}
	syncItem(w, run.Checkout)
	head := w.Commit(run.Checkout, "simplify "+item, files)
	if r := w.Must(w.In(run.Checkout, "branch", "push")); r.Data()["held"] != true {
		w.T.Fatalf("the simplify push is not held: %s", r)
	}
	return run, head
}

// transitionsOf counts the applied moves of item by verb from status.
func transitionsOf(w *acceptance.World, item, verb, from string) int {
	w.T.Helper()
	return w.Count(`SELECT count(*) FROM transitions WHERE item = ? AND verb = ? AND from_status = ?`, item, verb, from)
}

// C14 and C13: one ticket goes through the implementer, a simplify run
// that pushes one commit, and a code-reviewer that passes. The review
// waits for green CI at the simplify head, and its one review is there.
func TestC14OneTicketMeetsOneReviewAtTheSimplifyHead(t *testing.T) {
	w := simplifyWorld(t)
	id := createTicket(w, "Add a", "Create `a.txt` holding the line `a`.")
	_, from := implementRun(w, id, map[string]string{"a.txt": "a\n"})
	run, head := simplifyRun(w, id, map[string]string{"a.txt": "a.\n"})
	w.Must(w.In(run.Checkout, "pr", "open", "--title", "Implement "+id))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	w.SyncPRHeads()
	if it := w.Show(id); it.Status != machine.InReview || it.Fact(machine.FactHead) != head || it.Fact(machine.FactSimplifyFrom) != from {
		t.Fatalf("after the hand-off: %s head %q simplify_from %q, want in_review at %s from %s",
			it.Status, it.Fact(machine.FactHead), it.Fact(machine.FactSimplifyFrom), head, from)
	}

	if st, ref := w.TryDispatch(machine.StageCodeReviewer); st != nil || ref != nil {
		t.Fatalf("a review started before CI reported at the simplify head: %+v %+v", st, ref)
	}
	w.CI(head, "success")
	review(w, id, "pass")
	if got := w.Show(id).Fact(machine.FactReviewedHead); got != head {
		t.Fatalf("reviewed head = %q, want the simplify head %s", got, head)
	}
	if n := transitionsOf(w, id, machine.VerbSimplified, machine.InSimplify); n != 1 {
		t.Fatalf("%d simplified moves, want 1", n)
	}
	if n := transitionsOf(w, id, "claim", machine.InReview); n != 1 {
		t.Fatalf("%d code-reviewer claims, want 1", n)
	}
}

// C14: a simplify run that holds a commit and then ends with no finish
// leaves the pull request at simplify_from. The code-reviewer claims the
// ticket with no CI wait, and its one review is at simplify_from.
func TestC14ACrashedSimplifyRunLeavesTheReviewAtTheImplementersHead(t *testing.T) {
	w := simplifyWorld(t)
	id := createTicket(w, "Add b", "Create `b.txt` holding the line `b`.")
	_, from := implementRun(w, id, map[string]string{"b.txt": "b\n"})
	run, _ := simplifyRun(w, id, map[string]string{"b.txt": "b.\n"})
	crash(w, run)
	w.SyncPRHeads()
	if it := w.Show(id); it.Status != machine.InReview || it.Fact(machine.FactHead) != from {
		t.Fatalf("after the crash: %s head %q, want in_review at %s", it.Status, it.Fact(machine.FactHead), from)
	}
	if got := w.PRFor(id).HeadSHA; got != from {
		t.Fatalf("pull request head = %s, want simplify_from %s", got, from)
	}

	rev := w.Dispatch(machine.StageCodeReviewer)
	if rev.Item != id {
		t.Fatalf("the code-reviewer took %s, want %s", rev.Item, id)
	}
	w.CI(from, "success")
	w.Must(w.In(rev.Checkout, "review", "submit", id, "--verdict", "pass", "--head", from))
	w.Must(w.In(rev.Checkout, "run", "finish", "--outcome", "done"))
	if got := w.Show(id).Fact(machine.FactReviewedHead); got != from {
		t.Fatalf("reviewed head = %q, want simplify_from %s", got, from)
	}
	if n := transitionsOf(w, id, "reclaim", machine.InSimplify); n != 1 {
		t.Fatalf("%d reclaims from in_simplify, want 1", n)
	}
}

// A simplify run that finishes failed passes its ticket on to review, and
// counts no failure.
func TestAFailedSimplifyRunPassesTheTicketToReview(t *testing.T) {
	w := simplifyWorld(t)
	id := createTicket(w, "Add c", "Create `c.txt` holding the line `c`.")
	implementRun(w, id, map[string]string{"c.txt": "c\n"})
	wantStatus(w, id, machine.InSimplify)
	run := w.Dispatch(machine.StageSimplify)
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "failed"))
	wantStatus(w, id, machine.InReview)
	if n := counted(w); n != 0 {
		t.Fatalf("%d counted failures, want 0", n)
	}
}
