package overwatch

import (
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-1 (factorio-pwnr, F-72): two duplicate bugs of two merged fixes
// name one flaky key. The flake's run started after the first fix merged
// and before the second. The duplicate of the second fix files it, though
// the duplicate of the first fix is read first.
func TestAFlakeOlderThanTheLaterFixIsFiledThoughAnOlderDuplicateIsReadFirst(t *testing.T) {
	key := "overwatch:flaky:alpha:ci-yml/go-tests-race"
	w := newGHWorld(t, alpha)
	w.prs().AddRun(rerun(headOld, "failure", "success", w.now.Add(-3*time.Hour)))
	w.add("alpha", machine.Item{ID: "alpha-t1", Kind: machine.KindTicket, Status: machine.Done, Updated: w.now.Add(-8 * time.Hour)}, "fix the race")
	w.add("alpha", machine.Item{ID: "alpha-b1", Kind: machine.KindBug, Status: machine.Duplicate,
		Facts: map[string]string{machine.FactDuplicateOf: "alpha-t1"}}, "flaky race tests ("+key+")")
	w.add("alpha", machine.Item{ID: "alpha-t2", Kind: machine.KindTicket, Status: machine.Done, Updated: w.now.Add(-time.Hour)}, "fix the race again")
	w.add("alpha", machine.Item{ID: "alpha-b2", Kind: machine.KindBug, Status: machine.Duplicate,
		Facts: map[string]string{machine.FactDuplicateOf: "alpha-t2"}}, "the race again ("+key+")")
	pr := sweepOne(t, w, partNamed(t, "flaky"))
	if got := unfiled(pr, key); got != 0 {
		t.Fatalf("%d unfiled lines quote %s, want 0: %v", got, key, pr.Lines)
	}
	if len(pr.Lines) == 0 || !strings.Contains(pr.Lines[0], "(filed in alpha-b2)") {
		t.Fatalf("lines = %v, want the flake filed in alpha-b2", pr.Lines)
	}
	expect(t, pr, Quiet)
}
