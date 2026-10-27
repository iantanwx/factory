package sched

import (
	"maps"
	"slices"

	"github.com/iantanwx/loop/internal/config"
)

// Jobs names the native jobs registered on a scheduler.
type Jobs struct {
	Instance []string // each covers the whole instance
	Project  []string // each runs for each project
}

// Jobs names the native jobs registered on s.
func (s *Scheduler) Jobs() Jobs {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	return Jobs{Instance: slices.Sorted(maps.Keys(s.instanceJobs)), Project: slices.Sorted(maps.Keys(s.jobs))}
}

// Timer is one cron row of schedule that the scheduler fires: an instance
// job when Project is empty, or else one project's stage or job. Expr is the
// cron it fires on.
type Timer struct {
	Project, Stage, Expr string
}

// Timers lists every timer the scheduler fires: the enabled Firings, in
// firing order. fireDue fires these timers, and overwatch's timer-health
// judges them, so a row no timer names never fires and never stops.
func Timers(hc *config.HostConfig, projects []config.Project, snap func(project string) *config.ProjectSnapshot, jobs Jobs) []Timer {
	var out []Timer
	for _, f := range Firings(hc, projects, snap, jobs) {
		if f.Enabled {
			out = append(out, Timer{Project: f.Project, Stage: f.Stage, Expr: f.Schedule})
		}
	}
	return out
}
