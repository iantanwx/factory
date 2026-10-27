package github

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// C24: ProposeFile makes the branch at the base head the edit read, writes
// one commit that carries the message, and opens one pull request whose
// body holds the message. A call that fails after the commit, and a call
// after the pull request opened, make no second commit and no second pull
// request.
func TestC24ProposeFileWritesOneCommitOverTheBaseItRead(t *testing.T) {
	e := newEnv(t)
	w := e.api.World
	ctx := context.Background()
	base := strings.Repeat("a", 40)
	w.SetFile(base, config.ProjectConfigPath, "stages:\n  implementer: {}\n")
	msg := "chore(settings): set stages.implementer.cap to 3\n\nfewer runs\n\nLoop-Setting: stages.implementer.cap\n"
	f := FileChange{Branch: "loop/settings/7", Path: config.ProjectConfigPath, BaseSHA: base,
		Content: []byte("stages:\n  implementer: {cap: 3}\n"), Message: msg,
		Title: "chore(settings): set stages.implementer.cap to 3", Body: "```\n" + msg + "```\n"}

	w.FailNext("POST /repos/"+gt.Repo+"/pulls", 502)
	if _, _, err := e.svc.ProposeFile(ctx, e.project, f); err == nil {
		t.Fatal("ProposeFile answered no error when the pull request did not open")
	}
	pr, head, err := e.svc.ProposeFile(ctx, e.project, f)
	if err != nil {
		t.Fatal(err)
	}
	again, head2, err := e.svc.ProposeFile(ctx, e.project, f)
	if err != nil {
		t.Fatal(err)
	}

	commits := w.Commits(f.Branch)
	if len(commits) != 1 {
		t.Fatalf("the branch holds %d commits, want 1: %+v", len(commits), commits)
	}
	c := commits[0]
	if c.Parent != base || c.SHA != head || c.Message != msg || c.Path != config.ProjectConfigPath || c.Content != string(f.Content) {
		t.Fatalf("commit = %+v; want one commit over %s with the message and the content, head %s", c, base, head)
	}
	prs := w.PRs()
	if len(prs) != 1 || prs[0].Head != f.Branch || prs[0].Base != "main" || prs[0].Title != f.Title || prs[0].Body != f.Body {
		t.Fatalf("pull requests = %+v; want one from %s into main", prs, f.Branch)
	}
	if pr.Number != prs[0].Number || again.Number != pr.Number || head2 != head {
		t.Fatalf("calls answered #%d %s and #%d %s; want #%d and %s twice", pr.Number, head, again.Number, head2, prs[0].Number, c.SHA)
	}
}

// ProposeFile never writes the base branch or an item's branch.
func TestProposeFileRefusesTheBaseAndAnItemBranch(t *testing.T) {
	e := newEnv(t)
	for _, b := range []string{"main", BranchFor("tk-1")} {
		_, _, err := e.svc.ProposeFile(context.Background(), e.project, FileChange{Branch: b, Path: config.ProjectConfigPath,
			BaseSHA: strings.Repeat("a", 40), Content: []byte("x"), Message: "m", Title: "t"})
		if err == nil {
			t.Fatalf("ProposeFile wrote %s", b)
		}
	}
	if len(e.api.World.Commits("main")) != 0 || len(e.api.World.Commits(BranchFor("tk-1"))) != 0 || len(e.api.World.PRs()) != 0 {
		t.Fatalf("ProposeFile wrote a commit or opened a pull request: %v", e.api.Calls())
	}
}
