//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// C5 and C6 end to end: the coordinator parks a todo ticket on one
// blocker, adds a second, then ends each block with work unblock --by.
// The ticket stays in backlog while a blocker is open, and returns to
// todo when the last one goes. work unblock with no --by refuses while a
// blocker is open (C7).
func TestWorkBlockAndUnblockByMoveATicketThroughBacklog(t *testing.T) {
	w := newWorld(t, acceptance.Options{})
	x := createTicket(w, "Ticket X", "Create `x.txt` holding the line `x`.")
	y := createTicket(w, "Ticket Y", "Create `y.txt` holding the line `y`.")
	tk := createTicket(w, "Ticket T", "Create `t.txt` holding the line `t`.")
	coord := func(args ...string) acceptance.Result {
		return w.Coordinator(append(args, "--project", acceptance.Project)...)
	}

	w.Must(coord("work", "block", tk, "--by", x))
	wantStatus(w, tk, machine.Backlog)
	w.Must(coord("work", "block", tk, "--by", y))
	wantStatus(w, tk, machine.Backlog)

	if r := coord("work", "unblock", tk); r.Code != int(cli.ExitUndecided) || r.RefusalCode() != cli.CodeUsage {
		t.Fatalf("work unblock with open blockers: %s", r)
	}
	r := w.Must(coord("work", "unblock", tk, "--by", x))
	if r.Str("status") != machine.Backlog || r.Data()["edge_removed"] != true {
		t.Fatalf("work unblock --by %s with %s open: %s", x, y, r)
	}
	wantStatus(w, tk, machine.Backlog)
	r = w.Must(coord("work", "unblock", tk, "--by", y))
	if r.Str("status") != machine.Todo || r.Data()["edge_removed"] != true {
		t.Fatalf("work unblock --by %s: %s", y, r)
	}
	wantStatus(w, tk, machine.Todo)
	if got := w.Show(tk).Facts[machine.FactPendingBlock]; got != "" {
		t.Fatalf("pending_block = %q after the last unblock", got)
	}
}
