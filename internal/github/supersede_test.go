package github

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// The evidence of work supersede holds only when the base reaches the
// commit, or holds the ADR, after a fetch; and an open pull request on the
// ticket's branch refuses whatever the evidence.
func TestSupersedeEvidenceIsReadBackFromTheBase(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	id := e.ticket(machine.Todo, "")
	check := func(kind, ref string) *cli.Refusal { return e.svc.SupersedeEvidence(ctx, testProject, id, kind, ref) }
	wantRule := func(r *cli.Refusal, code, rule string) {
		t.Helper()
		if r == nil || r.Code != code || r.Rule != rule {
			t.Fatalf("want %s %s, got %+v", code, rule, r)
		}
	}

	if r := check(machine.EvidenceCommit, e.base[:7]); r != nil {
		t.Fatalf("the base's own commit: %+v", r)
	}
	// A commit pushed to main after the mirror last fetched still counts.
	gt.Commit(t, e.checkout, "adr", map[string]string{"docs/adr/0003-drop.md": "# 3. Drop it\n"})
	gt.Git(t, e.checkout, "push", "-q", e.git.RemoteDir(), "HEAD:main")
	if r := check(machine.EvidenceADR, "docs/adr/0003-drop.md"); r != nil {
		t.Fatalf("an ADR on the base: %+v", r)
	}
	wantRule(check(machine.EvidenceADR, "docs/adr/0004-none.md"), cli.CodeNotFound, "adr_not_on_base")
	wantRule(check(machine.EvidenceADR, "docs/notes.md"), cli.CodeUsage, "not_an_adr")
	local := gt.Commit(t, e.checkout, "never pushed", map[string]string{"x.txt": "x\n"})
	wantRule(check(machine.EvidenceCommit, local), cli.CodeNotFound, "commit_not_on_base")

	pr := w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: local})
	wantRule(check(machine.EvidenceDone, "tk-9"), cli.CodeTransitionNotAllowed, "open_pr")
	wantRule(check(machine.EvidenceCommit, e.base[:7]), cli.CodeTransitionNotAllowed, "open_pr")
	w.Update(pr, func(p *gt.PR) { p.State = "closed" })
	if r := check(machine.EvidenceDone, "tk-9"); r != nil {
		t.Fatalf("a closed pull request: %+v", r)
	}
}
