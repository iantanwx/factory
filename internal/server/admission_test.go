package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// runCall starts a server with opts and calls args as a run of project
// alpha from inside the run's checkout. Unless opts names a project config
// source, alpha has no committed config yet.
func runCall(t *testing.T, opts Options, args ...string) (map[string]any, *Server) {
	t.Helper()
	dir := instanceDir(t)
	if opts.Sched.ProjectConfig == nil {
		opts.Sched.ProjectConfig = func(_ context.Context, project string) (*config.ProjectSnapshot, error) {
			return nil, fmt.Errorf("%s has no config yet: %w", project, config.ErrConfigMissing)
		}
	}
	var srv *Server
	opts.Setup = []func(*Server) error{func(s *Server) error { srv = s; return nil }}
	start(t, dir, opts, true)
	checkout := t.TempDir()
	if _, err := srv.Tokens.IssueToCheckout(context.Background(), rpc.IssueRequest{
		RunID: "run-1", Kind: cli.PrincipalRun, Project: "alpha", Item: "tk-none", Checkout: checkout,
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(checkout)
	r := cli.Surface()
	(&cli.Client{Instance: dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return checkout, nil }}).Attach(r)
	var out bytes.Buffer
	r.Run(context.Background(), args, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("no envelope: %v\n%s", err, out.String())
	}
	return env, srv
}

func todoTicket(t *testing.T, bx *fake.Client) string {
	t.Helper()
	ctx := context.Background()
	if err := bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	is, err := bx.Create(ctx, beadsx.CreateSpec{Title: "item", Description: "no paths", Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.Todo}); err != nil {
		t.Fatal(err)
	}
	return is.ID
}

// With beads and no other guard, the server composes admission: work peek
// answers the stage's work query, and a claim that skips admission is
// refused.
func TestServerComposesAdmission(t *testing.T) {
	bx := fake.New("tk")
	id := todoTicket(t, bx)
	env, srv := runCall(t, Options{Beads: func(string) (beadsx.Client, error) { return bx, nil }},
		"work", "peek", "--stage", "implementer")
	data, _ := env["data"].(map[string]any)
	if env["exit"] != float64(0) || data["item"] != id {
		t.Fatalf("work peek = %v, want %s", env, id)
	}
	if srv.Admission == nil {
		t.Fatal("the server composed no admission")
	}
	_, res := srv.Machine.Claim(context.Background(), machine.ClaimRequest{Project: "alpha", Item: id, Stage: "implementer", RunID: "rogue"})
	if res.Refusal == nil || res.Refusal.Rule != "not_admitted" {
		t.Fatalf("a claim outside admission = %+v", res.Refusal)
	}
	cfg, err := config.LoadHost(srv.HostPath)
	if err != nil || cfg.Throughput["implementer"].Cap != 8 {
		t.Fatalf("the first start wrote %+v, %v; want the default throughput", cfg, err)
	}
}

// The server gives admission the projects' committed config: a config that
// does not read refuses the peek, because admission cannot know the
// project's caps.
func TestServerAdmissionReadsProjectConfig(t *testing.T) {
	bx := fake.New("tk")
	todoTicket(t, bx)
	opts := Options{Beads: func(string) (beadsx.Client, error) { return bx, nil }}
	opts.Sched.ProjectConfig = func(context.Context, string) (*config.ProjectSnapshot, error) {
		return nil, errors.New("the mirror does not read")
	}
	env, _ := runCall(t, opts, "work", "peek", "--stage", "implementer")
	hint, _ := env["hint"].(string)
	if env["code"] != cli.CodeInternal || !strings.Contains(hint, "the config of alpha") {
		t.Fatalf("work peek with no readable project config = %v, want INTERNAL", env)
	}
}

func TestWorkPeekUnknownStageAndNothing(t *testing.T) {
	bx := fake.New("tk")
	if err := bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	opts := Options{Beads: func(string) (beadsx.Client, error) { return bx, nil }}
	env, _ := runCall(t, opts, "work", "peek", "--stage", "implementer")
	if env["exit"] != float64(1) {
		t.Fatalf("work peek on an empty queue = %v, want exit 1", env)
	}
	env, _ = runCall(t, opts, "work", "peek", "--stage", "nonsense")
	if env["code"] != cli.CodeUsage || env["rule"] != "unknown_stage" {
		t.Fatalf("work peek of an unknown stage = %v", env)
	}
}

// A guard the caller names replaces admission, and work peek stays
// unimplemented.
func TestExplicitClaimGuardReplacesAdmission(t *testing.T) {
	bx := fake.New("tk")
	todoTicket(t, bx)
	env, srv := runCall(t, Options{Beads: func(string) (beadsx.Client, error) { return bx, nil }, Claims: allowAll{}},
		"work", "peek", "--stage", "implementer")
	if env["code"] != cli.CodeNotImplemented || srv.Admission != nil {
		t.Fatalf("work peek with another guard = %v", env)
	}
}

type allowAll struct{}

func (allowAll) AdmitClaim(context.Context, machine.ClaimCheck) *cli.Refusal { return nil }

// GitBase lists the blob id of each file under the prefixes at
// origin/main, and nothing else.
func TestGitBaseReadsDeclaredPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	for path, body := range map[string]string{"app/task.go": "one", "app/other.go": "two", "docs/x.md": "three", "z.txt": "four"} {
		if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", ".")
	git("commit", "-qm", "fixture")
	git("update-ref", "refs/remotes/origin/main", "HEAD")

	hostPath := filepath.Join(t.TempDir(), config.HostConfigFile)
	cfg := config.DefaultHost("/var/lib/loop")
	cfg.Projects = []config.Project{{ID: "alpha", Repo: "iantanwx/alpha", Path: repo}}
	if err := config.SaveHost(hostPath, cfg); err != nil {
		t.Fatal(err)
	}
	files, err := GitBase{Projects: &config.Projects{HostPath: hostPath}}.
		Files(context.Background(), "alpha", []string{"app/task.go", "docs/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files["app/task.go"] == "" || files["docs/x.md"] == "" {
		t.Fatalf("Files = %v", files)
	}
}
