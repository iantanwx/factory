//go:build acceptance

package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/agenthosttest"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/agenthost/orca"
	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/gitx/gittest"
	"github.com/iantanwx/loop/internal/machine"
)

// The real-host world runs over real orca on the lane. Here it runs over
// the fake, so CI proves its wiring before a lane spends a session.

// laneFixture makes a git repository with one commit on main, as the lane
// job's fixture step does. marked writes the lane fixture marker.
func laneFixture(t *testing.T, marked bool) string {
	t.Helper()
	clone := t.TempDir()
	gt.Git(t, clone, "init", "-q", "-b", "main")
	gt.Commit(t, clone, "the lane fixture", map[string]string{"README.md": "The lane fixture.\n"})
	if marked {
		if err := os.WriteFile(filepath.Join(clone, ".git", orca.LaneFixtureMarker), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return clone
}

// worktrees makes each fake checkout a git worktree of the clone's main, as
// orca does from the project's repository.
type worktrees struct {
	fake  *agentfake.Host
	clone string

	mu      sync.Mutex
	created int
}

func (c *worktrees) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	c.mu.Lock()
	c.created++
	c.mu.Unlock()
	co, err := c.fake.CreateCheckout(ctx, spec)
	if err != nil {
		return co, err
	}
	out, err := gittest.Command("-C", c.clone, "worktree", "add", "-q", "-b", spec.Name, co.Path, "main").CombinedOutput()
	if err != nil {
		return co, fmt.Errorf("git worktree add: %w: %s", err, out)
	}
	return co, nil
}

func (c *worktrees) RemoveCheckout(ctx context.Context, path string) error {
	if err := c.fake.RemoveCheckout(ctx, path); err != nil {
		return err
	}
	if out, err := gittest.Command("-C", c.clone, "worktree", "remove", "--force", path).CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove: %w: %s", err, out)
	}
	return nil
}

// realOver is a RealHost over a new fake host and the clone.
func realOver(t *testing.T, clone string, declared int) (*acceptance.RealHost, *agentfake.Host, *worktrees) {
	host := agentfake.New(t.TempDir(), agentfake.WithQuietPeriod(time.Hour))
	co := &worktrees{fake: host, clone: clone}
	return &acceptance.RealHost{Agents: host, Checkouts: co, Clone: clone, Declared: declared}, host, co
}

// runChild runs this test again in a process of its own with env, because
// the child must fail its test. It reuses the loop binary this process
// runs, and returns the child's output and its error.
func runChild(t *testing.T, env ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), append(env, acceptance.LoopBinaryEnv+"="+acceptance.Binary)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(gt.Git(t, dir, args...))
}

