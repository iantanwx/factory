package config

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// PoolConfig sets one pool for the project.
type PoolConfig struct {
	Cap *int `yaml:"cap,omitempty"` // nil: host config's cap applies
}

// Source names where a setting's value in force comes from.
type Source string

// The three sources of a setting.
const (
	SourceProject Source = "project" // .loop/config.yaml on the default branch
	SourceHost    Source = "host"    // host config's throughput entry
	SourceDefault Source = "default" // the loop's own default
)

// StageKind is the kind of a name under stages. Each kind holds its own
// fields (stageFields).
type StageKind int

// The stage kinds.
const (
	KindItemStage  StageKind = iota + 1 // a run stage that claims an item
	KindFixer                           // the run stage that claims no item
	KindBrainstorm                      // the stage whose cap counts epics
	KindProjectJob                      // a native job each project runs
	KindAgentJob                        // an agent job of host config
)

// The fields of a stage entry, in the order Settings lists them.
var (
	agentFields = []string{"enabled", "model", "harness", "effort", "run_budget", "schedule",
		"escalated.model", "escalated.harness", "escalated.effort"}
	stageFields = map[StageKind][]string{
		KindItemStage:  append(slices.Clone(agentFields), "cap", "runs_per_tick"),
		KindFixer:      append(slices.Clone(agentFields), "cap"),
		KindBrainstorm: {"cap"},
		KindProjectJob: {"schedule"},
		KindAgentJob:   agentFields,
	}
	mergeFields = []string{"mechanism", "required_checks"}
)

// Known is the set of stage names a project config may hold under stages.
// KnownStages builds it.
type Known struct {
	RunStages   []string // RunStages()
	ProjectJobs []string // ProjectJobs()
	AgentJobs   []string // host config's agent_jobs
}

// KnownStages returns the names a project config may hold: the run stages,
// the per-project native jobs and host config's agent jobs.
func KnownStages(hc *HostConfig) Known {
	return Known{RunStages: RunStages(), ProjectJobs: ProjectJobs(), AgentJobs: hc.AgentJobNames()}
}

// codeKnown is the part of Known that the code holds alone. A config parse
// reads no host config, so it knows no agent job.
func codeKnown() Known { return Known{RunStages: RunStages(), ProjectJobs: ProjectJobs()} }

// ProjectJobs lists the native jobs that run once for each project. A
// project may set their schedule.
func ProjectJobs() []string {
	return []string{"merge-handoff", "dependency-advance", "decision-timers"}
}

// InstanceJobs lists the native jobs that run once for the host. No
// project sets them.
func InstanceJobs() []string { return []string{"github-reactor", "overwatch", "orca-acceptance"} }

// KindOf returns the kind of name, or false when k holds no such name.
func KindOf(name string, k Known) (StageKind, bool) {
	switch {
	case name == StageFixer:
		return KindFixer, true
	case name == StageBrainstorm:
		return KindBrainstorm, true
	case slices.Contains(k.RunStages, name):
		return KindItemStage, true
	case slices.Contains(k.ProjectJobs, name):
		return KindProjectJob, true
	case slices.Contains(k.AgentJobs, name):
		return KindAgentJob, true
	}
	return 0, false
}

// CheckStages refuses a name under stages that k does not hold, and a
// field that the name's stage kind cannot hold. An instance job's name is
// refused with the reason that it runs for the host. The server calls it
// after each load, because the agent jobs live in host config.
func (c *ProjectConfig) CheckStages(k Known) error {
	ck := &checker{source: ProjectConfigPath}
	for _, name := range slices.Sorted(maps.Keys(c.Stages)) {
		f := "stages." + name
		kind, ok := KindOf(name, k)
		switch {
		case ok:
			checkStageFields(ck, f, kind, c.Stages[name])
		case slices.Contains(InstanceJobs(), name):
			ck.fail(f, "runs once for the host, so no project sets it")
		default:
			ck.fail(f, "is no run stage, project job or agent job of this loop")
		}
	}
	return ck.err()
}

// checkStageFields refuses cap and runs_per_tick on a stage kind that
// cannot hold them, and a value of either below 1. Any older field on any
// kind loads as before, so no working project stops.
func checkStageFields(ck *checker, f string, kind StageKind, s StageConfig) {
	for _, v := range []struct {
		field string
		n     *int
	}{{"cap", s.Cap}, {"runs_per_tick", s.RunsPerTick}} {
		switch {
		case v.n == nil:
		case !slices.Contains(stageFields[kind], v.field):
			ck.fail(f+"."+v.field, "must be absent; this stage kind takes no %s", v.field)
		case *v.n < 1:
			ck.fail(f+"."+v.field, "must be at least 1")
		}
	}
}

