package server

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// asHost runs the CLI from the operator's shell, outside every checkout.
func (hw *homesWorld) asHost(args ...string) (map[string]any, int) {
	hw.t.Helper()
	if _, err := rpc.EnsureAdminCredential(hw.dir); err != nil {
		hw.t.Fatal(err)
	}
	hw.t.Chdir(hw.t.TempDir())
	env, code, _ := call(hw.t, hw.dir, 5*time.Second, nil, args...)
	return env, code
}

// deleteCheckout ends project's coordinator session, deletes its checkout
// as the operator would, and ticks. It returns the coordinator before.
func (hw *homesWorld) deleteCheckout(project string) sched.CoordinatorState {
	hw.t.Helper()
	cs := hw.srv.Sched.Coordinators()[project]
	hw.host.Exit(cs.Session.ID, 0)
	if err := os.RemoveAll(cs.Checkout); err != nil {
		hw.t.Fatal(err)
	}
	hw.tick()
	return cs
}

// hostEntry is the entry that `loop inbox list` from the host lists for
// id, or nil.
func (hw *homesWorld) hostEntry(id string) map[string]any {
	hw.t.Helper()
	env, code := hw.asHost("inbox", "list")
	if code != 0 {
		hw.t.Fatalf("inbox list from the host = %d %v", code, env)
	}
	for _, e := range env["data"].([]any) {
		if m := e.(map[string]any); m["entry"] == id {
			return m
		}
	}
	return nil
}

// C32 and C34 through the CLI: the operator deletes alpha's checkout, and
// the tick raises the alert. The restart as the host makes the checkout
// again, and alpha's new session runs in it. Beta's session id stays.
func TestC34TheHostRestartsACoordinatorWhoseCheckoutIsMissing(t *testing.T) {
	hw := newHomesWorld(t, homesOpts{})
	b := hw.session("beta")
	a := hw.deleteCheckout("alpha")
	entry := hw.alertEntry(`dedupe_key = ?`, sched.AlertCoordinatorCheckoutMissing+":alpha")
	if hw.hostEntry(entry) == nil {
		t.Fatalf("the host's inbox does not list %s", entry)
	}
	n := len(hw.host.Launches())

	env, code := hw.asHost("coordinator", "restart", "--project", "alpha")
	if code != 0 {
		t.Fatalf("coordinator restart = %d %v", code, env)
	}
	a2 := hw.srv.Sched.Coordinators()["alpha"]
	if a2.Session == nil || a2.Session.ID == a.Session.ID || env["data"].(map[string]any)["run_id"] != a2.RunID {
		t.Fatalf("alpha's coordinator after the restart = %+v, answer %v", a2, env["data"])
	}
	if _, err := os.Stat(a2.Checkout); err != nil {
		t.Fatalf("alpha's new checkout: %v", err)
	}
	ls := hw.host.Launches()
	if len(ls) != n+1 || ls[n].Spec.Checkout != a2.Checkout || ls[n].Spec.Resume {
		t.Fatalf("launches after the restart = %+v", ls[n:])
	}
	if got := hw.session("beta"); got != b {
		t.Fatalf("beta's session = %v, was %v", got, b)
	}
}

// C35: a coordinator's token cannot restart a coordinator, and no session
// changes.
func TestC35NoCoordinatorRunsRestart(t *testing.T) {
	hw := newHomesWorld(t, homesOpts{})
	a := hw.srv.Sched.Coordinators()["alpha"]
	n := len(hw.host.Launches())

	env, code := hw.as(a.Checkout, "coordinator", "restart", "--project", "alpha")
	if code != 3 || env["code"] != cli.CodeCapabilityDenied || env["rule"] != "principal_not_allowed" {
		t.Fatalf("coordinator restart from a coordinator = %d %v", code, env)
	}
	if len(hw.host.Launches()) != n || hw.session("alpha") != a.Session.ID {
		t.Fatal("a refused restart changed a session")
	}
}

// C36: the restart refuses a project that is not registered, and one with
// no config at its default branch. No session starts.
func TestC36RestartNeedsARegisteredProjectWithAConfig(t *testing.T) {
	hw := newHomesWorld(t, homesOpts{})
	n := len(hw.host.Launches())
	for project, rule := range map[string]string{"nope": "unknown_project", "gamma": "no_project_config"} {
		env, code := hw.asHost("coordinator", "restart", "--project", project)
		if env["code"] != cli.CodeNotFound || env["rule"] != rule || !strings.Contains(env["hint"].(string), project) {
			t.Fatalf("coordinator restart --project %s = %d %v, want %s", project, code, env, rule)
		}
	}
	if len(hw.host.Launches()) != n {
		t.Fatal("a refused restart started a session")
	}
}

// C37: the loop project's checkout is gone and it has no session. The
// host's inbox lists the alert, which names the loop project and the
// restart verb.
func TestC37TheHostReadsTheAlertOfTheLoopProjectsMissingCheckout(t *testing.T) {
	hw := newHomesWorld(t, homesOpts{})
	hw.deleteCheckout("loop")
	entry := hw.alertEntry(`dedupe_key = ?`, sched.AlertCoordinatorCheckoutMissing+":loop")
	e := hw.hostEntry(entry)
	if e == nil {
		t.Fatalf("the host's inbox does not list %s", entry)
	}
	if e["project"] != "loop" || !strings.Contains(e["text"].(string), "loop coordinator restart --project loop") {
		t.Fatalf("the entry = %v, want one that names loop and the restart", e)
	}
}
