package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

func TestMergeSetupNeedsTheGitHubSetupFirst(t *testing.T) {
	if err := (&Merge{GitHub: &GitHub{}}).Setup(&Server{}); err == nil {
		t.Fatal("the merge setup ran before the GitHub setup")
	}
}

// project add reads the merge mechanism from the default branch and runs
// its check after the GitHub hook: github-queue needs a merge-queue rule.
func TestProjectAddChecksTheMergeMechanism(t *testing.T) {
	w := newGHWorld(t)
	mg := &Merge{GitHub: w.gh}
	if err := mg.Setup(w.srv); err != nil {
		t.Fatal(err)
	}
	gt.Git(t, w.checkout, "checkout", "-q", "main")
	gt.Commit(t, w.checkout, "config", map[string]string{config.ProjectConfigPath: "merge:\n  mechanism: github-queue\n"})
	gt.Git(t, w.checkout, "push", "-q", w.git.RemoteDir(), "main")

	ctx := context.Background()
	p := config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}
	err := w.srv.Projects.Add(ctx, p)
	var re *github.RulesError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "merge_queue") {
		t.Fatalf("add = %v; want a refusal that names the merge-queue rule", err)
	}
	w.api.SetRules(append(gt.GoodRules(), map[string]any{"type": "merge_queue"}))
	if err := w.srv.Projects.Add(ctx, p); err != nil {
		t.Fatalf("add with the rule: %v", err)
	}
	if mg.HandOff == nil {
		t.Fatal("Setup composed no hand-off job")
	}
}
