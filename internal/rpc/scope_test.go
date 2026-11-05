package rpc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/coord"
)

// scopeTopology is a host whose loop project is "loop", with alpha and
// beta beside it.
func scopeTopology() coord.Topology { return coord.Topology{Loop: "loop", Homes: map[string]bool{}} }

// issueIn writes a coordinator token of project into a new checkout and
// returns the checkout. An empty project is the old session's token.
func (h *harness) issueIn(runID, project string) string {
	h.t.Helper()
	checkout := filepath.Join(h.dir, "checkouts", runID)
	if err := os.MkdirAll(checkout, 0o700); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.tokens.IssueToCheckout(context.Background(), IssueRequest{
		RunID: runID, Kind: cli.PrincipalCoordinator, Project: project, Checkout: checkout,
	}); err != nil {
		h.t.Fatal(err)
	}
	return checkout
}

// C6, spec work entry 3: each instance-wide verb admits only the loop
// project's coordinator and the old session. A coordinator of another
// project gets loop_project_only, and the verb's handler never runs. For
// the client verbs upgrade and rollback, the CLI's authorization call
// returns the refusal, so the starter never runs.
func TestC6AnInstanceWideVerbOnlyFromTheLoopProjectsCoordinator(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Checkouts = nil; o.Topology = scopeTopology })
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	starts := map[string]*atomic.Int32{}
	for _, path := range []string{"fault ack", "fault resolve", "retro compared", "coordinator upgrade", "doctor-github", "doctor-env"} {
		n := new(atomic.Int32)
		starts[path] = n
		h.handle(path, func(context.Context, *Call) cli.Result { n.Add(1); return cli.Found(map[string]any{"done": true}) })
	}
	h.start()
	alpha, loop, old := h.issueIn("coord-alpha", "alpha"), h.issueIn("coord-loop", "loop"), h.issueIn("coord-old", "")

	calls := []struct {
		path string
		args []string
	}{
		{"fault ack", []string{"fault", "ack", "F-1"}},
		{"fault resolve", []string{"fault", "resolve", "F-1", "--fixed-in", "v9"}},
		{"retro compared", []string{"retro", "compared", "L-1"}},
		{"coordinator upgrade", []string{"coordinator", "upgrade", "--body", "the operator said upgrade"}},
		{"doctor-github", []string{"doctor-github"}},
		{"doctor-env", []string{"doctor-env"}},
	}
	for _, c := range calls {
		env, code := h.run(alpha, c.args...)
		wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "loop_project_only")
		if n := starts[c.path].Load(); n != 0 {
			t.Fatalf("%s ran %d times for alpha's coordinator", c.path, n)
		}
		for _, who := range []string{loop, old} {
			if env, code := h.run(who, c.args...); code != 0 {
				t.Fatalf("%s from %s = %d %v", c.path, who, code, env)
			}
		}
		if n := starts[c.path].Load(); n != 2 {
			t.Fatalf("%s ran %d times, want 2: the loop project's coordinator and the old session", c.path, n)
		}
	}
	for _, verb := range []string{"upgrade", "rollback"} {
		env, code := h.run(alpha, verb)
		wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "loop_project_only")
		// An admitted probe reaches the client verb, which has no starter here.
		for _, who := range []string{loop, old} {
			env, code := h.run(who, verb)
			wantRefusal(t, env, code, 2, cli.CodeNotImplemented, "")
		}
	}
	// The binary's doctor sends the authorization probe itself before it
	// reads the host (cmd/loop/release.go, prepare), so the harness client
	// does not gate it.
	doctor, _ := cli.Surface().Verb("doctor")
	probe := *doctor
	probe.RPC = cli.AuthorizeMethod
	authorize := func(cwd string) cli.Result {
		h.proc.setCwd(cwd)
		return h.client(cwd).Remote(context.Background(), cli.Invocation{
			Verb: &probe, Flags: map[string]string{cli.ExtraAuthorize: "true"}, RequestID: "doctor-" + filepath.Base(cwd),
		})
	}
	if res := authorize(alpha); res.Refusal == nil || res.Refusal.Code != cli.CodeCapabilityDenied || res.Refusal.Rule != "loop_project_only" {
		t.Fatalf("doctor probe from alpha = %+v, want loop_project_only", res)
	}
	for _, who := range []string{loop, old} {
		if res := authorize(who); res.Refusal != nil {
			t.Fatalf("doctor probe from %s = %+v", who, res.Refusal)
		}
	}
}

