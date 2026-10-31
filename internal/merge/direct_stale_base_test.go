package merge_test

import (
	"testing"

	"github.com/iantanwx/loop/internal/machine"
)

// Two tickets whose CI passed on the same base: once the first one
// merges, the base moved, and the second one's CI never ran on the base
// it would land on. The direct merge must not land it at that head.
func TestDirectDoesNotMergeAHeadWhoseCIRanOnAnOlderBase(t *testing.T) {
	e := newDirect(t)
	a, _ := e.reviewed(sha('1'), true)
	b, _ := e.reviewed(sha('2'), true)
	for range 4 {
		e.tick()
	}
	landed := 0
	for _, m := range e.world.Merges() {
		if m.Status == 200 {
			landed++
		}
	}
	if landed != 1 {
		t.Fatalf("%d merges landed; want 1: the second head's CI ran on a base without the first merge", landed)
	}
	if e.item(a).Status == machine.Done && e.item(b).Status == machine.Done {
		t.Fatal("both tickets are done, and no CI ran on the base the second one landed on")
	}
}
