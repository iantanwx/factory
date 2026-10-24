package overwatch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/machine"
)

// depsWorld is a fakeWorld whose Dependencies answers from a map.
type depsWorld struct {
	*fakeWorld
	deps map[string][]beadsx.Dependency // item -> its edges
	err  error
}

func (w *depsWorld) Dependencies(_ context.Context, _, item string) ([]beadsx.Dependency, error) {
	if w.err != nil {
		return nil, w.err
	}
	return w.deps[item], nil
}

func blocks(id, status string) beadsx.Dependency {
	return beadsx.Dependency{ID: id, Kind: beadsx.DepBlocks, Status: status}
}

// backlogWorld holds one ticket factorio-t1 in backlog with edges.
func backlogWorld(t *testing.T, edges ...beadsx.Dependency) *depsWorld {
	t.Helper()
	w := &depsWorld{fakeWorld: newFakeWorld(t), deps: map[string][]beadsx.Dependency{"factorio-t1": edges}}
	w.add("factorio", machine.Item{ID: "factorio-t1", Kind: machine.KindTicket, Status: machine.Backlog}, "")
	return w
}

// C14: a backlog ticket that dependency-advance cannot free is work: a
// canceled blocker, or no blocks edge. Every blocker done, or one open,
// prints a line with code 0. Edges of another kind are not blockers.
func TestC14ABacklogTicketThatDependencyAdvanceCannotFreeIsWork(t *testing.T) {
	const key = "overwatch:backlog:factorio-t1"
	cases := []struct {
		name  string
		edges []beadsx.Dependency
		code  Code
		line  string
	}{
		{"every blocker done", []beadsx.Dependency{blocks("factorio-a1", machine.Done), blocks("factorio-a2", machine.Done)}, Quiet,
			"factorio-t1 waits in backlog, and every blocker is done (factorio-a1 done, factorio-a2 done): dependency-advance moves it on its next fire"},
		{"one blocker canceled", []beadsx.Dependency{blocks("factorio-a1", machine.Done), blocks("factorio-a2", machine.Canceled)}, Work,
			"factorio-t1 waits in backlog on a canceled blocker factorio-a2 (factorio-a1 done, factorio-a2 canceled): " +
				"run `loop work unblock factorio-t1`, or name a new blocker (" + key + ")"},
		{"no edge", nil, Work,
			"factorio-t1 waits in backlog with no blocks edge, so dependency-advance never frees it: run `loop work unblock factorio-t1` (" + key + ")"},
		{"only a related edge", []beadsx.Dependency{{ID: "factorio-r1", Kind: beadsx.DepRelated, Status: machine.InProgress}}, Work,
			"factorio-t1 waits in backlog with no blocks edge, so dependency-advance never frees it: run `loop work unblock factorio-t1` (" + key + ")"},
		{"one blocker open", []beadsx.Dependency{blocks("factorio-a1", machine.Done), blocks("factorio-a2", machine.InReview)}, Quiet,
			"factorio-t1 waits in backlog on an open blocker (factorio-a1 done, factorio-a2 in_review); the open root: factorio-a2 in_review"},
		{"one open and one canceled", []beadsx.Dependency{blocks("factorio-a1", machine.Canceled), blocks("factorio-a2", machine.Todo)}, Quiet,
			"factorio-t1 waits in backlog on an open blocker (factorio-a1 canceled, factorio-a2 todo); the open root: factorio-a2 todo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pr := sweepWorkItemPart(t, backlogWorld(t, c.edges...), "backlog")
			if pr.Code != c.code || !slices.Equal(pr.Lines, []string{c.line}) {
				t.Fatalf("got %d %q\nwant %d %q", pr.Code, pr.Lines, c.code, c.line)
			}
		})
	}
}

// C14: the open root is the first open item off the chain of backlog
// blockers.
func TestC14TheLineNamesTheOpenRoot(t *testing.T) {
	w := backlogWorld(t, blocks("factorio-b1", machine.Backlog), blocks("factorio-c1", machine.Done))
	w.add("factorio", machine.Item{ID: "factorio-b1", Kind: machine.KindTicket, Status: machine.Backlog}, "")
	w.deps["factorio-b1"] = []beadsx.Dependency{blocks("factorio-r1", machine.InProgress),
		{ID: "factorio-x9", Kind: beadsx.DepRelated, Status: machine.Todo}}
	pr := sweepWorkItemPart(t, w, "backlog")
	want := []string{
		"factorio-t1 waits in backlog on an open blocker (factorio-b1 backlog, factorio-c1 done); the open root: factorio-r1 in_progress",
		"factorio-b1 waits in backlog on an open blocker (factorio-r1 in_progress); the open root: factorio-r1 in_progress",
	}
	if pr.Code != Quiet || !slices.Equal(pr.Lines, want) {
		t.Fatalf("got %d %q", pr.Code, pr.Lines)
	}
}

// C14: a prepared question that names the key ends the work, and a failed
// read of the edges is code 2.
func TestC14AQuestionThatNamesTheKeyEndsTheWorkAndAFailedReadIsUnread(t *testing.T) {
	w := backlogWorld(t)
	w.question(t, "Unblock overwatch:backlog:factorio-t1, or cancel it?", false)
	if pr := sweepWorkItemPart(t, w, "backlog"); pr.Code != Quiet || !strings.HasSuffix(pr.Lines[0], "(filed in question 1)") {
		t.Fatalf("a question that names the key: %+v", pr)
	}
	w.err = errors.New("bd timed out")
	if pr := sweepWorkItemPart(t, w, "backlog"); pr.Code != Unread || !strings.Contains(pr.Lines[0], "bd timed out") {
		t.Fatalf("a failed read: %+v", pr)
	}
}
