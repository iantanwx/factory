//go:build acceptance

package scenarios

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

const prdPath = "docs/prd/widgets.md"

const prdText = `# Widgets

## US-01 List widgets

A visitor sees every widget.

## US-02 Name a widget

A visitor names one widget.
`

// openRoom opens a room as the coordinator, checks the runbook its brief
// names, and returns the room's id and checkout.
func openRoom(w *acceptance.World, kind, item, reason string) (string, string) {
	w.T.Helper()
	args := []string{"room", "open", "--kind", kind, "--project", acceptance.Project, "--reason", reason, "--score", "4"}
	if item != "" {
		args = append(args, "--item", item)
	}
	r := w.Must(w.Coordinator(args...))
	room, _ := r.Data()["room"].(map[string]any)
	id, _ := room["room"].(string)
	co, _ := room["checkout"].(string)
	if id == "" || co == "" {
		w.T.Fatalf("the %s room has no id or checkout: %s", kind, r)
	}
	if got := w.CheckRunbook(co); got != kind+"-room" {
		w.T.Fatalf("the %s room's brief names stage %s", kind, got)
	}
	return id, co
}

// openBrainstorm opens a brainstorm room and returns its checkout.
func openBrainstorm(w *acceptance.World, reason string) string {
	w.T.Helper()
	_, co := openRoom(w, "brainstorm", "", reason)
	return co
}

// submitPRD commits a PRD in a brainstorm room and submits it. It returns the
// epic.
func submitPRD(w *acceptance.World, co, title string) string {
	w.T.Helper()
	w.Commit(co, "PRD: "+title, map[string]string{prdPath: prdText})
	r := w.Must(w.In(co, "prd", "submit", "--path", prdPath, "--title", title))
	epic := r.Str("epic")
	wantStatus(w, epic, machine.PRDReview)
	return epic
}

// docPR is the epic's document PR.
func docPR(w *acceptance.World, epic string) *gt.PR {
	w.T.Helper()
	n, err := strconv.Atoi(w.Show(epic).Facts[machine.FactDocPR])
	if err != nil {
		w.T.Fatalf("%s names no document PR: %v", epic, err)
	}
	return w.API.World.PR(n)
}

// docReview starts a doc-reviewer run on epic and submits verdict at the
// head it reads with `loop pr show`.
func docReview(w *acceptance.World, epic, verdict string) {
	w.T.Helper()
	run := w.Dispatch(machine.StageDocReviewer)
	if run.Item != epic {
		w.T.Fatalf("the doc-reviewer took %s, want %s", run.Item, epic)
	}
	head := w.Must(w.In(run.Checkout, "pr", "show")).Str("head_sha")
	w.Must(w.In(run.Checkout, "review", "submit", epic, "--verdict", verdict, "--head", head, "--body", "Read the document: "+verdict+"."))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
}

// syncItem brings the item's branch, when the remote holds one, into a
// run's checkout through the server's mirror, as a run that revises an
// open PR does.
func syncItem(w *acceptance.World, co string) {
	w.T.Helper()
	r := w.Must(w.In(co, "branch", "sync"))
	item, _ := r.Data()["item"].(map[string]any)
	sha, _ := item["sha"].(string)
	if sha == "" {
		return
	}
	w.GitIn(co, "fetch", "-q", r.Str("mirror"), sha)
	w.GitIn(co, "reset", "-q", "--hard", sha)
}

// draftSpec starts a spec-author run, commits a spec, and opens its PR.
func draftSpec(w *acceptance.World, epic, version string) {
	w.T.Helper()
	run := w.Dispatch(machine.StageSpecAuthor)
	if run.Item != epic {
		w.T.Fatalf("the spec-author took %s, want %s", run.Item, epic)
	}
	wantStatus(w, epic, machine.SpecDrafting)
	syncItem(w, run.Checkout)
	w.Commit(run.Checkout, "spec "+version, map[string]string{"docs/plans/" + epic + "-widgets-spec.md": "# Widgets spec\n\n" + version + "\n"})
	w.Must(w.In(run.Checkout, "branch", "push"))
	w.Must(w.In(run.Checkout, "pr", "open", "--title", "Spec: widgets", "--body", "The spec of "+epic+"."))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, epic, machine.SpecReview)
}

// ticketBody is a body of the ticket shape whose Change is change.
func ticketBody(change string) string {
	return "## Source\n\nThe widgets spec.\n\n## Change\n\n" + change + "\n\n## Contracts to prove\n\n- C1. The file holds its line.\n\n" +
		"## Files\n\n- May change: `widgets/`.\n\n## Done means\n\nThe gates pass.\n\n## Out of scope\n\nNothing.\n"
}

