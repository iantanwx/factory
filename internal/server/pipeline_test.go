package server

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

const docConfig = "stages:\n  doc-reviewer: {}\n  amender: {}\n  spec-author: {}\n  decomposer: {}\nmerge:\n  mechanism: direct\n"

// newDocWorld is a server with rooms, the scheduler, the GitHub actor over
// synthetic GitHub and the pipeline. Each checkout is a worktree of a
// clone of the synthetic remote.
func newDocWorld(t *testing.T) (*humanWorld, *GitHub, *gt.API) {
	t.Helper()
	api := gt.NewAPI(t)
	git := gt.NewGitServer(t, api)
	api.World.HeadOf = git.RemoteSHA
	seed := t.TempDir()
	git.Seed(t, seed)
	gt.Git(t, seed, "checkout", "-q", "main")
	gt.Commit(t, seed, "config", map[string]string{config.ProjectConfigPath: docConfig})
	gt.Git(t, seed, "push", "-q", git.RemoteDir(), "main")
	repo := filepath.Join(t.TempDir(), "alpha")
	gt.Git(t, filepath.Dir(repo), "clone", "-q", git.RemoteDir(), repo)

	pc, err := config.ParseProjectConfig([]byte(docConfig))
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.ProjectSnapshot{Config: pc, Manifest: &config.Manifest{}}
	w := &humanWorld{t: t, dir: instanceDir(t), bx: fake.New("tk"), clock: &testClock{now: time.Now()},
		host: agentfake.New(filepath.Join(t.TempDir(), "checkouts"), agentfake.WithQuietPeriod(time.Minute))}
	if err := w.bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveHost(filepath.Join(w.dir, config.HostConfigFile), &config.HostConfig{
		StateDir: w.dir, Throughput: config.DefaultThroughput(),
		Credentials: config.Credentials{GitHubApp: config.GitHubAppCredentials{AppID: gt.AppID, PrivateKeySecret: "GITHUB_APP_KEY"}},
	}); err != nil {
		t.Fatal(err)
	}
	gh := &GitHub{API: api.URL(), GitURL: git.URL, Protocols: []string{"http"},
		Sleep: func(context.Context, time.Duration) error { return nil }}
	w.opts = Options{
		Beads:  func(string) (beadsx.Client, error) { return w.bx, nil },
		Agents: w.host,
		Sched: SchedOptions{
			Checkouts:     func(string) sched.CheckoutHost { return worktrees{t: t, host: w.host, repo: repo} },
			ProjectConfig: func(context.Context, string) (*config.ProjectSnapshot, error) { return snap, nil },
			Coordinator:   &sched.CoordinatorConfig{Project: "alpha", Harness: agenthost.Claude},
			TickInterval:  time.Hour,
		},
		Human:     HumanOptions{Now: w.clock.Now, PollInterval: 20 * time.Millisecond, WatchInterval: time.Hour},
		Setup:     []func(*Server) error{gh.Setup, (&Pipeline{GitHub: gh}).Setup},
		Reconcile: []Step{gh.Reconcile},
	}
	w.start()
	ctx := context.Background()
	if err := w.srv.Secrets.Set(ctx, config.HostScope, "GITHUB_APP_KEY", bytes.NewReader(gt.KeyPEM())); err != nil {
		t.Fatal(err)
	}
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: repo}); err != nil {
		t.Fatal(err)
	}
	w.coord = t.TempDir()
	if _, err := w.srv.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: "coord-1", Kind: cli.PrincipalCoordinator,
		Checkout: w.coord}); err != nil {
		t.Fatal(err)
	}
	return w, gh, api
}

// docReview starts the doc-reviewer's run on the epic, submits verdict
// from the run's checkout and finishes the run.
func (w *humanWorld) docReview(epic, verdict string) {
	w.t.Helper()
	ctx := context.Background()
	st, ref := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageDocReviewer)
	if ref != nil || st == nil || st.Item != epic {
		w.t.Fatalf("doc-reviewer dispatch = %+v %+v", st, ref)
	}
	pr, code := w.as(st.Checkout, "pr", "show")
	if code != 0 {
		w.t.Fatalf("pr show = %d %v", code, pr)
	}
	head := pr["data"].(map[string]any)["head_sha"].(string)
	if env, code := w.as(st.Checkout, "review", "submit", epic, "--verdict", verdict, "--head", head); code != 0 {
		w.t.Fatalf("review submit = %d %v", code, env)
	}
	if _, ref := w.srv.Sched.Finish(ctx, st.RunID, "done"); ref != nil {
		w.t.Fatalf("finish: %+v", ref)
	}
}

