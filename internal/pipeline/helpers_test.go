package pipeline_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/beadsx"
	bxfake "github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pipeline"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

const (
	testProject = "alpha"
	prdPath     = "docs/prd/widgets.md"
	prdText     = "# Widgets\n\n## US-01 A trader sees widgets\n\n## US-02 A trader sorts widgets\n"
	roomID      = "room-1"
	roomRun     = "room-1-s1"
)

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

type hostSource struct{}

func (hostSource) Load(context.Context) (*config.HostConfig, error) {
	return config.DefaultHost("/nonexistent"), nil
}

// world is one project over synthetic GitHub: a store, a machine over fake
// beads, the GitHub service and reactor, admission's work query, and the
// pipeline. checkout is the intake room's checkout and every run's.
type world struct {
	t        *testing.T
	ctx      context.Context
	api      *gt.API
	git      *gt.GitServer
	checkout string
	st       *store.Store
	bx       *bxfake.Client
	m        *machine.Machine
	svc      *github.Service
	reactor  *github.Reactor
	adm      *admission.Admission
	p        *pipeline.Pipeline
	reg      *cli.Registry
	projects staticProjects
	red      *secrets.Redactor
	n        int
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	w := &world{t: t, ctx: ctx, api: gt.NewAPI(t), reg: cli.Surface(), red: secrets.NewRedactor()}
	w.git = gt.NewGitServer(t, w.api)
	w.api.World.HeadOf = w.git.RemoteSHA
	w.checkout = t.TempDir()
	w.git.Seed(t, w.checkout)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w.st = st
	p := config.Project{ID: testProject, Repo: gt.Repo, Path: w.checkout}
	w.projects = staticProjects{p}
	if err := config.StoreProjectRow(st)(ctx, p); err != nil {
		t.Fatal(err)
	}
	w.bx = bxfake.New("tk")
	if err := w.bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	beads := func(string) (beadsx.Client, error) { return w.bx, nil }
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	w.m, err = machine.New(machine.Config{Store: st, Beads: beads, Redactor: w.red, Registry: w.reg, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	w.svc, err = github.New(github.Config{
		Apps: github.OneApp(gt.AppID, func(context.Context) ([]byte, error) { return gt.KeyPEM(), nil }),
		API:  w.api.URL(), GitURL: w.git.URL, Protocols: []string{"http"}, MirrorRoot: t.TempDir(),
		Store: st, Redactor: w.red, Projects: w.projects, Machine: w.m, Logger: quiet,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	w.reactor = github.NewReactor(w.svc)
	w.adm, err = admission.New(admission.Config{Store: st, Host: hostSource{}, Beads: beads, Base: w.svc, Release: "test"})
	if err != nil {
		t.Fatal(err)
	}
	w.adm.Attach(w.m)
	w.p, err = pipeline.New(pipeline.Config{Store: st, Machine: w.m, Beads: beads, GitHub: w.svc, Projects: w.projects,
		Redactor: w.red, Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO rooms (id, project, item, state, opened_at, kind, reason, run_id, updated_at)
			VALUES (?, ?, '', 'live', 1, 'intake', 'new work', ?, 1)`, roomID, testProject, roomRun)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return w
}

// intakeRoom is the token of the intake room's session.
func (w *world) intakeRoom() rpc.Principal {
	return rpc.Principal{Kind: cli.PrincipalRoom, RunID: roomRun, Project: testProject, Stage: "intake-room", Checkout: w.checkout}
}

func (w *world) coordinator() rpc.Principal {
	return rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: "coord-1"}
}

// run is the token of a run that holds item for stage at its lease epoch.
func (w *world) run(id, item, stage string) rpc.Principal {
	w.t.Helper()
	pr := rpc.Principal{Kind: cli.PrincipalRun, RunID: id, Project: testProject, Item: item, Stage: stage, Checkout: w.checkout}
	if item != "" {
		pr.Epoch = w.show(item).Epoch
	}
	return pr
}

// call runs one server verb as pr, the way the RPC server does after it
// validated the call. Every call carries a new request id; key is the
// idempotency key, empty for none.
func (w *world) call(pr rpc.Principal, key, path string, flags map[string]string, args ...string) cli.Result {
	w.t.Helper()
	w.n++
	inv, bad := w.reg.Validate(path, args, flags, "req-"+strconv.Itoa(w.n))
	if bad != nil {
		return *bad
	}
	h, ok := w.p.Handlers()[path]
	if !ok {
		h, ok = w.svc.Handlers()[path]
	}
	if !ok {
		w.t.Fatalf("no handler for %s", path)
	}
	return h(w.ctx, &rpc.Call{Inv: inv, Principal: pr, Request: &loopv1.VerbRequest{Verb: path, IdempotencyKey: key}})
}

// commitPRD commits the PRD in the checkout and returns the head.
func (w *world) commitPRD(text string) string {
	w.t.Helper()
	return gt.Commit(w.t, w.checkout, "prd", map[string]string{prdPath: text})
}

func headFlags(head string, flags map[string]string) map[string]string {
	flags[cli.ExtraHead] = head
	flags[cli.ExtraContentHash] = cli.HeadContentHash(head)
	return flags
}

// submit runs prd submit from the intake room at the checkout's head.
func (w *world) submit(key string) cli.Result {
	w.t.Helper()
	head := gt.Git(w.t, w.checkout, "rev-parse", "HEAD")
	return w.call(w.intakeRoom(), key, "prd submit", headFlags(head, map[string]string{"path": prdPath, "title": "Widgets"}))
}

func ok(t *testing.T, res cli.Result) map[string]any {
	t.Helper()
	if res.Refusal != nil || res.Exit != cli.ExitDone {
		t.Fatalf("want exit 0, got %d %+v", res.Exit, res.Refusal)
	}
	raw, err := json.Marshal(res.Data)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("data %s: %v", raw, err)
	}
	return out
}

func wantCode(t *testing.T, res cli.Result, code string) *cli.Refusal {
	t.Helper()
	if res.Refusal == nil || res.Refusal.Code != code {
		t.Fatalf("want %s, got exit %d %+v", code, res.Exit, res.Refusal)
	}
	return res.Refusal
}

func (w *world) show(id string) machine.Item {
	w.t.Helper()
	it, ref := w.m.Show(w.ctx, testProject, id)
	if ref != nil {
		w.t.Fatalf("show %s: %+v", id, *ref)
	}
	return it
}

func (w *world) items(kind machine.Kind) []machine.Item {
	w.t.Helper()
	all, ref := w.m.List(w.ctx, testProject, machine.ListFilter{Kind: kind})
	if ref != nil {
		w.t.Fatalf("list: %+v", *ref)
	}
	return all
}

func (w *world) mustApply(req machine.Request) {
	w.t.Helper()
	req.Project = testProject
	if res := w.m.Apply(w.ctx, req); res.Refusal != nil {
		w.t.Fatalf("%s on %s: %+v", req.Verb, req.Item, *res.Refusal)
	}
}

// review claims the epic for the doc-reviewer and submits verdict.
func (w *world) review(epic, verdict string) {
	w.t.Helper()
	run := fmt.Sprintf("run-rev-%d", w.n)
	w.n++
	l, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageDocReviewer, RunID: run})
	if res.Refusal != nil {
		w.t.Fatalf("claim for review: %+v", *res.Refusal)
	}
	verb := "review_pass"
	if verdict == "reject" {
		verb = "review_reject"
	}
	w.mustApply(machine.Request{Item: epic, Verb: verb, Command: "review submit", Params: map[string]string{"head": "h1"},
		Actor: machine.Actor{Name: "run:" + run, RunID: run, Fenced: true, Epoch: l.Epoch}})
}

func (w *world) pr(n int) gt.PR {
	w.t.Helper()
	for _, p := range w.api.World.PRs() {
		if p.Number == n {
			return p
		}
	}
	w.t.Fatalf("no PR #%d", n)
	return gt.PR{}
}

// merge is the operator's merge of PR n at the checkout's head: main
// takes the head, and the PR reads merged.
func (w *world) merge(n int) {
	w.t.Helper()
	gt.Git(w.t, w.checkout, "push", "-q", w.git.RemoteDir(), "HEAD:main")
	w.updatePR(n, func(x *gt.PR) { x.State, x.Merged, x.MergeSHA = "closed", true, x.HeadSHA })
}

func (w *world) updatePR(n int, fn func(*gt.PR)) {
	w.t.Helper()
	p := w.api.World.PR(n)
	if p == nil {
		w.t.Fatalf("no PR #%d", n)
	}
	w.api.World.Update(p, fn)
}

func (w *world) tick() {
	w.t.Helper()
	if err := w.reactor.Tick(w.ctx); err != nil {
		w.t.Fatalf("reactor tick: %v", err)
	}
}

func (w *world) inbox() []inbox.Entry {
	w.t.Helper()
	in := inbox.New(inbox.Config{Store: w.st, Machine: w.m, Projects: w.projects.List, Link: pipeline.Link(w.m, w.projects),
		ItemLink: pipeline.ItemLink(w.projects)})
	es, err := in.Entries(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	return es
}

func entry(es []inbox.Entry, id string) *inbox.Entry {
	for i := range es {
		if es[i].ID == id {
			return &es[i]
		}
	}
	return nil
}

func fileFlag(v any) map[string]string {
	raw, _ := json.Marshal(v)
	return map[string]string{"file": "plan.json", cli.ExtraFile: base64.StdEncoding.EncodeToString(raw)}
}

func beadsxUpdate(from, to string) beadsx.UpdateSpec {
	return beadsx.UpdateSpec{IfStatus: from, Status: to}
}