// F4 and AE7: a PRD from brainstorm to tickets. The doc-reviewer rejects the
// PRD, the amender revises it once, and the second reject waits in the
// operator's inbox. The answer returns it to review, a pass waits for the
// operator's merge, and the spec follows the same review. The decomposer
// turns the approved spec into tickets that cite story labels. The last
// ticket's merge closes the epic.
func TestF4AE7APRDFromBrainstormToTicketsAndDone(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	co := openBrainstorm(w, "the operator wants widgets")
	epic := submitPRD(w, co, "Widgets")
	w.Must(w.Coordinator("work", "note", epic, "--project", acceptance.Project, "--body", "The operator wrote this PRD in the room."))
	w.Must(w.Coordinator("work", "edit", epic, "--project", acceptance.Project, "--title", "Widgets v1"))

	// AE7: the first reject goes to the amender, once.
	docReview(w, epic, "reject")
	wantStatus(w, epic, machine.PRDAmending)
	am := w.Dispatch(machine.StageAmender)
	if am.Item != epic {
		t.Fatalf("the amender took %s", am.Item)
	}
	if again, _ := w.TryDispatch(machine.StageAmender); again != nil {
		t.Fatalf("a second amender run started: %+v", again)
	}
	syncItem(w, am.Checkout)
	w.Commit(am.Checkout, "amend the PRD", map[string]string{prdPath: prdText + "\nRevised once.\n"})
	w.Must(w.In(am.Checkout, "branch", "push"))
	w.Must(w.In(am.Checkout, "pr", "open", "--title", "PRD: Widgets"))
	w.Must(w.In(am.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, epic, machine.PRDReview)

	// The second reject waits for the operator, and no stage revises it.
	docReview(w, epic, "reject")
	wantStatus(w, epic, machine.Escalated)
	if st, _ := w.TryDispatch(machine.StageAmender); st != nil {
		t.Fatalf("an amender run started after the second reject: %+v", st)
	}
	entry := "escalation/" + acceptance.Project + "/" + epic
	if e := w.Entry(entry); e == nil || !contains(asString(e["text"]), "brainstorm room") {
		t.Fatalf("the escalation entry = %v", e)
	}
	w.Must(w.Coordinator("inbox", "answer", entry, "--body", "The operator revised the PRD in a room."))
	wantStatus(w, epic, machine.PRDReview)

	// A pass waits for the operator's merge.
	docReview(w, epic, "pass")
	wantStatus(w, epic, machine.PRDSignoff)
	if e := w.Entry("signoff/" + acceptance.Project + "/" + epic); e == nil {
		t.Fatal("no sign-off entry for the PRD")
	}
	w.MergeByHand(docPR(w, epic))
	w.React()
	wantStatus(w, epic, machine.SpecNeeded)

	// The spec: a reject returns it to spec_needed, a pass to sign-off.
	draftSpec(w, epic, "draft one")
	docReview(w, epic, "reject")
	wantStatus(w, epic, machine.SpecNeeded)
	draftSpec(w, epic, "draft two")
	docReview(w, epic, "pass")
	wantStatus(w, epic, machine.SpecSignoff)
	w.MergeByHand(docPR(w, epic))
	w.React()
	wantStatus(w, epic, machine.SpecApproved)

	// The decomposer's batch: two tickets, the second waits on the first.
	dec := w.Dispatch(machine.StageDecomposer)
	if dec.Item != epic {
		t.Fatalf("the decomposer took %s", dec.Item)
	}
	wantStatus(w, epic, machine.Decomposing)
	batch, _ := json.Marshal(map[string]any{"tickets": []map[string]any{
		{"key": "list", "title": "List widgets", "body": ticketBody("Create `widgets/list.txt` holding the line `list`."), "stories": []string{"US-01"}},
		{"key": "name", "title": "Name a widget", "body": ticketBody("Create `widgets/name.txt` holding the line `name`."), "stories": []string{"US-02"},
			"blocked_by": []string{"list"}},
	}})
	r := w.Must(w.In(dec.Checkout, "epic", "decompose", "--file", w.WriteFile("decomposition.json", string(batch))))
	w.Must(w.In(dec.Checkout, "run", "finish", "--outcome", "done"))
	wantStatus(w, epic, machine.Execution)
	ids := map[string]string{}
	for _, tk := range r.Data()["tickets"].([]any) {
		m := tk.(map[string]any)
		ids[m["key"].(string)] = m["item"].(string)
	}
	wantStatus(w, ids["list"], machine.Todo)
	wantStatus(w, ids["name"], machine.Backlog)
	if got := w.Show(ids["name"]).Facts[machine.FactStories]; got != "US-02" {
		t.Fatalf("the ticket cites %q", got)
	}

	// C10: a person's answer returns a ticket escalated from backlog to
	// backlog, where it waits on its open blocker. The escalate write is
	// the one the server makes when the triage run routes an escalation to a
	// person.
	res := w.Srv.Machine.Apply(context.Background(), machine.Request{Project: acceptance.Project, Item: ids["name"],
		Verb: machine.VerbEscalate, ExpectedStatus: machine.Backlog, Actor: machine.Server("escalation")})
	if res.Refusal != nil {
		t.Fatalf("escalate %s: %+v", ids["name"], res.Refusal)
	}
	w.Must(w.Coordinator("inbox", "answer", "escalation/"+acceptance.Project+"/"+ids["name"], "--body", "Keep it after the list."))
	wantStatus(w, ids["name"], machine.Backlog)

	// Both tickets land, and the last one closes the epic.
	_, head := implement(w, ids["list"], map[string]string{"widgets/list.txt": "list\n"})
	w.CI(head, "success")
	review(w, ids["list"], "pass", "--body", "C1 holds: the file holds its line.")
	merge(w, ids["list"])
	wantStatus(w, epic, machine.Execution)
	w.Must(w.Coordinator("work", "unblock", ids["name"], "--project", acceptance.Project))
	_, head = implement(w, ids["name"], map[string]string{"widgets/name.txt": "name\n"})
	w.CI(head, "success")
	review(w, ids["name"], "pass", "--body", "C1 holds: the file holds its line.")
	merge(w, ids["name"])
	wantStatus(w, epic, machine.Done)
	if n := w.DoneWriters(epic); n != 1 {
		t.Fatalf("%d writers moved %s to done", n, epic)
	}
}
