package rpc

import (
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// factorio-kq28: a run or a room with a valid token reads `loop project list`
// from its own checkout. The read never rings the coordinator: it raises no
// open alert, whether the server answers or refuses it.
func TestRunAndRoomProjectListFromOwnCheckoutRaisesNoAlert(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.Checkouts = nil })
	if _, err := EnsureAdminCredential(h.dir); err != nil {
		t.Fatal(err)
	}
	h.handle("project list", found)
	h.start()
	run := h.issue("run-a", cli.PrincipalRun, "T-A")
	room := h.issue("room-a", cli.PrincipalRoom, "T-B")

	for _, who := range []string{run, room} {
		env, code := h.run(who, "project", "list")
		if code != 0 && code != 3 {
			t.Fatalf("project list from %s exited %d: %v", who, code, env)
		}
	}
	if n := h.count(`SELECT count(*) FROM alerts WHERE state = 'open'`); n != 0 {
		t.Fatalf("open alerts = %d after a run and a room read project list from their own checkouts, want 0", n)
	}
}

// factorio-pdlb: only a valid token's read in its own checkout is quiet. A
// run's host write verb, a tokenless caller and a token in another checkout
// still raise their alert.
func TestHostVerbFromCheckoutStillAlertsOutsideAQuietRead(t *testing.T) {
	cases := []struct {
		name string
		call func(h *harness, run, other string) (map[string]any, int)
	}{
		{"a run's upgrade from its own checkout", func(h *harness, run, _ string) (map[string]any, int) {
			return h.run(run, "upgrade")
		}},
		{"a tokenless project list inside a run's checkout", func(h *harness, run, _ string) (map[string]any, int) {
			return h.runWith(t.Context(), h.client(t.TempDir()), run, nil, "project", "list")
		}},
		{"a run's token reads project list in another run's checkout", func(h *harness, run, other string) (map[string]any, int) {
			return h.runWith(t.Context(), h.client(run), other, nil, "project", "list")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, func(o *Options) { o.Checkouts = nil })
			if _, err := EnsureAdminCredential(h.dir); err != nil {
				t.Fatal(err)
			}
			h.handle("project list", found)
			h.start()
			run := h.issue("run-a", cli.PrincipalRun, "T-A")
			other := h.issue("run-b", cli.PrincipalRun, "T-B")

			env, code := tc.call(h, run, other)
			wantRefusal(t, env, code, 3, cli.CodeCapabilityDenied, "host_verb_from_checkout")
			if n := h.count(`SELECT count(*) FROM alerts WHERE state = 'open' AND kind = ?`, AlertHostVerbFromCheckout); n != 1 {
				t.Fatalf("open host_verb_from_checkout alerts = %d, want 1", n)
			}
		})
	}
}
