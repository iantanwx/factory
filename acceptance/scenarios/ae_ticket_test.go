//go:build acceptance

package scenarios

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// AE1 (R8, R12): a ticket passed review at sha A and is ready to merge.
// A new commit B lands on the PR, so the ticket returns to review, and no
// merge starts at B without a new pass. The same holds once the merge
// interface holds the PR: a push returns the queued ticket to review.
func TestAE1NewHeadAfterAPassReturnsTheTicketToReview(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	id, a := readyToMerge(w, "Add a", "a.txt")
	w.React()

	b := w.PushBranch("loop/"+id, "a commit after the pass", map[string]string{"b.txt": "b\n"})
	w.CI(b, "success")
	w.React()
	it := w.Show(id)
	if it.Status != machine.InReview || it.Fact(machine.FactReviewedHead) != "" || it.Fact(machine.FactHead) != b {
		t.Fatalf("after B: %s reviewed %q head %q (A %s)", it.Status, it.Fact(machine.FactReviewedHead), it.Fact(machine.FactHead), a)
	}
	w.HandOff()
	w.HandOff()
	if n := len(w.API.World.Merges()); n != 0 {
		t.Fatalf("%d merges started at B with no pass at B", n)
	}

	// A pass at B, a hand-off, then a push C while the mechanism holds it.
	review(w, id, "pass")
	w.React()
	w.HandOff()
	wantStatus(w, id, machine.Queued)
	c := w.PushBranch("loop/"+id, "a commit after the hand-off", map[string]string{"c.txt": "c\n"})
	w.CI(c, "success")
	w.HandOff()
	if it := w.Show(id); it.Status != machine.InReview || it.Fact(machine.FactHead) != c {
		t.Fatalf("after C: %s head %q", it.Status, it.Fact(machine.FactHead))
	}
	if n := len(w.API.World.Merges()); n != 0 {
		t.Fatalf("%d merges started at C with no pass at C", n)
	}

	review(w, id, "pass")
	merge(w, id)
	if ms := w.API.World.Merges(); len(ms) != 1 || ms[0].SHA != c {
		t.Fatalf("merges = %+v, want one at C", ms)
	}
}

// AE3 (R5, R19): a ticket is in review, and its reviewer asks for a move
// the table does not allow. The CLI exits 3 and the state does not change.
func TestAE3AMoveTheTableRefusesExits3(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	id := createTicket(w, "Add a", "Create `a.txt` holding the line `a`.")
	implement(w, id, map[string]string{"a.txt": "a\n"})
	rev := w.Dispatch(machine.StageCodeReviewer)
	before := w.Show(id)
	for _, args := range [][]string{{"work", "cancel", id}, {"work", "unblock", id}} {
		r := w.In(rev.Checkout, args...)
		if r.Code != 3 || r.RefusalCode() != cli.CodeTransitionNotAllowed || r.Env["current_status"] != machine.InReview {
			t.Fatalf("%s", r)
		}
	}
	after := w.Show(id)
	if after.Status != machine.InReview || after.Seq != before.Seq || after.Owner != rev.RunID {
		t.Fatalf("the state changed: %+v -> %+v", before, after)
	}
}

// AE8 (R53): the project's manifest names STRIPE_KEY, and the store holds
// no value for it. No run starts, one inbox item names STRIPE_KEY, and the
// failure count does not change. Once the host stores it, the run starts
// and its .env holds the name.
func TestAE8AMissingManifestSecretStartsNoRun(t *testing.T) {
	w := newWorld(t, acceptance.Options{Files: map[string]string{
		".loop/secrets.yaml": "secrets:\n  - name: STRIPE_KEY\n    source: op://fixture/payments/key\n",
	}})
	id := createTicket(w, "Add a", "Create `a.txt` holding the line `a`.")
	for range 2 {
		st, ref := w.TryDispatch(machine.StageImplementer)
		if st != nil || ref == nil || ref.Code != cli.CodeMissingSecret || !strings.Contains(ref.Hint, "STRIPE_KEY") {
			t.Fatalf("dispatch = %+v %+v", st, ref)
		}
	}
	if n := w.Count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("%d runs recorded", n)
	}
	if n := counted(w); n != 0 {
		t.Fatalf("%d failures counted", n)
	}
	var named []map[string]any
	for _, e := range w.Entries("") {
		if strings.Contains(e["text"].(string)+" "+asString(e["title"]), "STRIPE_KEY") {
			named = append(named, e)
		}
	}
	if len(named) != 1 {
		t.Fatalf("%d inbox entries name STRIPE_KEY: %v", len(named), w.Entries(""))
	}
	wantStatus(w, id, machine.Todo)

	w.Must(w.HostCLI(strings.NewReader("sk-fixture-not-a-key"), "secret", "set", "STRIPE_KEY", "--project", acceptance.Project))
	run := w.Dispatch(machine.StageImplementer)
	env, err := os.ReadFile(filepath.Join(run.Checkout, ".env"))
	if err != nil || !strings.Contains(string(env), "STRIPE_KEY=") {
		t.Fatal("the run's .env does not name STRIPE_KEY")
	}
	if r := w.In(run.Checkout, "secret", "check", "STRIPE_KEY"); r.Code != 0 || strings.Contains(r.Raw, "sk-fixture") {
		t.Fatalf("secret check printed a value or failed: exit %d", r.Code)
	}
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// AE9 (R55): an implementer run hits a fault in the loop, writes a report
// and files it. It gets F-1, and the coordinator's inbox holds one fault
// entry. A later run reads open. After the coordinator resolves it as
// fixed in v1.4.0, a run reads fixed, fixed_in and the release this host
// runs.
func TestAE9ALoopFaultReportReachesTheCoordinatorAndItsResolutionReachesRuns(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	createTicket(w, "Add a", "Create `a.txt` holding the line `a`.")
	run := w.Dispatch(machine.StageImplementer)
	report := w.WriteFile("fault.md", "`loop branch push` answered INTERNAL with no hint. I expected a push.\n")
	r := w.Must(w.In(run.Checkout, "fault", "report", "--file", report))
	if r.Str("fault") != "F-1" || r.Str("status") != "open" {
		t.Fatalf("%s", r)
	}
	var faults []map[string]any
	for _, e := range w.Entries("coordinator") {
		if e["kind"] == "fault" {
			faults = append(faults, e)
		}
	}
	if len(faults) != 1 || faults[0]["entry"] != "fault/F-1" {
		t.Fatalf("fault entries = %v", faults)
	}
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "blocked", "--fault", "F-1"))

	later := w.Dispatch(machine.StageImplementer)
	if r := w.Must(w.In(later.Checkout, "fault", "show", "F-1")); r.Str("status") != "open" {
		t.Fatalf("%s", r)
	}
	w.Must(w.Coordinator("fault", "resolve", "F-1", "--fixed-in", "v1.4.0"))
	r = w.Must(w.In(later.Checkout, "fault", "show", "F-1"))
	if r.Str("status") != "fixed" || r.Str("fixed_in") != "v1.4.0" || r.Str("installed") != cli.Version {
		t.Fatalf("%s", r)
	}
	if w.Entry("fault/F-1") != nil {
		t.Fatal("a fixed fault stays in the inbox")
	}
}
