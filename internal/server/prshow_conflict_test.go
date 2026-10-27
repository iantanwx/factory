package server

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// C-bug-1: `pr show` reports a conflict on an open PR whose head does not
// merge cleanly with its base, even when GitHub's lazily computed
// mergeable state still says clean. The implementer runbook merges the
// base only on dirty, so a stale clean sends a real conflict to blocked.
func TestPRShowReportsAConflictThatGitHubStillCallsClean(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	w.claim()
	head := gt.Commit(t, w.checkout, "work", map[string]string{"same.go": "package a // from the ticket\n"})
	if env := w.run("branch", "push"); env["ok"] != true {
		t.Fatalf("branch push = %v", env)
	}
	if env := w.run("pr", "open", "--title", "Add same.go", "--body", "Adds same.go."); env["ok"] != true {
		t.Fatalf("pr open = %v", env)
	}

	// Another ticket lands the same path on main with other content: an
	// add/add conflict, as on factorio-m6yz (PR #145).
	gt.Git(t, w.checkout, "switch", "-q", "main")
	gt.Commit(t, w.checkout, "other ticket", map[string]string{"same.go": "package a // from another ticket\n"})
	gt.Git(t, w.checkout, "push", "-q", w.git.RemoteDir(), "main")
	gt.Git(t, w.checkout, "switch", "-q", "work")

	// GitHub has not recomputed mergeability after the base moved.
	w.api.World.Update(w.api.World.PR(1), func(p *gt.PR) { p.MergeableState = "clean" })

	env := w.run("pr", "show")
	data, _ := env["data"].(map[string]any)
	if env["ok"] != true || data["head_sha"] != head {
		t.Fatalf("pr show = %v", env)
	}
	if data["mergeable_state"] != "dirty" {
		t.Fatalf("pr show mergeable_state = %v, want dirty: head %s conflicts with main in same.go", data["mergeable_state"], head[:7])
	}
}
