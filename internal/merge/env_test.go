package merge_test

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	bxfake "github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/gitx/gittest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

const testProject = "alpha"

type staticProjects []config.Project

func (s staticProjects) List() ([]config.Project, error) { return s, nil }
func (s staticProjects) Show(id string) (config.Project, error) {
	for _, p := range s {
		if p.ID == id {
			return p, nil
		}
	}
	return config.Project{}, config.ErrProjectNotFound
}

// env is one world: the fake GitHub API and git server, a store, a
// machine over fake beads, the GitHub service and reactor, one mechanism
// and the hand-off job over it.
type env struct {
	t       *testing.T
	ctx     context.Context
	api     *gt.API
	git     *gt.GitServer
	world   *gt.World
	st      *store.Store
	m       *machine.Machine
	svc     *github.Service
	reactor *github.Reactor
	project config.Project
	mech    merge.Mechanism
	job     *merge.HandOff
	// source answers the project's config; a test may replace it.
	source func(ctx context.Context, p config.Project) (*config.ProjectSnapshot, error)
}

func sha(c byte) string { return strings.Repeat(string(c), 40) }

func newEnv(t *testing.T, mk func(e *env) merge.Mechanism, opts ...func(*github.Config)) *env {
	t.Helper()
	ctx := context.Background()
	e := &env{t: t, ctx: ctx, api: gt.NewAPI(t)}
	e.world = e.api.World
	e.git = gt.NewGitServer(t, e.api)
	checkout := t.TempDir()
	e.git.Seed(t, checkout)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st = st
	e.project = config.Project{ID: testProject, Repo: gt.Repo, Path: checkout}
	if err := config.StoreProjectRow(st)(ctx, e.project); err != nil {
		t.Fatal(err)
	}
	bx := bxfake.New("tk")
	if err := bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	red := secrets.NewRedactor()
	e.m, err = machine.New(machine.Config{Store: st, Beads: func(string) (beadsx.Client, error) { return bx, nil },
		Redactor: red, Registry: cli.Surface(), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	gc := github.Config{
		Apps: github.OneApp(gt.AppID, func(context.Context) ([]byte, error) { return gt.KeyPEM(), nil }),
		API:  e.api.URL(), GitURL: e.git.URL, Protocols: []string{"http"}, MirrorRoot: t.TempDir(), GitConfig: gittest.Config,
		Store: st, Redactor: red, Projects: staticProjects{e.project}, Machine: e.m,
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	for _, o := range opts {
		o(&gc)
	}
	e.svc, err = github.New(gc)
	if err != nil {
		t.Fatal(err)
	}
	e.reactor = github.NewReactor(e.svc)
	e.mech = mk(e)
	e.source = func(context.Context, config.Project) (*config.ProjectSnapshot, error) {
		return &config.ProjectSnapshot{Config: &config.ProjectConfig{Merge: config.MergeConfig{Mechanism: e.mech.Name()}}}, nil
	}
	e.job, err = merge.New(merge.Config{GitHub: e.svc, Machine: e.m, Store: st, Projects: staticProjects{e.project},
		Source:     func(ctx context.Context, p config.Project) (*config.ProjectSnapshot, error) { return e.source(ctx, p) },
		Mechanisms: map[config.MergeMechanism]merge.Mechanism{e.mech.Name(): e.mech}, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) mustOK(res cli.Result) {
	e.t.Helper()
	if res.Refusal != nil {
		e.t.Fatalf("refused: %+v", *res.Refusal)
	}
}

func (e *env) apply(item, verb string, params map[string]string) {
	e.t.Helper()
	e.mustOK(e.m.Apply(e.ctx, machine.Request{Project: testProject, Item: item, Verb: verb, Params: params,
		Actor: machine.Server("test")}))
}

// reviewed makes a ticket with an open PR at head that passed review, and
// returns it with its PR. It posts loop/eligible on head when eligible.
func (e *env) reviewed(head string, eligible bool) (string, *gt.PR) {
	e.t.Helper()
	id, pr := e.passed(head, map[string]string{"head": head})
	if eligible {
		e.postEligibility()
	}
	return id, pr
}

// passed makes a ticket with an open PR at head and applies review_pass
// with params at green CI.
func (e *env) passed(head string, params map[string]string) (string, *gt.PR) {
	e.t.Helper()
	id, res := e.m.Create(e.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindTicket, Title: "t",
		Verb: "create", Actor: machine.Server("test")})
	e.mustOK(res)
	_, res = e.m.Claim(e.ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageImplementer, RunID: "run-" + id})
	e.mustOK(res)
	e.apply(id, "pr_open", map[string]string{"head": head})
	e.apply(id, machine.VerbSimplifyOff, nil) // the project runs no simplify stage
	pr := e.world.Add(&gt.PR{Head: github.BranchFor(id), HeadSHA: head, Title: id})
	// The review passed at green CI: the ruleset's ci check reported.
	e.world.SetRuns(head, gt.CheckRun("ci", head, "completed", "success", time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)))
	e.apply(id, "review_pass", params)
	return id, pr
}

// postEligibility runs the reactor's eligibility step of two ticks: it
// posts loop/qa, then loop/eligible, on every reviewed head.
func (e *env) postEligibility() {
	e.t.Helper()
	for range 2 {
		if _, err := e.svc.EnqueueEligibility(e.ctx, e.project); err != nil {
			e.t.Fatal(err)
		}
		if err := errors.Join(e.svc.RunCheckRunEffects(e.ctx), e.svc.RunStatusEffects(e.ctx)); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) tick() {
	e.t.Helper()
	if err := e.job.TickProject(e.ctx, testProject); err != nil {
		e.t.Fatalf("hand-off tick: %v", err)
	}
}

func (e *env) reactorTick() {
	e.t.Helper()
	if err := e.reactor.Tick(e.ctx); err != nil {
		e.t.Fatalf("reactor tick: %v", err)
	}
}

func (e *env) item(id string) machine.Item {
	e.t.Helper()
	it, ref := e.m.Show(e.ctx, testProject, id)
	if ref != nil {
		e.t.Fatalf("show %s: %+v", id, *ref)
	}
	return it
}

func (e *env) wantStatus(id, status string) machine.Item {
	e.t.Helper()
	it := e.item(id)
	if it.Status != status {
		e.t.Fatalf("%s is %s; want %s", id, it.Status, status)
	}
	return it
}

// transitions counts the applied moves of verb on item.
func (e *env) transitions(item, verb string) int {
	e.t.Helper()
	var n int
	err := e.st.Read(e.ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(e.ctx, `SELECT count(*) FROM transitions WHERE project = ? AND item = ? AND verb = ?`,
			testProject, item, verb).Scan(&n)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

// mergeCalls lists the API calls a hand-off makes: labels, comments,
// GraphQL and merges.
func (e *env) mergeCalls() []string {
	var out []string
	for _, c := range e.api.Calls() {
		if strings.HasPrefix(c, "GET ") {
			continue
		}
		if strings.Contains(c, "/labels") || strings.Contains(c, "/graphql") || strings.Contains(c, "/merge") ||
			strings.Contains(c, "/comments") {
			out = append(out, c)
		}
	}
	return out
}

// wantTracked checks the PR's row in `merge status --all`.
func (e *env) wantTracked(pr *gt.PR, state string, reason merge.Reason, attempts int) merge.Tracked {
	e.t.Helper()
	rows, err := merge.Status(e.ctx, e.st, testProject, true)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range rows {
		if r.PR != pr.Number {
			continue
		}
		if r.State != state || r.Reason != string(reason) || r.Attempts != attempts {
			e.t.Fatalf("merge status of #%d = %+v; want %s, reason %q, %d attempts", pr.Number, r, state, reason, attempts)
		}
		return r
	}
	e.t.Fatalf("merge status holds no #%d: %+v", pr.Number, rows)
	return merge.Tracked{}
}

// baseConfig commits .loop/config.yaml to main on the remote and syncs
// the project's mirror, as the reactor's tick does.
func (e *env) baseConfig(doc string) {
	e.t.Helper()
	dir := e.project.Path
	gt.Git(e.t, dir, "checkout", "-q", "main")
	gt.Commit(e.t, dir, "config", map[string]string{config.ProjectConfigPath: doc})
	gt.Git(e.t, dir, "push", "-q", e.git.RemoteDir(), "main")
	gt.Git(e.t, dir, "checkout", "-q", "work")
	if _, _, err := e.svc.SyncBase(e.ctx, e.project); err != nil {
		e.t.Fatal(err)
	}
}

func failing(head string) map[string]any {
	return gt.CheckRun("ci", head, "completed", "failure", time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC))
}

func has(list []string, s string) bool { return slices.Contains(list, s) }
