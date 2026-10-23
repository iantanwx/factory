package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
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

// merge status lists the PRs a mechanism holds, longest wait first, to
// the coordinator and to the operator's own shell. --all adds the PRs
// that left the pipeline, and --project narrows the list. A run cannot
// read it.
func TestMergeStatusListsThePipeline(t *testing.T) {
	w := newGHWorld(t)
	if err := (&Merge{GitHub: w.gh}).Setup(w.srv); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	err := w.srv.Store.Write(ctx, func(tx *store.Tx) error {
		for _, r := range []struct {
			project      string
			pr           int
			state        string
			reason       string
			attempts     int
			enq, updated int64
		}{
			{"alpha", 3, "requeued", "other", 2, 1000, 3000},
			{"alpha", 1, "handed", "conflict", 2, 2000, 2000},
			{"alpha", 2, "merged", "", 1, 1500, 4000},
			{"beta", 7, "handed", "", 1, 2500, 2500},
		} {
			if _, err := tx.Exec(`INSERT INTO merge_tracking (project, pr_number, item, head_sha, mechanism, state, reason, attempts,
				enqueued_at, updated_at) VALUES (?, ?, ?, ?, 'direct', ?, ?, ?, ?, ?)`,
				r.project, r.pr, fmt.Sprintf("tk-%d", r.pr), strings.Repeat("a", 40), r.state, r.reason, r.attempts, r.enq, r.updated); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	prs := func(env map[string]any) []string {
		t.Helper()
		list, ok := env["data"].([]any)
		if env["ok"] != true || !ok {
			t.Fatalf("merge status = %v", env)
		}
		var out []string
		for _, x := range list {
			r := x.(map[string]any)
			out = append(out, fmt.Sprintf("%s#%v %s %v", r["project"], r["pr"], r["state"], r["since"]))
		}
		return out
	}

	coord := t.TempDir()
	if _, err := w.srv.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: "coord-1", Kind: cli.PrincipalCoordinator,
		Checkout: coord}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(coord)
	r := cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return coord, nil }}).Attach(r)
	var out bytes.Buffer
	r.Run(ctx, []string{"merge", "status"}, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("no envelope: %v\n%s", err, out.String())
	}
	if got, want := prs(env), []string{"alpha#1 handed 2000", "beta#7 handed 2500", "alpha#3 requeued 3000"}; !slices.Equal(got, want) {
		t.Fatalf("merge status = %v; want %v", got, want)
	}
	first := env["data"].([]any)[0].(map[string]any)
	if first["reason"] != "conflict" || first["attempts"] != float64(2) || first["enqueued_at"] != float64(2000) || first["item"] != "tk-1" {
		t.Fatalf("merge status row = %v", first)
	}

	if _, err := rpc.EnsureAdminCredential(w.dir); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	env, code, _ := call(t, w.dir, 5*time.Second, nil, "merge", "status", "--project", "alpha", "--all")
	if got, want := prs(env), []string{"alpha#1 handed 2000", "alpha#3 requeued 3000", "alpha#2 merged 4000"}; code != 0 || !slices.Equal(got, want) {
		t.Fatalf("merge status --project alpha --all from the host = %d %v; want %v", code, got, want)
	}

	w.claim()
	if env := w.run("merge", "status"); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("a run's merge status = %v", env)
	}
}