// checkPools refuses a pool this loop does not have, and a cap below 1.
func (c *ProjectConfig) checkPools(ck *checker) {
	for _, name := range slices.Sorted(maps.Keys(c.Pools)) {
		f := "pools." + name
		switch cp := c.Pools[name].Cap; {
		case !isPool(name):
			ck.fail(f, "is not a pool of this loop")
		case cp != nil && *cp < 1:
			ck.fail(f+".cap", "must be at least 1")
		}
	}
}

// SettingKey names one setting, such as stages.implementer.cap. Only
// ParseSettingKey and Settings build one, so it always names a field that
// its stage kind may hold.
type SettingKey struct{ section, name, field string }

func (k SettingKey) String() string {
	if k.name == "" {
		return k.section + "." + k.field
	}
	return k.section + "." + k.name + "." + k.field
}

// ParseSettingKey parses s against k. It refuses a key that names no
// setting of this loop.
func ParseSettingKey(s string, k Known) (SettingKey, error) {
	refuse := func(reason string) (SettingKey, error) {
		return SettingKey{}, &InvalidError{Source: "setting", Field: s, Reason: reason}
	}
	section, rest, _ := strings.Cut(s, ".")
	switch section {
	case "merge":
		if !slices.Contains(mergeFields, rest) {
			return refuse("is not a field of merge")
		}
		return SettingKey{section: section, field: rest}, nil
	case "pools":
		name, field, _ := strings.Cut(rest, ".")
		if !isPool(name) || field != "cap" {
			return refuse("is not a pool cap of this loop")
		}
		return SettingKey{section, name, field}, nil
	case "stages":
		name, field, _ := strings.Cut(rest, ".")
		kind, ok := KindOf(name, k)
		if !ok {
			return refuse("names no run stage, project job or agent job of this loop")
		}
		if !slices.Contains(stageFields[kind], field) {
			return refuse("is not a field that this stage kind holds")
		}
		return SettingKey{section, name, field}, nil
	}
	return refuse("is not a setting; a setting is under stages, pools or merge")
}

// Setting is one setting and its value in force.
type Setting struct {
	Key    SettingKey
	Value  string // as `settings set --value` takes it; "" when no value is in force
	Source Source
}

// Defaults gives the loop's own default of a stage field that host config
// does not hold: the model, the effort and the escalated model. sched
// supplies it from DefaultModels, DefaultEfforts and DefaultEscalatedModels.
type Defaults func(stage, field string) (string, bool)

// ProjectThroughput is one project's values in force: host config with the
// project's values over it. It is the only place that merges the two.
type ProjectThroughput struct {
	hc *HostConfig
	pc *ProjectConfig
}

// Effective merges pc over hc. A nil pc gives the host values.
func Effective(hc *HostConfig, pc *ProjectConfig) ProjectThroughput {
	if pc == nil {
		pc = &ProjectConfig{}
	}
	return ProjectThroughput{hc: hc, pc: pc}
}

// Limits returns the stage's own limit, then the limit of each pool that
// holds the stage. A run stage's limits count project's live runs only. An
// agent job's limit counts the whole instance, and no project sets it.
func (t ProjectThroughput) Limits(project, stage string) []Limit {
	out := t.hc.Limits(stage)
	if _, ok := t.hc.AgentJob(stage); ok {
		out[0].Field = "agent_jobs." + stage + ".cap"
		return out
	}
	for i := range out {
		l := &out[i]
		l.Project = project
		l.Field = "throughput." + l.Name + ".cap"
		if cp := t.projectCap(l.Name); cp != nil {
			l.Cap, l.Field = *cp, t.projectField(l.Name)
		}
	}
	return out
}

// projectCap returns the project's cap of a stage or a pool, or nil.
func (t ProjectThroughput) projectCap(name string) *int {
	if isPool(name) {
		return t.pc.Pools[name].Cap
	}
	return t.pc.Stages[name].Cap
}

func (t ProjectThroughput) projectField(name string) string {
	if isPool(name) {
		return "pools." + name + ".cap"
	}
	return "stages." + name + ".cap"
}

// Schedule returns the stage's cron: the project's, else the agent job's
// or host config's.
func (t ProjectThroughput) Schedule(stage string) string {
	v, _ := t.schedule(stage)
	return v
}