// C11: a real-host world runs with no coordinator, on the real clock, and
// counts its starts. Its checkouts start from the synthetic remote's main,
// and each run's token file names the world's instance and its copy of loop.
func TestC11ARealHostWorldRunsWithNoCoordinatorOnTheRealClock(t *testing.T) {
	if os.Getenv("ACC_C11_OVER") != "" {
		startPastDeclared(t)
		return
	}
	clone := laneFixture(t, true)
	rh, host, _ := realOver(t, clone, 1)
	w := newWorld(t, acceptance.Options{Real: rh})
	// The service's tick after a project add starts a coordinator in a
	// world that has one.
	w.Tick()
	if n := len(host.Launches()); n != 0 {
		t.Fatalf("the world launched %d sessions before any dispatch", n)
	}
	id := w.CreateTicket("Add a greeting", "Create `greeting.txt` holding `hello, world`.")
	wantStatus(w, id, machine.Todo)

	main := w.PushMain("main moves on", map[string]string{"NEXT.md": "next\n"}, "")
	before := time.Now().Add(-time.Second).UnixMilli()
	run := w.Dispatch(machine.StageImplementer)
	ls := host.Launches()
	if len(ls) != 1 || ls[0].Spec.Kind != agenthost.KindStage {
		t.Fatalf("launches = %+v, want the implementer's one stage session", ls)
	}
	if got := gitOut(t, run.Checkout, "rev-parse", "HEAD"); got != main {
		t.Fatalf("the checkout starts at %s, want the remote's main %s", got, main)
	}
	if got := gitOut(t, clone, "rev-parse", "main"); got != main {
		t.Fatalf("the clone's main is %s, want the remote's main %s", got, main)
	}

	b, err := os.ReadFile(filepath.Join(run.Checkout, cli.TokenPath))
	if err != nil {
		t.Fatal(err)
	}
	var tf cli.TokenFile
	if err := json.Unmarshal(b, &tf); err != nil {
		t.Fatal(err)
	}
	if tf.Instance != w.Dir || !strings.HasPrefix(tf.Binary, w.Dir+string(filepath.Separator)) {
		t.Fatalf("the token file names instance %q and binary %q, want %q and a loop under it", tf.Instance, tf.Binary, w.Dir)
	}
	if err := acceptance.CheckLoop(tf.Binary); err != nil {
		t.Fatalf("the token file's binary: %v", err)
	}
	w.Must(w.In(run.Checkout, "work", "show", id))
	after := time.Now().Add(time.Second).UnixMilli()
	if n := w.Count(`SELECT count(*) FROM runs WHERE run_id = ? AND last_cli_call_at BETWEEN ? AND ?`, run.RunID, before, after); n != 1 {
		t.Fatalf("the scheduler did not record the run's call on the wall clock")
	}
	if n := len(host.Launches()); n != 1 {
		t.Fatalf("the world launched %d sessions, want 1", n)
	}

	out, err := runChild(t, "ACC_C11_OVER=1", "ACC_CLONE="+laneFixture(t, true))
	if err == nil || !strings.Contains(out, agenthosttest.ErrOverDeclared.Error()) || !strings.Contains(out, "over=true launches=1") {
		t.Fatalf("a start past the declared one: %v\n%s", err, out)
	}
}

// startPastDeclared is C11's child: with one session declared, a second
// Start returns ErrOverDeclared, which fails this child's test, and the
// fake records one launch.
func startPastDeclared(t *testing.T) {
	rh, host, _ := realOver(t, os.Getenv("ACC_CLONE"), 1)
	w := newWorld(t, acceptance.Options{Real: rh})
	w.CreateTicket("Add a greeting", "")
	w.Dispatch(machine.StageImplementer)
	_, err := w.Agents.Start(context.Background(), host.Launches()[0].Spec)
	fmt.Printf("over=%t launches=%d\n", errors.Is(err, agenthosttest.ErrOverDeclared), len(host.Launches()))
}

// C26: a real world never rewrites a repository that is not a lane fixture.
func TestC26ARealWorldNeverRewritesARepositoryThatIsNotALaneFixture(t *testing.T) {
	if clone := os.Getenv("ACC_C26_CLONE"); clone != "" {
		rh, host, co := realOver(t, clone, 1)
		t.Cleanup(func() { fmt.Printf("launches=%d checkouts=%d\n", len(host.Launches()), co.created) })
		newWorld(t, acceptance.Options{Real: rh})
		return
	}
	clone := laneFixture(t, false)
	gt.Git(t, clone, "remote", "add", "origin", "https://example.invalid/fixture.git")
	read := func() [3]string {
		return [3]string{gitOut(t, clone, "rev-parse", "main"), gitOut(t, clone, "remote", "get-url", "origin"),
			gitOut(t, clone, "worktree", "list", "--porcelain")}
	}
	was := read()
	out, err := runChild(t, "ACC_C26_CLONE="+clone)
	line := "acceptance: " + clone + " is no lane fixture: it has no .git/" + orca.LaneFixtureMarker
	if err == nil || !strings.Contains(out, line) || !strings.Contains(out, "launches=0 checkouts=0") {
		t.Fatalf("a world over an unmarked clone: %v; want a failure that names %q and no launch or checkout\n%s", err, line, out)
	}
	if now := read(); now != was {
		t.Fatalf("the unmarked clone changed: %q, was %q", now, was)
	}
}
