package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/escalation"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// issue gives run the item's lease for stage and writes its token into
// the checkout, as dispatch does.
func (w *ghWorld) issue(item, stage, run string) {
	w.t.Helper()
	ctx := context.Background()
	lease, res := w.srv.Machine.Claim(ctx, machine.ClaimRequest{Project: "alpha", Item: item, Stage: stage, RunID: run})
	if res.Refusal != nil {
		w.t.Fatalf("claim: %+v", *res.Refusal)
	}
	w.token(item, stage, run, lease.Epoch)
}

func (w *ghWorld) token(item, stage, run string, epoch int64) {
	w.t.Helper()
	if _, err := w.srv.Tokens.IssueToCheckout(context.Background(), rpc.IssueRequest{RunID: run, Kind: cli.PrincipalRun,
		Project: "alpha", Item: item, Stage: stage, Checkout: w.checkout, Epoch: epoch}); err != nil {
		w.t.Fatal(err)
	}
}

// A reviewer submits its verdict with its comment in one call: the server
// posts the comment on the item's PR and records it as the review fact. A
// comment that holds a secret posts nothing and records nothing.
func TestReviewSubmitPostsItsBodyOnThePR(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	id := w.claim()
	gt.Commit(t, w.checkout, "work", map[string]string{"a.txt": "a\n"})
	if env := w.run("branch", "push"); env["ok"] != true {
		t.Fatalf("branch push = %v", env)
	}
	if env := w.run("pr", "open", "--title", "Add a"); env["ok"] != true {
		t.Fatalf("pr open = %v", env)
	}
	w.issue(id, machine.StageCodeReviewer, "run-2")

	env := w.run("review", "submit", id, "--verdict", "reject", "--body", "the key is "+ghSecret)
	if env["code"] != cli.CodeSecretInOutput {
		t.Fatalf("review submit with a secret = %v", env)
	}
	if n := len(w.api.World.Comments(1)); n != 0 {
		t.Fatalf("%d comments reached GitHub", n)
	}
	env = w.run("review", "submit", id, "--verdict", "reject", "--body", "Fix the test in a_test.go.")
	if env["ok"] != true {
		t.Fatalf("review submit = %v", env)
	}
	cs := w.api.World.Comments(1)
	if len(cs) != 1 || !strings.Contains(cs[0]["body"].(string), "Fix the test in a_test.go.") {
		t.Fatalf("PR comments = %v", cs)
	}
	it, _ := w.srv.Machine.Show(ctx, "alpha", id)
	if it.Status != machine.Todo || it.Fact(machine.FactReview) != "Fix the test in a_test.go." {
		t.Fatalf("after the reject: %s %v", it.Status, it.Facts)
	}
}

// A code pass needs green CI at the head it names (alpha pr-comment.ts
// 74-91). A pass at red or pending CI is refused: it posts no comment,
// records no reviewed head, and so loop/eligible is never posted. A pass
// at green CI lands.
func TestReviewPassNeedsGreenCIAtItsHead(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	prj := config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}
	if err := w.srv.Projects.Add(ctx, prj); err != nil {
		t.Fatal(err)
	}
	id := w.claim()
	gt.Commit(t, w.checkout, "work", map[string]string{"a.txt": "a\n"})
	if env := w.run("branch", "push"); env["ok"] != true {
		t.Fatalf("branch push = %v", env)
	}
	if env := w.run("pr", "open", "--title", "Add a"); env["ok"] != true {
		t.Fatalf("pr open = %v", env)
	}
	head := w.api.World.PR(1).HeadSHA
	w.issue(id, machine.StageCodeReviewer, "run-2")
	at := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	refused := func(rule string) {
		t.Helper()
		env := w.run("review", "submit", id, "--verdict", "pass", "--head", head, "--body", "Looks right.")
		if env["code"] != cli.CodeCINotGreen || env["rule"] != rule {
			t.Fatalf("a pass at %s CI = %v", rule, env)
		}
		it, _ := w.srv.Machine.Show(ctx, "alpha", id)
		if it.Status != machine.InReview || it.Fact(machine.FactReviewedHead) != "" {
			t.Fatalf("a refused pass wrote: %s %v", it.Status, it.Facts)
		}
		if n := len(w.api.World.Comments(1)); n != 0 {
			t.Fatalf("a refused pass posted %d comments", n)
		}
		if n, err := w.gh.Service.EnqueueEligibility(ctx, prj); err != nil || n != 0 {
			t.Fatalf("a refused pass enqueued %d loop/eligible posts: %v", n, err)
		}
	}

	// Each refusal is not stored under the call's idempotency key, so the
	// same call gets a fresh answer once CI changes.
	refused("ci_pending") // no check has reported yet
	w.api.World.SetRuns(head, gt.CheckRun("ci", head, "completed", "failure", at))
	refused("ci_failed")
	w.api.World.SetRuns(head, gt.CheckRun("ci", head, "in_progress", "", at))
	refused("ci_pending")
	// The branch rules require "ci": another green check does not stand in.
	w.api.World.SetRuns(head, gt.CheckRun("lint", head, "completed", "success", at))
	refused("ci_pending")

	w.api.World.SetRuns(head, gt.CheckRun("ci", head, "completed", "success", at))
	if env := w.run("review", "submit", id, "--verdict", "pass", "--head", head, "--body", "Looks right."); env["ok"] != true {
		t.Fatalf("a pass at green CI = %v", env)
	}
	it, _ := w.srv.Machine.Show(ctx, "alpha", id)
	if it.Status != machine.ReadyToMerge || it.Fact(machine.FactReviewedHead) != head {
		t.Fatalf("after the pass: %s %v", it.Status, it.Facts)
	}
}