// C5 and C8, spec work entry 3: the scope check refuses a record of
// another project with other_project, and its hint names the owner. The
// loop project's coordinator reaches a record of another project that an
// entry routed to it names, and --project B only with such a record. A
// bug in B passes only when its body names a fault of B.
func TestC5TheScopeCheckRefusesARecordOfAnotherProject(t *testing.T) {
	routed := coord.Record{Kind: coord.KindRun, ID: "r-b1", Project: "beta"}
	records := func(_ context.Context, inv cli.Invocation) ([]coord.Record, error) {
		var out []coord.Record
		if id := inv.Arg("run"); id != "" {
			out = append(out, coord.Record{Kind: coord.KindRun, ID: id, Project: map[string]string{"r-a1": "alpha", "r-b1": "beta", "r-b2": "beta"}[id]})
		}
		if inv.Flags["body"] == "fault F-2" {
			out = append(out, coord.Record{Kind: coord.KindFault, ID: "F-2", Project: "beta"})
		}
		if p := inv.Flags["project"]; p != "" {
			out = append(out, coord.Record{Kind: coord.KindProject, ID: p, Project: p})
		}
		return out, nil
	}
	h := newHarness(t, func(o *Options) {
		o.Topology = scopeTopology
		o.Records = records
		o.Routed = func(_ context.Context, r coord.Record) (bool, error) { return r == routed, nil }
	})
	var created atomic.Int32
	h.handle("run show", found)
	h.handle("run list", found)
	h.handle("work create", func(context.Context, *Call) cli.Result {
		created.Add(1)
		return cli.Found(map[string]any{"item": "tb-9"})
	})
	h.start()
	alpha, loop, old := h.issueIn("coord-alpha", "alpha"), h.issueIn("coord-loop", "loop"), h.issueIn("coord-old", "")

	if env, code := h.run(alpha, "run", "show", "r-a1"); code != 0 {
		t.Fatalf("alpha's own run = %d %v", code, env)
	}
	env, code := h.run(alpha, "run", "show", "r-b1")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "other_project")
	if hint, _ := env["hint"].(string); !strings.Contains(hint, "beta") {
		t.Fatalf("the hint names no owner: %v", env)
	}
	env, code = h.run(alpha, "run", "list", "--project", "beta")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "other_project")

	// The loop project's coordinator reaches the run an entry names, and
	// no other run of beta.
	if env, code := h.run(loop, "run", "show", "r-b1"); code != 0 {
		t.Fatalf("loop: the routed run = %d %v", code, env)
	}
	env, code = h.run(loop, "run", "show", "r-b2")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "other_project")
	env, code = h.run(loop, "run", "list", "--project", "beta")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "other_project")

	// The fault rule of a bug in another project.
	bug := []string{"work", "create", "--kind", "bug", "--project", "beta", "--title", "t", "--request", "the operator's words"}
	if env, code := h.run(loop, append(bug, "--body", "fault F-2")...); code != 0 {
		t.Fatalf("loop: a bug for beta's fault = %d %v", code, env)
	}
	env, code = h.run(loop, append(bug, "--body", "no fault")...)
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "fault_bug_only")
	env, code = h.run(alpha, append(bug, "--body", "fault F-2")...)
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "other_project")
	if n := created.Load(); n != 1 {
		t.Fatalf("work create ran %d times, want 1", n)
	}

	// The old session stays unscoped.
	for _, args := range [][]string{{"run", "show", "r-b2"}, {"run", "list", "--project", "beta"}} {
		if env, code := h.run(old, args...); code != 0 {
			t.Fatalf("old session %v = %d %v", args, code, env)
		}
	}
}

// A server with no topology checks no scope, so every coordinator token
// acts as before ADR 0015.
func TestNoTopologyChecksNoScope(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Checkouts = nil })
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("fault ack", found)
	h.start()
	alpha := h.issueIn("coord-alpha", "alpha")
	if env, code := h.run(alpha, "fault", "ack", "F-1"); code != 0 {
		t.Fatalf("fault ack with no topology = %d %v", code, env)
	}
}

// Spec work entry 3: the handler of a coordinator's call reads the caller
// with CallerFrom, for a list it filters by reach. A run's call carries
// none.
func TestCallerFromNamesTheCoordinatorsReach(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Topology = scopeTopology })
	h.handle("run list", func(ctx context.Context, _ *Call) cli.Result {
		c, ok := CallerFrom(ctx)
		return cli.Found(map[string]any{"ok": ok, "reach": int(c.Reach), "project": c.Project})
	})
	h.handle("work list", func(ctx context.Context, _ *Call) cli.Result {
		_, ok := CallerFrom(ctx)
		return cli.Found(map[string]any{"ok": ok})
	})
	h.start()
	for _, tc := range []struct {
		project string
		reach   coord.Reach
	}{{"alpha", coord.ReachProject}, {"loop", coord.ReachLoop}, {"", coord.ReachUnscoped}} {
		env, code := h.run(h.issueIn("coord-"+tc.project, tc.project), "run", "list")
		data, _ := env["data"].(map[string]any)
		if code != 0 || data["ok"] != true || data["reach"] != float64(tc.reach) || data["project"] != tc.project {
			t.Fatalf("CallerFrom of %q's coordinator = %d %v", tc.project, code, env)
		}
	}
	env, code := h.run(h.issue("run-a", cli.PrincipalRun, "T-A"), "work", "list")
	if data, _ := env["data"].(map[string]any); code != 0 || data["ok"] != false {
		t.Fatalf("CallerFrom of a run = %d %v", code, env)
	}
}
