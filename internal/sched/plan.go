package sched

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/config"
)

// FiringKind names what a firing starts.
type FiringKind string

const (
	FiringRun         FiringKind = "run"          // runs of a run stage
	FiringAgentJob    FiringKind = "agent-job"    // a run of a host config agent job
	FiringJob         FiringKind = "job"          // one project's native job
	FiringInstanceJob FiringKind = "instance-job" // a native job of the whole instance
)

// Firing is one project's stage or job, or an instance job when Project
// is empty. The scheduler fires exactly the firings with Enabled true, on
// Schedule.
type Firing struct {
	Project  string     `json:"project"`
	Stage    string     `json:"stage"`
	Kind     FiringKind `json:"kind"`
	Enabled  bool       `json:"enabled"`
	Schedule string     `json:"schedule"`
}

// Firings names every firing of the host, in the order fireDue walks it:
// each registered instance job, then for each project each run stage and
// agent job, enabled or not, and each registered project job. A run stage
// or an agent job is enabled where the project's config lists it, except
// intake, which runs in a room. snap returns a project's config, or nil
// when it has none, so the project fires its jobs only. A project's
// schedule is the one config.Effective puts in force. An instance job keeps
// host config's.
func Firings(hc *config.HostConfig, projects []config.Project, snap func(project string) *config.ProjectSnapshot, jobs Jobs) []Firing {
	var out []Firing
	for _, name := range config.NativeJobs() {
		if slices.Contains(jobs.Instance, name) {
			out = append(out, Firing{Stage: name, Kind: FiringInstanceJob, Enabled: true, Schedule: hc.ThroughputFor(name).Schedule})
		}
	}
	for _, p := range projects {
		sn := snap(p.ID)
		t := config.Effective(hc, configOf(sn))
		add := func(stage string, kind FiringKind, enabled bool) {
			out = append(out, Firing{Project: p.ID, Stage: stage, Kind: kind, Enabled: enabled, Schedule: t.Schedule(stage)})
		}
		listed := func(stage string) bool {
			return sn != nil && sn.Config.StageEnabled(stage) && stage != config.StageIntake
		}
		for _, stage := range config.RunStages() {
			add(stage, FiringRun, listed(stage))
		}
		for _, name := range hc.AgentJobNames() {
			add(name, FiringAgentJob, listed(name))
		}
		for _, name := range config.NativeJobs() {
			if slices.Contains(jobs.Project, name) {
				add(name, FiringJob, true)
			}
		}
	}
	return out
}

// configOf returns a snapshot's config, or nil for no snapshot.
func configOf(sn *config.ProjectSnapshot) *config.ProjectConfig {
	if sn == nil {
		return nil
	}
	return sn.Config
}

// StageRow is one firing with the values that start its runs: the caps
// admission checks, the profiles in the order dispatch tries them, the
// implementer's escalated profile, the pauses that hold it, its cron row
// and its live runs. Error holds why a run stage's project config does not
// read.
type StageRow struct {
	Firing
	Limits    []config.Limit         `json:"limits,omitempty"`
	Profiles  []Profile              `json:"profiles,omitempty"`
	Escalated *Profile               `json:"escalated,omitempty"`
	Pauses    []admission.StagePause `json:"pauses,omitempty"`
	LastFire  *time.Time             `json:"last_fire,omitempty"`
	NextFire  *time.Time             `json:"next_fire,omitempty"`
	Live      int                    `json:"live_runs"`
	Error     string                 `json:"error,omitempty"`
}

// StageList lists a row for each firing of the host, or of one project
// when project is not empty.
func (s *Scheduler) StageList(ctx context.Context, project string) ([]StageRow, error) {
	hc, err := s.cfg.Host.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("sched: load host config: %w", err)
	}
	projects, err := s.cfg.Projects()
	if err != nil {
		return nil, err
	}
	snaps, errs := map[string]*config.ProjectSnapshot{}, map[string]error{}
	snap := func(p string) *config.ProjectSnapshot {
		snaps[p], errs[p] = s.cfg.ProjectConfig(ctx, p)
		if errs[p] != nil {
			snaps[p] = nil
		}
		return snaps[p]
	}
	if project != "" {
		projects = slices.DeleteFunc(projects, func(p config.Project) bool { return p.ID != project })
	}
	firings := Firings(hc, projects, snap, s.Jobs())

	var pauses []admission.StagePause
	var crons map[string]cronRow
	live := map[string]int{}
	err = s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		if pauses, err = admission.Pauses(ctx, tx); err != nil {
			return err
		}
		if crons, err = readCrons(ctx, tx); err != nil {
			return err
		}
		rs, err := tx.QueryContext(ctx, `SELECT project, stage, count(*) FROM runs WHERE ended_at IS NULL GROUP BY project, stage`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var p, st string
			var n int
			if err := rs.Scan(&p, &st, &n); err != nil {
				return err
			}
			live[stageKey(p, st)] = n
		}
		return rs.Err()
	})
	if err != nil {
		return nil, err
	}

	out := make([]StageRow, 0, len(firings))
	for _, f := range firings {
		if project != "" && f.Project != project {
			continue
		}
		r := StageRow{Firing: f, Limits: hc.Limits(f.Stage), Live: live[stageKey(f.Project, f.Stage)]}
		if c := crons[stageKey(f.Project, f.Stage)]; c.ok {
			r.LastFire, r.NextFire = msTime(c.last), msTime(c.due)
		}
		if f.Kind == FiringRun || f.Kind == FiringAgentJob {
			for _, p := range pauses {
				if p.Stage == f.Stage && (p.Project == "" || p.Project == f.Project) {
					r.Pauses = append(r.Pauses, p)
				}
			}
			if err := errs[f.Project]; err != nil {
				r.Error = err.Error()
			} else if sn := snaps[f.Project]; sn != nil {
				r.Profiles = profiles(f.Stage, sn.Config, hc)
				if f.Stage == config.StageImplementer {
					r.Escalated = &escalatedProfiles(f.Stage, sn.Config)[0]
				}
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// msTime reads a column of unix milliseconds, or nil when it is null.
func msTime(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := time.UnixMilli(ms.Int64).UTC()
	return &t
}
