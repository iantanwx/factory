package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
)

// Throughput is one entry of host config's throughput map (KTD30). Cap is
// the most the stage holds at once: live runs for a run stage, epics in
// counted document states for intake, and concurrent runs of a native
// job. Schedule is the stage's cron. Project config may override the
// schedule and never the cap (R35). A pool entry holds a cap only.
type Throughput struct {
	Cap      int    `yaml:"cap"`
	Schedule string `yaml:"schedule,omitempty"`
}

// The run stages (R13). The machine's claim rows use these names. Intake
// and the fixer claim no item.
const (
	StageImplementer  = "implementer"
	StageCodeReviewer = "code-reviewer"
	StageDocReviewer  = "doc-reviewer"
	StageAmender      = "amender"
	StageSpecAuthor   = "spec-author"
	StageDecomposer   = "decomposer"
	StageFixer        = "fixer"
	StageReproducer   = "reproducer"
	// StageIntake is the stage whose cap counts epics, not runs (KTD30).
	StageIntake = "intake"
)

// The stage set (R13). The native jobs claim no item.
var (
	runStages = []string{
		StageImplementer, StageCodeReviewer, StageDocReviewer, StageAmender, StageSpecAuthor,
		StageDecomposer, StageFixer, StageReproducer, StageIntake,
	}
	nativeJobs = []string{"merge-handoff", "dependency-advance", "decision-timers", "github-reactor", "overwatch"}
	// optional lists the keys that a later release added. A host config
	// that an older release wrote holds no entry for them, so a missing
	// entry reads as its default and is never written to the file
	// (docs/plans/factorio-a7r-overwatch-sweep-spec.md, Decisions). Every
	// other key stays required.
	optional = []string{"overwatch"}
	// pools bound several stages together. The reviewers pool is today's
	// REVIEW_CAP over every reviewer stage (scripts/sdlc/review-cap.ts,
	// alpha), next to each stage's own cap.
	pools = []pool{{name: "reviewers", stages: []string{StageCodeReviewer, StageDocReviewer}}}
)

type pool struct {
	name   string
	stages []string
}

// RunStages lists every stage that runs an agent, in flow order.
func RunStages() []string { return slices.Clone(runStages) }

// NativeJobs lists every stage the server runs itself (R24).
func NativeJobs() []string { return slices.Clone(nativeJobs) }

// ThroughputKeys lists every key of the throughput map: each run stage,
// each native job and each pool. The map must hold each one except an
// optional key, which ThroughputFor reads as its default.
func ThroughputKeys() []string {
	out := append(RunStages(), nativeJobs...)
	for _, p := range pools {
		out = append(out, p.name)
	}
	return out
}

func isPool(key string) bool {
	return slices.ContainsFunc(pools, func(p pool) bool { return p.name == key })
}

// DefaultThroughput is the throughput map a new host config holds. R13
// sets the caps of implementer, reviewers, code-reviewer and intake. The
// current loop capped no other stage, so each other cap is a starting
// value for the operator to tune. The schedules follow the current timer
// table (scripts/sdlc/timer-table.ts MAIN_CRONS, alpha). The implementer
// holds the minutes of all three implementer rows there, so a reject waits
// at most one reviewer cycle for it. internal/sched/cadence_test.go holds
// why. Each firing fills the stage's free cap, so the schedule bounds the
// wait and not how many runs start.
func DefaultThroughput() map[string]Throughput {
	every5 := "*/5 * * * *"
	return map[string]Throughput{
		StageImplementer:     {Cap: 8, Schedule: "3,8,13,18,23,28,33,38,43,48,53,58 * * * *"},
		StageCodeReviewer:    {Cap: 8, Schedule: every5},
		StageDocReviewer:     {Cap: 10, Schedule: "26,56 * * * *"},
		"reviewers":          {Cap: 10},
		StageAmender:         {Cap: 2, Schedule: "16,46 * * * *"},
		StageSpecAuthor:      {Cap: 2, Schedule: "25,55 * * * *"},
		StageDecomposer:      {Cap: 2, Schedule: "10,40 * * * *"},
		StageFixer:           {Cap: 1, Schedule: "14,44 * * * *"},
		StageReproducer:      {Cap: 2, Schedule: "12,42 * * * *"},
		StageIntake:          {Cap: 8, Schedule: "20,50 * * * *"},
		"merge-handoff":      {Cap: 1, Schedule: every5},
		"dependency-advance": {Cap: 1, Schedule: every5},
		"decision-timers":    {Cap: 1, Schedule: "* * * * *"},
		"github-reactor":     {Cap: 1, Schedule: "* * * * *"},
		"overwatch":          {Cap: 1, Schedule: "3,13,23,33,43,53 * * * *"},
	}
}

// ThroughputFor returns a stage's, job's or pool's entry. A native job that a
// later release added, which the host config does not hold, returns its
// DefaultThroughput entry. The returned value is never written into c.
func (c *HostConfig) ThroughputFor(name string) Throughput {
	e, _ := c.throughput(name)
	return e
}

// throughput returns name's entry, and whether the file holds it or name is
// an optional key that reads as its default.
func (c *HostConfig) throughput(name string) (Throughput, bool) {
	if e, ok := c.Throughput[name]; ok {
		return e, true
	}
	if slices.Contains(optional, name) {
		return DefaultThroughput()[name], true
	}
	return Throughput{}, false
}

