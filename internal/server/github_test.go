package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

const ghSecret = "sk-test-0123456789abcdef"

// ghWorld is a server with the GitHub actor over synthetic GitHub, and a
// run's checkout.
type ghWorld struct {
	t        *testing.T
	dir      string
	srv      *Server
	gh       *GitHub
	api      *gt.API
	git      *gt.GitServer
	bx       *fake.Client
	checkout string
	cli      *cli.Registry
}

func newGHWorld(t *testing.T) *ghWorld {
	t.Helper()
	w := &ghWorld{t: t, dir: instanceDir(t), api: gt.NewAPI(t)}
	w.git = gt.NewGitServer(t, w.api)
	w.api.World.HeadOf = w.git.RemoteSHA
	w.checkout = t.TempDir()
	w.git.Seed(t, w.checkout)
	if err := config.SaveHost(filepath.Join(w.dir, config.HostConfigFile), &config.HostConfig{
		StateDir: w.dir, Throughput: config.DefaultThroughput(),
		Credentials: config.Credentials{GitHubApp: config.GitHubAppCredentials{AppID: gt.AppID, PrivateKeySecret: "GITHUB_APP_KEY"}},
	}); err != nil {
		t.Fatal(err)
	}
	w.bx = fake.New("tk")
	if err := w.bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	w.gh = &GitHub{API: w.api.URL(), GitURL: w.git.URL, Protocols: []string{"http"},
		Sleep: func(context.Context, time.Duration) error { return nil }, ProjectConfig: triageConfig(false)}
	opts := Options{
		Beads:     func(string) (beadsx.Client, error) { return w.bx, nil },
		Claims:    allowAll{},
		Setup:     []func(*Server) error{w.gh.Setup, func(s *Server) error { w.srv = s; return nil }},
		Reconcile: []Step{w.gh.Reconcile},
	}
	start(t, w.dir, opts, true)
	ctx := context.Background()
	if err := w.srv.Secrets.Set(ctx, config.HostScope, "GITHUB_APP_KEY", bytes.NewReader(gt.KeyPEM())); err != nil {
		t.Fatal(err)
	}
	// A stored project secret that no outbound text may hold.
	w.srv.Redactor.SetLive("alpha/API_KEY", ghSecret)
	return w
}

// claim registers nothing: it makes a ticket, claims it for run-1 and
// issues the run's token into the checkout.
func (w *ghWorld) claim() string {
	w.t.Helper()
	ctx := context.Background()
	m := w.srv.Machine
	id, res := m.Create(ctx, machine.CreateRequest{Project: "alpha", Kind: machine.KindTicket, Title: "t", Verb: "create",
		Actor: machine.Server("test")})
	if res.Refusal != nil {
		w.t.Fatalf("create: %+v", *res.Refusal)
	}
	lease, res := m.Claim(ctx, machine.ClaimRequest{Project: "alpha", Item: id, Stage: machine.StageImplementer, RunID: "run-1"})
	if res.Refusal != nil {
		w.t.Fatalf("claim: %+v", *res.Refusal)
	}
	if _, err := w.srv.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: "run-1", Kind: cli.PrincipalRun, Project: "alpha",
		Item: id, Stage: machine.StageImplementer, Checkout: w.checkout, Epoch: lease.Epoch}); err != nil {
		w.t.Fatal(err)
	}
	// The server reads the caller's working directory from the socket peer.
	w.t.Chdir(w.checkout)
	w.cli = cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return w.checkout, nil }}).Attach(w.cli)
	return id
}

func (w *ghWorld) run(args ...string) map[string]any {
	w.t.Helper()
	var out bytes.Buffer
	w.cli.Run(context.Background(), args, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		w.t.Fatalf("no envelope: %v\n%s", err, out.String())
	}
	return env
}

// project add creates the mirror and checks the branch rules: a missing
// rule fails the add and names the rule, and nothing registers.
func TestProjectAddChecksTheBranchRules(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	p := config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}
	w.api.SetRules([]any{map[string]any{"type": "required_status_checks", "parameters": map[string]any{
		"required_status_checks": []any{map[string]any{"context": "ci"}}}}})
	err := w.srv.Projects.Add(ctx, p)
	var re *github.RulesError
	if !errors.As(err, &re) || !strings.Contains(err.Error(), "eligible_required") || !strings.Contains(err.Error(), "direct_push_blocked") {
		t.Fatalf("add = %v", err)
	}
	if ps, _ := w.srv.Projects.List(); len(ps) != 0 {
		t.Fatalf("a failed add registered %v", ps)
	}
	w.api.SetRules(gt.GoodRules())
	if err := w.srv.Projects.Add(ctx, p); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.dir, MirrorDir, "alpha.git", "HEAD")); err != nil {
		t.Fatalf("no bare mirror: %v", err)
	}
}