// An intake room submits its PRD through the CLI: one epic in prd_review
// with one PR, also on a repeat. A pass puts a sign-off entry in the inbox
// that links the PR. AE7 over the scheduler: the first reject starts one
// amender run, and the second waits in the inbox for an intake room.
func TestIntakeRoomSubmitsAPRDThroughTheServer(t *testing.T) {
	w, _, api := newDocWorld(t)
	ctx := context.Background()
	r := w.openRoom("intake", "", "the operator wants widgets")
	co := r["checkout"].(string)
	gt.Commit(t, co, "prd", map[string]string{"docs/prd/widgets.md": "# Widgets\n\n## US-01 See widgets\n"})

	env, code := w.as(co, "prd", "submit", "--path", "docs/prd/widgets.md", "--title", "Widgets")
	if code != 0 {
		t.Fatalf("prd submit = %d %v", code, env)
	}
	epic := env["data"].(map[string]any)["epic"].(string)
	it := w.show(epic)
	if it.Status != machine.PRDReview || len(api.World.PRs()) != 1 || it.Fact(machine.FactDocPR) != "1" {
		t.Fatalf("after submit: %+v, %d PRs", it, len(api.World.PRs()))
	}
	if env, code := w.as(co, "prd", "submit", "--path", "docs/prd/widgets.md", "--title", "Widgets"); code != 0 || env["data"].(map[string]any)["epic"] != epic {
		t.Fatalf("a repeated submit = %d %v", code, env)
	}
	if n := len(api.World.PRs()); n != 1 || w.count(`SELECT count(*) FROM transitions WHERE verb = 'prd_submit'`) != 1 {
		t.Fatalf("a repeated submit wrote again: %d PRs", n)
	}
	// A run cannot submit a PRD, and its PRD-less ticket is PRD_REQUIRED.
	runCo := w.runToken("run-x", "", machine.StageImplementer)
	if env, _ := w.as(runCo, "work", "create", "--title", "Fix it"); env["code"] != cli.CodePRDRequired {
		t.Fatalf("a run's PRD-less ticket = %v", env)
	}
	if env, code := w.coordinator("work", "create", "--project", "alpha", "--title", "Fix it", "--request", "Ian: fix the typo"); code != 0 {
		t.Fatalf("the coordinator's requested ticket = %d %v", code, env)
	}

	// AE7.
	w.docReview(epic, "reject")
	st, ref := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageAmender)
	if ref != nil || st == nil || st.Item != epic {
		t.Fatalf("the first reject started no amender run: %+v %+v", st, ref)
	}
	if again, _ := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageAmender); again != nil {
		t.Fatalf("a second amender run started: %+v", again)
	}
	if _, ref := w.srv.Sched.Finish(ctx, st.RunID, "done"); ref != nil {
		t.Fatalf("finish: %+v", ref)
	}
	if res := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: epic, Verb: "prd_amended",
		Actor: machine.Server("test")}); res.Refusal != nil {
		t.Fatalf("prd_amended: %+v", *res.Refusal)
	}
	w.docReview(epic, "reject")
	if got := w.show(epic).Status; got != machine.Escalated {
		t.Fatalf("after the second reject: %s", got)
	}
	if st, _ := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageAmender); st != nil {
		t.Fatalf("an amender run started after the second reject: %+v", st)
	}
	e := entryByID(w.entries("person"), "escalation/alpha/"+epic)
	if e == nil || !strings.Contains(e["text"].(string), "intake room") {
		t.Fatalf("the inbox entry = %v", e)
	}
	wantLink := "https://github.com/" + gt.Repo + "/pull/1"
	if e["link"] != wantLink {
		t.Fatalf("the entry links %v, want %s", e["link"], wantLink)
	}

	// The answer returns it to review, and a pass waits for the operator's
	// merge with a link to the PR.
	if env, code := w.coordinator("inbox", "answer", "escalation/alpha/"+epic, "--body", "the room revised it"); code != 0 {
		t.Fatalf("answer = %d %v", code, env)
	}
	w.docReview(epic, "pass")
	e = entryByID(w.entries("person"), "signoff/alpha/"+epic)
	if e == nil || e["link"] != wantLink {
		t.Fatalf("the sign-off entry = %v", e)
	}
	if n, _ := strconv.Atoi(w.show(epic).Fact(machine.FactPRDRejects)); n != 2 {
		t.Fatalf("prd_rejects = %d", n)
	}
}
