package github

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

const (
	testRepo     = gt.Repo
	testProject  = "alpha"
	testAppID    = gt.AppID
	storedSecret = "sk-test-0123456789abcdef"
)

var (
	gitT      = gt.Git
	commit    = gt.Commit
	writeFile = gt.WriteFile
	writeJSON = gt.WriteJSON
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

type storeTx = store.Tx

// env is one GitHub world: the fake API, the git server with its bare
// remote, a checkout cloned from it, a store, a service and, after
// withWorld, a machine over fake beads.
type env struct {
	t        *testing.T
	api      *gt.API
	git      *gt.GitServer
	st       *store.Store
	red      *secrets.Redactor
	root     string // mirror root
	checkout string
	base     string // the sha of main
	svc      *Service
	project  config.Project
	bx       *fake.Client
	m        *machine.Machine
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, api: gt.NewAPI(t)}
	e.git = gt.NewGitServer(t, e.api)
	e.checkout = t.TempDir()
	e.base = e.git.Seed(t, e.checkout)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st = st
	if err := st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES (?, ?, 0, 0)`, testProject, testRepo)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	e.red = secrets.NewRedactor()
	e.red.SetStored([]secrets.NamedValue{{Name: testProject + "/API_KEY", Value: storedSecret}})
	e.root = t.TempDir()
	e.project = config.Project{ID: testProject, Repo: testRepo, Path: e.checkout}
	e.svc = e.service()
	return e
}

func (e *env) service() *Service {
	e.t.Helper()
	svc, err := New(Config{
		Apps: OneApp(testAppID, func(context.Context) ([]byte, error) { return gt.KeyPEM(), nil }),
		API:  e.api.URL(), GitURL: e.git.URL, Protocols: []string{"http"}, MirrorRoot: e.root,
		Store: e.st, Redactor: e.red, Projects: staticProjects{e.project}, Machine: e.m,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return svc
}

// restart replaces the service, as a new server process over the same
// store and mirrors.
func (e *env) restart() { e.svc = e.service() }

func (e *env) push(item, head string) (PushResult, error) {
	return e.svc.Push(context.Background(), PushRequest{Project: testProject, Item: item, Branch: BranchFor(item),
		Checkout: e.checkout, Head: head, RunID: "run-1"})
}

// pushAs pushes as a caller of one principal kind, such as cli.PrincipalRun.
func (e *env) pushAs(kind, item, head string) (PushResult, error) {
	return e.svc.Push(context.Background(), PushRequest{Project: testProject, Item: item, Branch: BranchFor(item),
		Checkout: e.checkout, Head: head, RunID: "run-1", Caller: kind})
}

func (e *env) remoteSHA(branch string) string { return e.git.RemoteSHA(branch) }

// withWorld attaches a machine over fake beads, and returns the API's PR
// world.
func (e *env) withWorld() *gt.World {
	e.t.Helper()
	bx := fake.New("tk")
	if err := bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		e.t.Fatal(err)
	}
	e.bx = bx
	e.m = e.newMachine()
	e.restart()
	return e.api.World
}

func (e *env) newMachine() *machine.Machine {
	e.t.Helper()
	m, err := machine.New(machine.Config{Store: e.st, Beads: func(string) (beadsx.Client, error) { return e.bx, nil },
		Redactor: e.red, Registry: cli.Surface(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func mustOK(t *testing.T, res cli.Result) {
	t.Helper()
	if res.Refusal != nil {
		t.Fatalf("refused: %+v", *res.Refusal)
	}
}

// ticket creates a ticket and drives it to status with head as its PR
// head. ready_to_merge passes review at head.
func (e *env) ticket(status, head string) string {
	e.t.Helper()
	ctx := context.Background()
	id, res := e.m.Create(ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindTicket, Title: "t", Verb: "create",
		Actor: machine.Server("test")})
	mustOK(e.t, res)
	if status == machine.Todo {
		return id
	}
	_, res = e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageImplementer, RunID: "run-1"})
	mustOK(e.t, res)
	if status == machine.InProgress {
		return id
	}
	apply := func(verb string) {
		mustOK(e.t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: verb,
			Params: map[string]string{"head": head}, Actor: machine.Server("test")}))
	}
	apply("pr_open")
	switch status {
	case machine.ReadyToMerge:
		apply("review_pass")
	case machine.Repair:
		apply("ci_red")
	}
	return id
}

func (e *env) status(item string) machine.Item {
	e.t.Helper()
	it, ref := e.m.Show(context.Background(), testProject, item)
	if ref != nil {
		e.t.Fatalf("show %s: %+v", item, *ref)
	}
	return it
}

func (e *env) alerts(kind string) int {
	e.t.Helper()
	var n int
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		return tx.QueryRow(`SELECT count(*) FROM alerts WHERE kind = ?`, kind).Scan(&n)
	}); err != nil {
		e.t.Fatal(err)
	}
	return n
}
