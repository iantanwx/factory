package overwatch

import (
	"strings"
	"testing"
	"time"
)

// C17, F-35: the scheduler fires the rows of registered projects only. A
// cron row of a project that is no longer registered never fires again, and
// the scheduler never deletes it. So it is no stopped timer.
func TestC17ACronRowOfAnUnregisteredProjectIsNoStoppedTimer(t *testing.T) {
	w := newSchedWorld(t) // registers factorio and alpha only
	w.tick(t, w.now)
	w.cronRow(t, "", "overwatch", everyTen, w.now.Add(-time.Minute))
	last := w.now.Add(-3 * 24 * time.Hour)
	w.cronRow(t, "grinch", "implementer", "*/5 * * * *", last)
	w.cronRow(t, "grinch", "dependency-advance", "*/5 * * * *", last)
	p := readTimers(t, w)
	if all := strings.Join(p.Lines, "\n"); p.Code != Quiet || strings.Contains(all, "grinch") {
		t.Fatalf("code %d, lines %q; want 0 and no line for the unregistered project grinch", p.Code, p.Lines)
	}
}

// C17, decision factorio-kbr: the scheduler fires only the stages a project
// enables. The row of a stage the project no longer enables is no stopped
// timer, while the row of a stage it enables still is.
func TestC17ACronRowOfADisabledStageIsNoStoppedTimer(t *testing.T) {
	w := newSchedWorld(t) // factorio enables the implementer only
	w.tick(t, w.now)
	last := w.now.Add(-3 * 24 * time.Hour)
	w.cronRow(t, "factorio", "doc-reviewer", "*/5 * * * *", last)
	w.cronRow(t, "factorio", "implementer", "*/5 * * * *", last)
	p := readTimers(t, w)
	all := strings.Join(p.Lines, "\n")
	if strings.Contains(all, "factorio/doc-reviewer") {
		t.Fatalf("lines %q; want no line for the disabled stage factorio/doc-reviewer", p.Lines)
	}
	if p.Code != Work || !strings.Contains(all, "overwatch:timer-health:factorio/implementer") {
		t.Fatalf("code %d, lines %q; want work keyed factorio/implementer", p.Code, p.Lines)
	}
}

// C17, decision factorio-kbr: an agent job that host config no longer
// defines never fires, even where a project still lists it. Its row is no
// stopped timer.
func TestC17ACronRowOfARemovedAgentJobIsNoStoppedTimer(t *testing.T) {
	w := newSchedWorld(t)
	w.configs["factorio"].Stages["nightly"] = w.configs["factorio"].Stages["implementer"]
	w.tick(t, w.now)
	w.cronRow(t, "factorio", "nightly", "0 3 * * *", w.now.Add(-3*24*time.Hour))
	if p := readTimers(t, w); p.Code != Quiet || strings.Contains(strings.Join(p.Lines, "\n"), "nightly") {
		t.Fatalf("code %d, lines %q; want 0 and no line for the removed agent job nightly", p.Code, p.Lines)
	}
}

// C17, decision factorio-kbr: a native job that the server no longer
// registers, such as the GitHub reactor on a host with no App, never fires.
// Its row is no stopped timer.
func TestC17ACronRowOfAnUnregisteredNativeJobIsNoStoppedTimer(t *testing.T) {
	w := newSchedWorld(t)
	w.jobs.Instance = []string{"overwatch"}
	w.tick(t, w.now)
	w.cronRow(t, "", "github-reactor", "* * * * *", w.now.Add(-3*24*time.Hour))
	if p := readTimers(t, w); p.Code != Quiet || strings.Contains(strings.Join(p.Lines, "\n"), "github-reactor") {
		t.Fatalf("code %d, lines %q; want 0 and no line for the unregistered job github-reactor", p.Code, p.Lines)
	}
}
