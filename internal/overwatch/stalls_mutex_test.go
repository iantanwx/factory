package overwatch

import (
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-2 (factorio-gqyt, F-57): the spec-author is a mutex stage. A fire
// that admission refuses because a run on an older item of the queue holds
// the stage mutex is the queue draining, and does not count. factorio-e1
// entered spec_needed at 09:40, and its run r-e1e1e1e1e1e1 started at 09:45
// and holds the mutex still. factorio-e2 entered spec_needed at 09:48, and
// the fires at 09:50 and 09:55 started no run. The clock reads 09:56.
func TestC10AFireThatAnOlderItemsMutexRefusedIsNoStall(t *testing.T) {
	w := newStallsWorld(t)
	w.now = at(9, 56)
	w.configs["factorio"].Stages[config.StageSpecAuthor] = config.StageConfig{Schedule: "*/5 * * * *"}
	w.item("factorio-e1", machine.KindEpic, machine.SpecDrafting, at(9, 45))
	w.entered(t, "factorio-e1", machine.PRDSignoff, machine.SpecNeeded, at(9, 40))
	w.entered(t, "factorio-e1", machine.SpecNeeded, machine.SpecDrafting, at(9, 45))
	w.run(t, "r-e1e1e1e1e1e1", config.StageSpecAuthor, "factorio-e1", at(9, 45), time.Time{})
	w.exec(t, `INSERT INTO stage_mutexes (project, stage, holder_run_id, acquired_at) VALUES ('factorio', ?, 'r-e1e1e1e1e1e1', ?)`,
		config.StageSpecAuthor, at(9, 45).UnixMilli())
	w.item("factorio-e2", machine.KindEpic, machine.SpecNeeded, at(9, 48))
	w.entered(t, "factorio-e2", machine.PRDSignoff, machine.SpecNeeded, at(9, 48))
	r := readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:factorio-e2@spec_needed"); ok {
		t.Fatalf("factorio-e2 waits behind factorio-e1, whose run holds the spec-author mutex, and it is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "factorio-e2"), "spec-author", "r-e1e1e1e1e1e1")
}

// C-bug-2 (factorio-gqyt, F-57): a run holds the mutex of its stage only
// while it is live, and only a run on an older item of the queue drains it.
// factorio-e2 entered spec_needed at 09:48, and the fires at 09:50 and
// 09:55 started no run. The clock reads 09:56.
func TestC10OnlyAnOlderItemsLiveMutexRunDrainsTheQueue(t *testing.T) {
	cases := []struct {
		name           string
		entered        time.Time // when factorio-e1 entered spec_needed
		started, ended time.Time // its run r-e1e1e1e1e1e1
		work           bool
	}{
		// The mutex of the younger factorio-e1 refused both fires, and
		// both count.
		{"a younger item's run", at(9, 49), at(9, 49), time.Time{}, true},
		// The run settled at 09:52: it refused the fire at 09:50 only.
		{"a run that ended between the fires", at(9, 40), at(9, 45), at(9, 52), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStallsWorld(t)
			w.now = at(9, 56)
			w.configs["factorio"].Stages[config.StageSpecAuthor] = config.StageConfig{Schedule: "*/5 * * * *"}
			w.item("factorio-e1", machine.KindEpic, machine.SpecDrafting, c.started)
			w.entered(t, "factorio-e1", machine.PRDSignoff, machine.SpecNeeded, c.entered)
			w.entered(t, "factorio-e1", machine.SpecNeeded, machine.SpecDrafting, c.started)
			w.run(t, "r-e1e1e1e1e1e1", config.StageSpecAuthor, "factorio-e1", c.started, c.ended)
			w.item("factorio-e2", machine.KindEpic, machine.SpecNeeded, at(9, 48))
			w.entered(t, "factorio-e2", machine.PRDSignoff, machine.SpecNeeded, at(9, 48))
			r := readStallsPart(t, w)
			if _, ok := findingOf(r, "overwatch:stalls:factorio-e2@spec_needed"); ok != c.work {
				t.Fatalf("work = %v, want %v: %+v", ok, c.work, r)
			}
			if !c.work {
				mustContain(t, noteOf(t, r, "factorio-e2"), "only one fire", "r-e1e1e1e1e1e1 on factorio-e1 at the fire of "+stamp(at(9, 50)))
			}
		})
	}
}
