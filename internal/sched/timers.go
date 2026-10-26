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
	kind                 launchKind
}

// Timers lists every timer the scheduler fires, in firing order: each
// registered instance job, then for each project each run stage and agent
// job its config enables, except intake, and each registered project job.
// snap returns a project's config, or nil when it has none, so the project
// fires its jobs only. A project's schedule overrides host config's.
// fireDue fires these timers, and overwatch's timer-health judges them, so
// a row no timer names never fires and never stops.
func Timers(hc *config.HostConfig, projects []config.Project, snap func(project string) *config.ProjectSnapshot, jobs Jobs) []Timer {
	var out []Timer
	for _, name := range config.NativeJobs() {
		if slices.Contains(jobs.Instance, name) {
			out = append(out, Timer{Stage: name, Expr: hc.ThroughputFor(name).Schedule, kind: kindInstanceJob})
		}
	}
	for _, p := range projects {
		sn := snap(p.ID)
		// An agent job is a run stage that host config defines, so it fires
		// as one: only where the project lists it.
		stages := append(append(config.RunStages(), hc.AgentJobNames()...), config.NativeJobs()...)
		for _, stage := range stages {
			kind := kindRun
			if slices.Contains(config.NativeJobs(), stage) {
				kind = kindJob
				if !slices.Contains(jobs.Project, stage) {
					continue
				}
			} else if sn == nil || !sn.Config.StageEnabled(stage) || stage == config.StageIntake {
				continue
			}
			expr := hc.ThroughputFor(stage).Schedule
			if j, ok := hc.AgentJob(stage); ok {
				expr = j.Schedule
			}
			if sn != nil {
				if sc, ok := sn.Config.Stages[stage]; ok && sc.Schedule != "" {
					expr = sc.Schedule
				}
			}
			out = append(out, Timer{Project: p.ID, Stage: stage, Expr: expr, kind: kind})
		}
	}
	return out
}