func (t ProjectThroughput) schedule(stage string) (string, Source) {
	if s := t.pc.Stages[stage].Schedule; s != "" {
		return s, SourceProject
	}
	if j, ok := t.hc.AgentJob(stage); ok {
		return j.Schedule, SourceHost
	}
	return t.hc.ThroughputFor(stage).Schedule, SourceHost
}

// RunsPerTick returns how many runs one firing of the stage starts at
// most: the project's value, else the stage's cap in force.
func (t ProjectThroughput) RunsPerTick(stage string) int {
	if n := t.pc.Stages[stage].RunsPerTick; n != nil {
		return *n
	}
	if ls := t.Limits("", stage); len(ls) > 0 {
		return ls[0].Cap
	}
	return 0
}

// Settings lists every setting of the project with its value in force and
// its source, sorted by key.
func (t ProjectThroughput) Settings(k Known, d Defaults) []Setting {
	var out []Setting
	add := func(key SettingKey, v string, src Source) {
		out = append(out, Setting{Key: key, Value: v, Source: src})
	}
	for _, name := range slices.Concat(k.RunStages, k.ProjectJobs, k.AgentJobs) {
		kind, _ := KindOf(name, k)
		for _, field := range stageFields[kind] {
			v, src := t.stageField(name, field, d)
			add(SettingKey{"stages", name, field}, v, src)
		}
	}
	for _, p := range pools {
		v, src := strconv.Itoa(t.hc.ThroughputFor(p.name).Cap), SourceHost
		if cp := t.pc.Pools[p.name].Cap; cp != nil {
			v, src = strconv.Itoa(*cp), SourceProject
		}
		add(SettingKey{section: "pools", name: p.name, field: "cap"}, v, src)
	}
	mech := SourceProject
	if t.pc.Merge.Mechanism == "" {
		mech = SourceDefault
	}
	add(SettingKey{section: "merge", field: "mechanism"}, string(t.pc.Merge.Mechanism), mech)
	checks := SourceProject
	if len(t.pc.Merge.RequiredChecks) == 0 {
		checks = SourceDefault
	}
	add(SettingKey{section: "merge", field: "required_checks"}, strings.Join(t.pc.Merge.RequiredChecks, ","), checks)
	slices.SortFunc(out, func(a, b Setting) int { return strings.Compare(a.Key.String(), b.Key.String()) })
	return out
}

// stageField returns one field of a stage entry in force, and its source.
func (t ProjectThroughput) stageField(stage, field string, d Defaults) (string, Source) {
	s, listed := t.pc.Stages[stage]
	switch field {
	case "enabled":
		if !listed {
			return "false", SourceDefault
		}
		return strconv.FormatBool(t.pc.StageEnabled(stage)), SourceProject
	case "schedule":
		return t.schedule(stage)
	case "cap":
		l := t.Limits("", stage)[0]
		if strings.HasPrefix(l.Field, "stages.") {
			return strconv.Itoa(l.Cap), SourceProject
		}
		return strconv.Itoa(l.Cap), SourceHost
	case "runs_per_tick":
		if s.RunsPerTick != nil {
			return strconv.Itoa(*s.RunsPerTick), SourceProject
		}
		return strconv.Itoa(t.RunsPerTick(stage)), SourceDefault
	case "run_budget":
		switch {
		case s.RunBudget != 0:
			return s.RunBudget.String(), SourceProject
		case t.hc.RunBudget != 0:
			return t.hc.RunBudget.String(), SourceHost
		}
	}
	e := s.Escalated
	if e == nil {
		e = &EscalatedProfile{}
	}
	v := map[string]string{
		"model": s.Model, "harness": s.Harness, "effort": s.Effort,
		"escalated.model": e.Model, "escalated.harness": e.Harness, "escalated.effort": e.Effort,
	}[field]
	if v != "" {
		return v, SourceProject
	}
	if d != nil {
		if v, ok := d(stage, field); ok {
			return v, SourceDefault
		}
	}
	return "", SourceDefault
}

// StageCheck returns the Loader.Check of a server: CheckStages against the
// names of the host config that host loads.
func StageCheck(host *HostLoader) func(*ProjectConfig) error {
	return func(pc *ProjectConfig) error {
		hc, err := host.Load(context.Background())
		if err != nil {
			return fmt.Errorf("config: load host config for the stage check: %w", err)
		}
		return pc.CheckStages(KnownStages(hc))
	}
}