// The fixer reads and notes the item of the escalation it routes, though
// its token holds no item. Any other item stays refused, and so does any
// other write on the escalated item.
func TestFixerNotesTheEscalatedItem(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	m := w.srv.Machine
	mk := func() string {
		id, res := m.Create(ctx, machine.CreateRequest{Project: "alpha", Kind: machine.KindTicket, Title: "t", Verb: "create",
			Actor: machine.Server("test")})
		if res.Refusal != nil {
			t.Fatalf("create: %+v", *res.Refusal)
		}
		return id
	}
	escalated, other := mk(), mk()
	if err := w.srv.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO escalations (project, item, level, choice, finding, state, created_at, updated_at)
			VALUES ('alpha', ?, 4, 'inside', 'Which side wins?', ?, 0, 0)`, escalated, escalation.StateAwaitingPanel)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.token("", escalation.FixerStage, "run-fix", 0)
	w.t.Chdir(w.checkout)
	w.cli = cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return w.checkout, nil }}).Attach(w.cli)

	if env := w.run("work", "show", escalated); env["ok"] != true {
		t.Fatalf("work show of the escalated item = %v", env)
	}
	if env := w.run("work", "note", escalated, "--body", "F1 routed to the loop: the spec wins."); env["ok"] != true {
		t.Fatalf("work note of the escalated item = %v", env)
	}
	if it, _ := m.Show(ctx, "alpha", escalated); it.Fact(machine.FactNote) != "F1 routed to the loop: the spec wins." {
		t.Fatalf("note = %q", it.Fact(machine.FactNote))
	}
	env := w.run("work", "note", other, "--body", "not mine")
	if env["code"] != cli.CodeCapabilityDenied || env["rule"] != "item_not_held" {
		t.Fatalf("work note of another item = %v", env)
	}
	env = w.run("work", "edit", escalated, "--title", "mine now")
	if env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("work edit of the escalated item = %v", env)
	}
	// A run of another stage gets no such allowance.
	w.token("", machine.StageImplementer, "run-impl", 0)
	if env := w.run("work", "note", escalated, "--body", "x"); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("an implementer's note of the escalated item = %v", env)
	}
}

// doctor-github answers the server's GitHub checks to the host-admin
// caller, and a run cannot call it.
func TestDoctorGithubVerb(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.EnsureAdminCredential(w.dir); err != nil {
		t.Fatal(err)
	}
	env, code, _ := call(t, w.dir, 5*time.Second, nil, "doctor-github")
	if code != 0 {
		t.Fatalf("doctor-github = %d %v", code, env)
	}
	list, _ := env["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("doctor-github data = %v", env["data"])
	}
	p := list[0].(map[string]any)
	if p["project"] != "alpha" {
		t.Fatalf("project = %v", p)
	}
	var checks []string
	for _, l := range p["lines"].([]any) {
		checks = append(checks, l.(map[string]any)["check"].(string))
	}
	if !strings.Contains(strings.Join(checks, " "), "project_config") {
		t.Fatalf("checks = %v", checks)
	}

	w.claim()
	if env := w.run("doctor-github"); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("a run's doctor-github = %v", env)
	}
}
