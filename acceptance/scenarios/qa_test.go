//go:build acceptance

package scenarios

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/qa"
)

// verifyWeb is the one verify skill of the QA world: the web app over
// apps/web/**.
const verifyWeb = "---\nname: verify-web\ndescription: drive the web app\npaths:\n  - apps/web/**\n---\n"

// qaReport starts a QA run on item, hands in a report of one fuzzy UAT
// check of web at the reviewed head with result, and finishes the run. A
// failed check cites one evidence file. It returns the head the run drove.
func qaReport(w *acceptance.World, item, result string) string {
	w.T.Helper()
	run := w.Dispatch(machine.StageQA)
	if run.Item != item {
		w.T.Fatalf("the QA run took %s, want %s", run.Item, item)
	}
	head := w.Show(item).Fact(machine.FactReviewedHead)
	check := qa.Check{Kind: "fuzzy_uat", App: "web", Result: result, Goal: "open the page", Steps: 4, Minutes: 2,
		Findings: []qa.Finding{}}
	dir := w.T.TempDir()
	if result == "fail" {
		check.Findings = []qa.Finding{{Summary: "the page shows v1, not the title", Severity: "fail",
			Evidence: []string{"page.txt"}, Steps: []string{"open apps/web/page.txt"}}}
		if err := os.WriteFile(filepath.Join(dir, "page.txt"), []byte("v1\n"), 0o644); err != nil {
			w.T.Fatal(err)
		}
	}
	raw, err := json.Marshal(qa.Report{Head: head, Verify: "present", Checks: []qa.Check{check}, Bugs: []string{}})
	if err != nil {
		w.T.Fatal(err)
	}
	report := w.WriteFile("qa-"+result+".json", string(raw))
	w.Must(w.In(run.Checkout, "qa", "report", item, "--head", head, "--file", report, "--evidence", dir))
	w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "done"))
	return head
}

// wantQACheck fails unless the fake GitHub holds one loop/qa check run at
// head, with conclusion.
func wantQACheck(w *acceptance.World, head, conclusion string) {
	w.T.Helper()
	var qas []map[string]any
	for _, r := range w.API.World.CheckRunsAt(head) {
		if r["name"] == "loop/qa" {
			qas = append(qas, r)
		}
	}
	if len(qas) != 1 || qas[0]["conclusion"] != conclusion {
		w.T.Fatalf("loop/qa check runs at %s = %v, want one with %s", head, qas, conclusion)
	}
}

// C34 of docs/plans/factorio-nhb5-qa-stage-spec.md: in a project whose
// config does not list qa, one ticket meets a QA run at each reviewed
// head, and merges only after a pass. The first QA run fails head A, the
// rework pushes head B, and the second QA run passes B. loop/eligible
// reaches B only.
func TestC34ATicketMeetsEachQARunAtItsReviewedHeadAndMergesAfterAPass(t *testing.T) {
	cfg := strings.Replace(acceptance.DefaultConfig, acceptance.QAOff, "", 1)
	if cfg == acceptance.DefaultConfig {
		t.Fatal("DefaultConfig holds no QAOff line")
	}
	w := newWorld(t, acceptance.Options{Config: cfg,
		Files: map[string]string{".claude/skills/verify-web/SKILL.md": verifyWeb}})
	id := createTicket(w, "Add the web page", "Create `apps/web/page.txt` holding its title.\n\n"+
		"## Files\n\nMay change: `apps/web/page.txt`.\n")

	_, a := implement(w, id, map[string]string{"apps/web/page.txt": "v1\n"})
	w.CI(a, "success")
	review(w, id, "pass")
	wantStatus(w, id, machine.InQA)
	if got := qaReport(w, id, "fail"); got != a {
		t.Fatalf("the first QA run drove %s, want the reviewed head %s", got, a)
	}
	wantStatus(w, id, machine.Todo)
	w.React()
	wantQACheck(w, a, "failure")

	b := rework(w, id, map[string]string{"apps/web/page.txt": "Add the web page\n"})
	w.CI(b, "success")
	review(w, id, "pass")
	wantStatus(w, id, machine.InQA)
	if got := qaReport(w, id, "pass"); got != b {
		t.Fatalf("the second QA run drove %s, want the reviewed head %s", got, b)
	}
	wantStatus(w, id, machine.ReadyToMerge)
	merge(w, id)
	wantQACheck(w, a, "failure")
	wantQACheck(w, b, "success")

	if ms := w.API.World.Merges(); len(ms) != 1 || ms[0].SHA != b {
		t.Fatalf("merges = %+v, want one at B %s", ms, b)
	}
	for _, c := range []struct {
		head string
		want bool
	}{{a, false}, {b, true}} {
		if got := slices.Contains(w.API.World.Posted(), c.head+" "+gt.Eligible+" success"); got != c.want {
			t.Fatalf("loop/eligible at %s = %t, want %t: %v", c.head, got, c.want, w.API.World.Posted())
		}
	}

	hist, err := w.Srv.Machine.History(context.Background(), acceptance.Project, id)
	if err != nil {
		t.Fatal(err)
	}
	claims := 0
	for _, tr := range hist {
		if tr.Verb == "claim" && tr.From == machine.InQA {
			claims++
		}
	}
	var qaRounds []string
	for _, r := range machine.Rounds(hist) {
		if r.Stage == machine.StageQA {
			qaRounds = append(qaRounds, r.Verdict+" "+r.Head)
		}
	}
	if want := []string{"reject " + a, "pass " + b}; claims != 2 || !slices.Equal(qaRounds, want) {
		t.Fatalf("%d QA claims and QA rounds %q; want 2 claims and %q", claims, qaRounds, want)
	}
}
