package server

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// C40: on a host with no loop project, a run of alpha reports a loop
// fault. The fault is instance-wide work, so no session gets its ring.
// `loop inbox list` from the operator's shell succeeds and lists the
// fault's entry, with no filter by session.
func TestC40TheHostReadsTheInbox(t *testing.T) {
	w := newHumanWorldOf(t, "", "iantanwx/alpha")
	if loop := w.srv.Sched.Topology().Loop; loop != "" {
		t.Fatalf("fixture: the host has the loop project %q", loop)
	}
	co := w.runToken("run-1", "", machine.StageImplementer)
	if env, code := w.as(co, "fault", "report", "--file", writeReport(t, "`loop branch push` answered INTERNAL.")); code != 0 {
		t.Fatalf("fault report = %d %v", code, env)
	}
	if err := w.srv.Notifier.Poke(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, sess := range w.host.Sessions() {
		for _, n := range w.host.Notifications(sess.ID) {
			if strings.Contains(n, "fault/F-1") {
				t.Fatalf("session %s got a ring of the fault: %q", sess.ID, n)
			}
		}
	}

	if _, err := rpc.EnsureAdminCredential(w.dir); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	env, code, _ := call(t, w.dir, 5*time.Second, nil, "inbox", "list")
	if code != 0 {
		t.Fatalf("inbox list from the host = %d %v", code, env)
	}
	var found bool
	for _, e := range env["data"].([]any) {
		found = found || e.(map[string]any)["entry"] == "fault/F-1"
	}
	if !found {
		t.Fatalf("the host's inbox list does not hold fault/F-1: %v", env["data"])
	}
}

// C40 while alpha's old session lives, on a host with no loop project:
// the old session serves alpha only, so the fault rings no session, and
// the old session's inbox does not list it. The host's inbox lists it.
func TestC40TheFaultOfAHostWithNoLoopProjectRingsNoSession(t *testing.T) {
	hw := newHomesWorld(t, homesOpts{old: true, noLoop: true})
	if loop := hw.srv.Sched.Topology().Loop; loop != "" {
		t.Fatalf("fixture: the host has the loop project %q", loop)
	}
	fault := hw.reportFault()
	hw.poke()
	for _, sess := range hw.host.Sessions() {
		for _, n := range hw.host.Notifications(sess.ID) {
			if strings.Contains(n, fault+" ") {
				t.Fatalf("session %s got a ring of the fault: %q", sess.ID, n)
			}
		}
	}
	if got := hw.inboxOf(hw.old); slices.Contains(got, fault) {
		t.Fatalf("the old session's inbox = %q, and lists the fault", got)
	}

	if _, err := rpc.EnsureAdminCredential(hw.dir); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	env, code, _ := call(t, hw.dir, 5*time.Second, nil, "inbox", "list")
	if code != 0 {
		t.Fatalf("inbox list from the host = %d %v", code, env)
	}
	if !slices.Contains(entryIDs(env), fault) {
		t.Fatalf("the host's inbox list does not hold %s: %v", fault, env["data"])
	}
}
