package rpc

import (
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// factorio-obc: the coordinator of the loop calls host verbs from its own
// checkout, as the operator's shell would. A run and a room stay refused.
func TestCoordinatorCallsHostVerbsFromItsCheckout(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Checkouts = nil })
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("project list", found)
	h.start()
	coord := h.issue("coordinator-r-1", cli.PrincipalCoordinator, "")
	run := h.issue("run-a", cli.PrincipalRun, "T-A")
	room := h.issue("room-a", cli.PrincipalRoom, "T-B")

	env, code := h.run(coord, "project", "list")
	if code != 0 {
		t.Fatalf("coordinator: loop project list from its checkout exited %d: %v", code, env)
	}

	env, code = h.run(run, "project", "list")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")
	env, code = h.run(room, "project", "list")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")
	if n := h.count(`SELECT count(*) FROM audit WHERE action = ?`, AlertHostVerbFromCheckout); n != 2 {
		t.Fatalf("host_verb_from_checkout audit rows = %d, want 2: one for the run, one for the room", n)
	}
}

// factorio-obc: the server's probe admits the coordinator's `loop upgrade`
// from its checkout, and refuses a run's and a room's. The upgrade itself
// has no handler here, so an admitted call answers NOT_IMPLEMENTED.
func TestCoordinatorUpgradeFromItsCheckoutPassesTheProbe(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Checkouts = nil })
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.start()
	coord := h.issue("coordinator-r-1", cli.PrincipalCoordinator, "")
	run := h.issue("run-a", cli.PrincipalRun, "T-A")
	room := h.issue("room-a", cli.PrincipalRoom, "T-B")

	env, code := h.run(coord, "upgrade")
	wantRefusal(t, env, code, 2, cli.CodeNotImplemented, "")

	env, code = h.run(run, "upgrade")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")
	env, code = h.run(room, "upgrade")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")
}

// factorio-obc: the coordinator's token works only in its own checkout. In
// a run's checkout, with the coordinator's token, a host verb is refused.
func TestCoordinatorHostVerbFromAnotherCheckoutIsRefused(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Checkouts = nil })
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("project list", found)
	h.start()
	coord := h.issue("coordinator-r-1", cli.PrincipalCoordinator, "")
	run := h.issue("run-a", cli.PrincipalRun, "T-A")

	// The CLI reads the coordinator's token; the server sees the caller in
	// run A's checkout.
	env, code := h.runWith(t.Context(), h.client(coord), run, nil, "project", "list")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")
}

// factorio-obc: a tokenless caller inside the coordinator's checkout holds
// no coordinator token, so the host verb stays refused there.
func TestTokenlessHostVerbInsideTheCoordinatorCheckoutIsRefused(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Checkouts = nil })
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("project list", found)
	h.start()
	coord := h.issue("coordinator-r-1", cli.PrincipalCoordinator, "")

	// The CLI finds no token; the server sees the caller inside the
	// coordinator's checkout.
	env, code := h.runWith(t.Context(), h.client(t.TempDir()), coord, nil, "project", "list")
	wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")
}