// A run pushes, opens its PR and hands the ticket to review through the
// CLI, with no GitHub credential of its own. A repeat is a replay, and a
// secret in outbound text is refused.
func TestRunPushesAndOpensItsPRThroughTheServer(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	id := w.claim()
	head := gt.Commit(t, w.checkout, "work", map[string]string{"a.txt": "a\n"})

	env := w.run("branch", "push")
	data, _ := env["data"].(map[string]any)
	if env["ok"] != true || data["pushed"] != true || data["head"] != head {
		t.Fatalf("branch push = %v", env)
	}
	if w.git.RemoteSHA(github.BranchFor(id)) != head {
		t.Fatal("the head did not reach the remote")
	}
	if env := w.run("branch", "push"); env["ok"] != true || env["replayed"] != true {
		t.Fatalf("a repeated push = %v, want a replay", env)
	}

	env = w.run("pr", "open", "--title", "Add a", "--body", "Adds a.")
	data, _ = env["data"].(map[string]any)
	if env["ok"] != true || data["created"] != true {
		t.Fatalf("pr open = %v", env)
	}
	it, _ := w.srv.Machine.Show(ctx, "alpha", id)
	if it.Status != machine.InSimplify || it.Fact(machine.FactHead) != head || it.Fact(machine.FactSimplifyFrom) != head {
		t.Fatalf("after pr open: %s %v", it.Status, it.Facts)
	}
	if n := len(w.api.World.PRs()); n != 1 {
		t.Fatalf("%d PRs", n)
	}

	env = w.run("pr", "reply", "--body", "the key is "+ghSecret)
	if env["code"] != cli.CodeSecretInOutput || env["exit"] != float64(3) {
		t.Fatalf("pr reply with a secret = %v", env)
	}
	if strings.Contains(strings.Join(w.api.Calls(), "\n"), "/comments") && len(w.api.World.Comments(1)) != 0 {
		t.Fatal("the secret reached GitHub")
	}
	// The implementer reads a conflict here. GitHub's PR list carries no
	// mergeable state, so pr show reads the PR by number.
	w.api.World.Update(w.api.World.PR(1), func(p *gt.PR) { p.MergeableState = "dirty" })
	env = w.run("pr", "show")
	if data, _ := env["data"].(map[string]any); env["ok"] != true || data["head_sha"] != head || data["mergeable_state"] != "dirty" {
		t.Fatalf("pr show = %v", env)
	}
}

// A run that lost its lease pushes nothing.
func TestStaleRunCannotPush(t *testing.T) {
	w := newGHWorld(t)
	ctx := context.Background()
	if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	id := w.claim()
	gt.Commit(t, w.checkout, "work", map[string]string{"a.txt": "a\n"})
	// The lease moves on: a reclaim bumps the epoch.
	if res := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: id, Verb: "reclaim",
		Actor: machine.Server("test")}); res.Refusal != nil {
		t.Fatalf("reclaim: %+v", *res.Refusal)
	}
	env := w.run("branch", "push")
	if env["code"] != cli.CodeLeaseNotHeld {
		t.Fatalf("a stale run's push = %v", env)
	}
	if w.git.RemoteSHA(github.BranchFor(id)) != "" {
		t.Fatal("the stale run's push landed")
	}
}

// neonPinConfig is a project config that pins the userspace skill neon.
const neonPinConfig = `merge: {mechanism: direct}
skills:
  - name: neon
    repo: owner/repo
    commit: 0123456789abcdef0123456789abcdef01234567
    path: ""
    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`

// TestC16PushReadsThePinsOfTheLastGoodConfig proves C16 through the
// server: the push service reads the pin names from the project's last
// good config, so a run's push under .claude/skills/neon/ is refused. A
// project with no last good config passes it.
func TestC16PushReadsThePinsOfTheLastGoodConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		lkg     string
		refused bool
	}{
		{"pinned", neonPinConfig, true},
		{"no last good config", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newGHWorld(t)
			ctx := context.Background()
			if err := w.srv.Projects.Add(ctx, config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
				t.Fatal(err)
			}
			if tc.lkg != "" {
				if err := config.StoreLKG(w.srv.Store).SaveLKG(ctx, "alpha", []byte(tc.lkg), nil); err != nil {
					t.Fatal(err)
				}
			}
			id := w.claim()
			head := gt.Commit(t, w.checkout, "add neon", map[string]string{".claude/skills/neon/SKILL.md": "x\n"})
			env := w.run("branch", "push")
			if !tc.refused {
				if env["ok"] != true || w.git.RemoteSHA(github.BranchFor(id)) != head {
					t.Fatalf("branch push = %v", env)
				}
				return
			}
			if env["code"] != cli.CodeSecretInDiff || env["rule"] != "injected_path" {
				t.Fatalf("branch push = %v, want SECRET_IN_DIFF injected_path", env)
			}
			if w.git.RemoteSHA(github.BranchFor(id)) != "" {
				t.Fatal("the pinned skill reached the remote")
			}
		})
	}
}