// DefaultHost is the host config a first start writes when none exists.
func DefaultHost(stateDir string) *HostConfig {
	return &HostConfig{StateDir: stateDir, Throughput: DefaultThroughput()}
}

func (c *HostConfig) validateThroughput(ck *checker) {
	for _, key := range ThroughputKeys() {
		e, ok := c.Throughput[key]
		f := "throughput." + key
		switch {
		case !ok && slices.Contains(optional, key):
			continue
		case !ok:
			ck.fail(f, "is missing; every stage, native job and pool needs an entry")
			continue
		case e.Cap < 1:
			ck.fail(f+".cap", "must be at least 1")
		}
		switch {
		case isPool(key) && e.Schedule != "":
			ck.fail(f+".schedule", "must be absent; a pool bounds other stages and runs nothing")
		case !isPool(key) && (e.Schedule == "" || HasControl(e.Schedule)):
			ck.fail(f+".schedule", "must be a nonempty schedule")
		}
	}
	for _, key := range slices.Sorted(maps.Keys(c.Throughput)) {
		if !slices.Contains(ThroughputKeys(), key) {
			ck.fail("throughput."+key, "is not a stage, native job or pool of this loop")
		}
	}
}

// AgentJob is one stage kind that host config defines: an agent run that
// claims no item and starts on Schedule, ported from the daily dispatch
// row of the current loop (scripts/sdlc/daily-dispatch.ts, alpha). Runbook
// is the repository path the run follows. A project runs the job only when
// its config lists it under stages, which may override its harness, model,
// budget and schedule like any run stage's.
type AgentJob struct {
	Cap      int    `yaml:"cap"`
	Schedule string `yaml:"schedule"`
	Runbook  string `yaml:"runbook"`
}

// AgentJob returns the agent job of name, when host config defines one.
func (c *HostConfig) AgentJob(name string) (AgentJob, bool) {
	j, ok := c.AgentJobs[name]
	return j, ok
}

// AgentJobNames lists the agent jobs, sorted.
func (c *HostConfig) AgentJobNames() []string { return slices.Sorted(maps.Keys(c.AgentJobs)) }

func (c *HostConfig) validateAgentJobs(ck *checker) {
	for _, name := range c.AgentJobNames() {
		j, f := c.AgentJobs[name], "agent_jobs."+name
		switch {
		case !validIdent(name):
			ck.fail(f, "must be a lower-case identifier")
			continue
		case slices.Contains(ThroughputKeys(), name):
			ck.fail(f, "names a stage, native job or pool of this loop")
			continue
		}
		if j.Cap < 1 {
			ck.fail(f+".cap", "must be at least 1")
		}
		if j.Schedule == "" || HasControl(j.Schedule) {
			ck.fail(f+".schedule", "must be a nonempty schedule")
		}
		if j.Runbook == "" || !repoPath(j.Runbook) {
			ck.fail(f+".runbook", "must be a clean path inside the repository")
		}
	}
}

// Limit is one cap a stage's admission checks: the cap and the stages
// whose live runs count against it.
type Limit struct {
	Name   string
	Cap    int
	Stages []string
}

// Limits returns the stage's own limit, then the limit of each pool that
// holds the stage. An agent job's own cap is its one limit. A stage with
// no entry has none.
func (c *HostConfig) Limits(stage string) []Limit {
	if j, ok := c.AgentJob(stage); ok {
		return []Limit{{Name: stage, Cap: j.Cap, Stages: []string{stage}}}
	}
	e, ok := c.throughput(stage)
	if !ok || isPool(stage) {
		return nil
	}
	out := []Limit{{Name: stage, Cap: e.Cap, Stages: []string{stage}}}
	for _, p := range pools {
		if slices.Contains(p.stages, stage) {
			out = append(out, Limit{Name: p.name, Cap: c.ThroughputFor(p.name).Cap, Stages: slices.Clone(p.stages)})
		}
	}
	return out
}

// AlertHostConfigInvalid is the alert kind and dedupe key for a host config
// file that no longer loads.
const AlertHostConfigInvalid = "host_config_invalid"

// HostLoader reads host config at each Load, so a changed entry takes effect
// with no restart (KTD30). A file that fails to validate keeps the last good
// config in force and raises an alert. The last good config lives in memory:
// a server start needs a valid file.
type HostLoader struct {
	Path   string
	Alerts Alerter // optional

	mu     sync.Mutex
	good   *HostConfig
	raised bool // an alert this loader raised is open
}

// Load returns the current host config. Only a refusal of the file
// (ErrInvalid) falls back to the last good config. A read error goes to
// the caller.
func (l *HostLoader) Load(ctx context.Context) (*HostConfig, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cfg, err := LoadHost(l.Path)
	if err == nil {
		l.good = cfg
		if l.Alerts != nil && l.raised {
			if err := l.Alerts.Clear(ctx, AlertHostConfigInvalid); err != nil {
				return nil, err
			}
			l.raised = false
		}
		return cfg, nil
	}
	if !errors.Is(err, ErrInvalid) || l.good == nil {
		return nil, err
	}
	if l.Alerts != nil {
		if aerr := l.Alerts.Raise(ctx, Alert{
			Kind: AlertHostConfigInvalid, DedupeKey: AlertHostConfigInvalid,
			Subject: fmt.Sprintf("%s does not load; the last good host config stays in force", HostConfigFile),
			Body:    err.Error(),
		}); aerr != nil {
			return nil, errors.Join(err, aerr)
		}
		l.raised = true
	}
	return l.good, nil
}
