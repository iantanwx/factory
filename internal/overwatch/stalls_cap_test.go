package overwatch

import (
	"fmt"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// C10, F-46: a fire that starts no run because the stage holds its cap
// does not count against an item. The implementer's cap is 8, and eight
// implementer runs on other tickets started at 08:40 and run still.
// factorio-t1 entered todo at 08:50, and the fires at 08:53, 08:58 and
// 09:03 found the cap full. The clock reads 09:05.
func TestC10AFireAtAFullCapIsNoStall(t *testing.T) {
	w := newStallsWorld(t)
	if got := w.host.ThroughputFor(config.StageImplementer).Cap; got != 8 {
		t.Fatalf("implementer cap = %d, want 8", got)
	}
	w.item("factorio-t1", machine.KindTicket, machine.Todo, at(8, 50))
	w.entered(t, "factorio-t1", machine.Backlog, machine.Todo, at(8, 50))
	for i := range 8 {
		w.run(t, fmt.Sprintf("r-live%08d", i), config.StageImplementer, fmt.Sprintf("factorio-o%d", i), at(8, 40), time.Time{})
	}
	r := readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:factorio-t1@todo"); ok {
		t.Fatalf("every fire since factorio-t1 entered todo found the implementer at its cap, and it is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "factorio-t1"), "implementer holds its cap (8 of 8 live)")
}

// C10, F-46: a fire counts again once the cap has room. The eight runs
// ended at 08:55, so the fire at 08:53 found the cap full, and the fires
// at 08:58 and 09:03 had room and started no run on factorio-t1. At 09:00
// only the fire at 08:58 counts, and at 09:05 two do.
func TestC10AFireCountsOnceTheCapHasRoom(t *testing.T) {
	w := newStallsWorld(t)
	w.now = at(9, 0)
	w.item("factorio-t1", machine.KindTicket, machine.Todo, at(8, 50))
	w.entered(t, "factorio-t1", machine.Backlog, machine.Todo, at(8, 50))
	for i := range 8 {
		w.run(t, fmt.Sprintf("r-done%08d", i), config.StageImplementer, fmt.Sprintf("factorio-o%d", i), at(8, 40), at(8, 55))
	}
	r := readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:factorio-t1@todo"); ok {
		t.Fatalf("at 09:00 the fire at 08:53 found the cap full and one fire counts, and it is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "factorio-t1"), "only one fire",
		"implementer holds its cap (8 of 8 live) at the fire of 2026-10-24T08:53:00Z")
	w.now = at(9, 5)
	r = readStallsPart(t, w)
	if _, ok := findingOf(r, "overwatch:stalls:factorio-t1@todo"); !ok {
		t.Fatalf("at 09:05 two fires with room in the cap started no run on factorio-t1, and it is not work: %+v", r)
	}
}

// C10, F-46: the cap is instance-wide, and a pool's cap counts too. Four
// code-reviewer runs of factorio and six doc-reviewer runs of alpha fill
// the reviewers pool's cap of 10, while the code-reviewer's own cap of 8
// has room. The fires at 08:55, 09:00 and 09:05 found the pool full.
func TestC10AFireAtAFullPoolIsNoStall(t *testing.T) {
	w := newStallsWorld(t)
	w.item("factorio-t2", machine.KindTicket, machine.InReview, at(8, 50))
	w.entered(t, "factorio-t2", machine.InProgress, machine.InReview, at(8, 50))
	for i := range 4 {
		w.run(t, fmt.Sprintf("r-code%08d", i), config.StageCodeReviewer, fmt.Sprintf("factorio-o%d", i), at(8, 40), time.Time{})
	}
	for i := range 6 {
		w.exec(t, `INSERT INTO runs (run_id, project, stage, item, state, outcome_class, started_at) VALUES (?, 'alpha', ?, ?, 'running', 'done', ?)`,
			fmt.Sprintf("r-docs%08d", i), config.StageDocReviewer, fmt.Sprintf("alpha-e%d", i), at(8, 40).UnixMilli())
	}
	r := readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:factorio-t2@in_review"); ok {
		t.Fatalf("every fire since factorio-t2 entered in_review found the reviewers pool full, and it is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "factorio-t2"), "code-reviewer holds the cap of the reviewers pool (10 of 10 live)")
}
