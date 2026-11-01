package orcalane_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost/orca"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/server/orcalane"
)

// refusingOrca is the world with one worktree that orca refuses to remove.
type refusingOrca struct {
	*world
	refused string
}

func (r refusingOrca) RemoveWorktree(ctx context.Context, wt string) error {
	if wt == r.refused {
		r.event("remove %s", wt)
		return &orca.Error{Verb: "worktree rm", Code: "runtime_error", Exit: 1}
	}
	return r.world.RemoveWorktree(ctx, wt)
}

// C-bug-1: one worktree that orca refuses to remove does not stop the lane.
// The sweep removes the other worktrees, and both lane binaries run.
func TestCBug1OneRefusedWorktreeDoesNotStopTheLane(t *testing.T) {
	w := newWorld(t)
	fx := w.fixture()
	w.trees[fx] = []string{"/srv/checkouts/lane-a", "/srv/checkouts/lane-b"}
	w.job.Orca = refusingOrca{world: w, refused: "/srv/checkouts/lane-a"}
	w.tick(context.Background())
	if !slices.Contains(w.events, "remove /srv/checkouts/lane-b") {
		t.Errorf("the sweep stopped at the refused worktree: events %v", w.events)
	}
	if got := w.binsRun(); !slices.Equal(got, []string{release.LaneContractFile, release.LaneAcceptanceFile}) {
		t.Fatalf("lane binaries run = %v, want both: a refused worktree stopped the lane", got)
	}
}

// fakeOrcaBin is an orca binary that answers each call with one envelope.
type fakeOrcaBin struct{ stdout string }

func (f fakeOrcaBin) Run(context.Context, []string) (orca.Output, error) {
	return orca.Output{Code: 1, Stdout: []byte(f.stdout)}, nil
}

// C-bug-2: the log line of a refused worktree names the worktree and the
// message orca gave, not only its code.
func TestCBug2TheLogNamesTheRefusedWorktreeAndOrcasMessage(t *testing.T) {
	const msg = "worktree has 1 live terminal"
	cli := orcalane.OrcaCLI{Runner: fakeOrcaBin{stdout: `{"ok":false,"error":{"code":"runtime_error","message":"` + msg + `"}}`}}
	w := newWorld(t)
	fx := w.fixture()
	const wt = "/srv/checkouts/lane-a"
	w.trees[fx] = []string{wt}
	w.job.Orca = sweepOnly{world: w, cli: cli}
	w.tick(context.Background())
	logs := w.logs()
	if len(logs) != 1 {
		t.Fatalf("logs = %v", logs)
	}
	data, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, "worktree rm") {
			line = l
		}
	}
	if !strings.Contains(line, wt) || !strings.Contains(line, msg) {
		t.Fatalf("the refusal line %q names neither %s nor orca's message %q; log:\n%s", line, wt, msg, data)
	}
}

// sweepOnly is the world, with orca's real CLI parsing for worktree rm.
type sweepOnly struct {
	*world
	cli orcalane.OrcaCLI
}

func (s sweepOnly) RemoveWorktree(ctx context.Context, wt string) error {
	return s.cli.RemoveWorktree(ctx, wt)
}
