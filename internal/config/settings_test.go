package config

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// memLKG keeps each project's last known good bytes in memory.
type memLKG map[string][2][]byte

func (m memLKG) LoadLKG(_ context.Context, project string) ([]byte, []byte, error) {
	e := m[project]
	return e[0], e[1], nil
}

func (m memLKG) SaveLKG(_ context.Context, project string, config, manifest []byte) error {
	m[project] = [2][]byte{config, manifest}
	return nil
}

const settingsBase = "merge:\n  mechanism: direct\nstages:\n  implementer: {}\n"

// checkedLoader is the server's loader: it checks the stage names against
// host config's.
func checkedLoader(t *testing.T) (*Loader, *alertLog) {
	t.Helper()
	hc, err := ParseHost([]byte(validHost + agentJobsHost))
	if err != nil {
		t.Fatal(err)
	}
	alerts := &alertLog{}
	return &Loader{LKG: memLKG{}, Alerts: alerts, Check: func(pc *ProjectConfig) error {
		return pc.CheckStages(KnownStages(hc))
	}}, alerts
}

// loadAfterGood loads settingsBase, then doc, and returns the second load.
func loadAfterGood(t *testing.T, l *Loader, doc string) (*Loaded, error) {
	t.Helper()
	origin := fakeOrigin{ProjectConfigPath: settingsBase}
	if got, err := l.Load(context.Background(), "alpha", Refs{Read: origin.read}, "main"); err != nil || got.Stale {
		t.Fatalf("first load = %+v, %v", got, err)
	}
	origin[ProjectConfigPath] = doc
	return l.Load(context.Background(), "alpha", Refs{Read: origin.read}, "main")
}

// C1: a name under stages that is no run stage, no per-project native job
// and no agent job of host config makes the config invalid. The load keeps
// the last good config, and the alert names the field. An instance job's
// name gets the reason that it runs once for the host.
func TestC1UnknownStageNameKeepsTheLastGoodConfig(t *testing.T) {
	cases := map[string]struct{ name, reason string }{
		"misspelled stage": {"implementr", "is no run stage, project job or agent job"},
		"github-reactor":   {"github-reactor", "runs once for the host"},
		"overwatch":        {"overwatch", "runs once for the host"},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			l, alerts := checkedLoader(t)
			got, err := loadAfterGood(t, l, settingsBase+"  "+c.name+": {}\n")
			if err != nil {
				t.Fatalf("load = %v, want the last good config", err)
			}
			if !got.Stale || !errors.Is(got.Problem, ErrInvalid) {
				t.Fatalf("load = %+v, want stale with an invalid problem", got)
			}
			if _, ok := got.Config.Stages[c.name]; ok {
				t.Fatalf("the refused config is in force: %+v", got.Config.Stages)
			}
			want := "config: .loop/config.yaml: stages." + c.name + ": " + c.reason
			if len(alerts.raised) != 1 || !strings.Contains(alerts.raised[0], want) ||
				!strings.HasPrefix(alerts.raised[0], AlertProjectConfigInvalid+":alpha: ") {
				t.Fatalf("alerts = %q, want one project_config_invalid that holds %q", alerts.raised, want)
			}
		})
	}
}

// C1: a name of each known kind loads.
func TestC1KnownStageNamesLoad(t *testing.T) {
	for _, name := range []string{"code-reviewer", "fixer", "intake", "merge-handoff", "dependency-advance", "decision-timers", "nightly-qa"} {
		t.Run(name, func(t *testing.T) {
			l, alerts := checkedLoader(t)
			got, err := loadAfterGood(t, l, settingsBase+"  "+name+": {}\n")
			if err != nil || got.Stale {
				t.Fatalf("load = %+v, %v; want %s to load", got, err, name)
			}
			if _, ok := got.Config.Stages[name]; !ok || len(alerts.raised) != 0 {
				t.Fatalf("stages = %+v, alerts = %q", got.Config.Stages, alerts.raised)
			}
		})
	}
}

// C2: a setting value outside its rule makes the config invalid, and the
// error names the field's full path. Each value at 1 loads.
func TestC2SettingOutsideItsRuleNamesTheField(t *testing.T) {
	cases := map[string]struct{ doc, field string }{
		"zero cap":                 {"  implementer: {cap: 0}\n", "stages.implementer.cap"},
		"negative cap":             {"  implementer: {cap: -1}\n", "stages.implementer.cap"},
		"zero runs per tick":       {"  implementer: {runs_per_tick: 0}\n", "stages.implementer.runs_per_tick"},
		"cap on a project job":     {"  merge-handoff: {cap: 1}\n", "stages.merge-handoff.cap"},
		"cap on an agent job":      {"  nightly-qa: {cap: 1}\n", "stages.nightly-qa.cap"},
		"runs per tick on fixer":   {"  fixer: {runs_per_tick: 1}\n", "stages.fixer.runs_per_tick"},
		"runs per tick on intake":  {"  intake: {runs_per_tick: 1}\n", "stages.intake.runs_per_tick"},
		"unknown pool":             {"pools:\n  coders: {cap: 1}\n", "pools.coders"},
		"zero reviewers pool cap":  {"pools:\n  reviewers: {cap: 0}\n", "pools.reviewers.cap"},
		"negative intake cap":      {"  intake: {cap: -1}\n", "stages.intake.cap"},
		"zero runs per tick, item": {"  code-reviewer: {runs_per_tick: 0}\n", "stages.code-reviewer.runs_per_tick"},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			doc := strings.Replace(settingsBase, "  implementer: {}\n", "", 1) + c.doc
			l, alerts := checkedLoader(t)
			got, err := loadAfterGood(t, l, doc)
			if err != nil || !got.Stale {
				t.Fatalf("load = %+v, %v; want the last good config", got, err)
			}
			var ie *InvalidError
			if !errors.As(got.Problem, &ie) || !strings.Contains(got.Problem.Error(), ": "+c.field+": ") {
				t.Fatalf("problem = %v, want a refusal that names %s", got.Problem, c.field)
			}
			if len(alerts.raised) != 1 || !strings.Contains(alerts.raised[0], c.field) {
				t.Fatalf("alerts = %q, want one that names %s", alerts.raised, c.field)
			}
		})
	}
	ok := settingsBase + "  code-reviewer: {cap: 1, runs_per_tick: 1}\n  fixer: {cap: 1}\n  intake: {cap: 1}\n" +
		"pools:\n  reviewers: {cap: 1}\n"
	l, _ := checkedLoader(t)
	got, err := loadAfterGood(t, l, ok)
	if err != nil || got.Stale {
		t.Fatalf("each value at 1 = %+v, %v; want it to load", got, err)
	}
	if c := got.Config.Stages["code-reviewer"]; c.Cap == nil || *c.Cap != 1 || c.RunsPerTick == nil || *c.RunsPerTick != 1 {
		t.Fatalf("code-reviewer = %+v", c)
	}
	if p := got.Config.Pools["reviewers"].Cap; p == nil || *p != 1 {
		t.Fatalf("pools.reviewers.cap = %v", p)
	}
}

// The native jobs split into the jobs each project runs and the jobs that
// run once for the host.
func TestProjectAndInstanceJobsPartitionTheNativeJobs(t *testing.T) {
	got := append(ProjectJobs(), InstanceJobs()...)
	slices.Sort(got)
	want := NativeJobs()
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("ProjectJobs + InstanceJobs = %v, want %v", got, want)
	}
	for _, j := range ProjectJobs() {
		if slices.Contains(InstanceJobs(), j) {
			t.Fatalf("%s is both a project job and an instance job", j)
		}
	}
	if !slices.Equal(ProjectJobs(), []string{"merge-handoff", "dependency-advance", "decision-timers"}) {
		t.Fatalf("ProjectJobs = %v", ProjectJobs())
	}
}

func intp(n int) *int { return &n }

// Effective puts a project's values over host config's. A cap names the
// field that set it, and a run stage's limit counts its project only. An
// agent job's limit counts the whole instance.
func TestEffectiveLimits(t *testing.T) {
	hc, err := ParseHost([]byte(validHost + agentJobsHost))
	if err != nil {
		t.Fatal(err)
	}
	pc := &ProjectConfig{
		Stages: map[string]StageConfig{"code-reviewer": {Cap: intp(3)}},
		Pools:  map[string]PoolConfig{"reviewers": {Cap: intp(2)}},
	}
	got := Effective(hc, pc).Limits("alpha", "code-reviewer")
	want := []Limit{
		{Name: "code-reviewer", Cap: 3, Stages: []string{"code-reviewer"}, Project: "alpha", Field: "stages.code-reviewer.cap"},
		{Name: "reviewers", Cap: 2, Stages: []string{"code-reviewer", "doc-reviewer"}, Project: "alpha", Field: "pools.reviewers.cap"},
	}
	if !slices.EqualFunc(got, want, limitEqual) {
		t.Fatalf("Limits(code-reviewer) = %+v, want %+v", got, want)
	}
	got = Effective(hc, nil).Limits("alpha", "implementer")
	want = []Limit{{Name: "implementer", Cap: 4, Stages: []string{"implementer"}, Project: "alpha", Field: "throughput.implementer.cap"}}
	if !slices.EqualFunc(got, want, limitEqual) {
		t.Fatalf("Limits(implementer) with no project config = %+v, want %+v", got, want)
	}
	got = Effective(hc, pc).Limits("alpha", "nightly-qa")
	want = []Limit{{Name: "nightly-qa", Cap: 1, Stages: []string{"nightly-qa"}, Field: "agent_jobs.nightly-qa.cap"}}
	if !slices.EqualFunc(got, want, limitEqual) {
		t.Fatalf("Limits(nightly-qa) = %+v, want %+v", got, want)
	}
}

func limitEqual(a, b Limit) bool {
	return a.Name == b.Name && a.Cap == b.Cap && slices.Equal(a.Stages, b.Stages) && a.Project == b.Project && a.Field == b.Field
}

// A project's schedule replaces host config's. Runs per tick is the
// project's value, or the stage's cap in force.
func TestEffectiveScheduleAndRunsPerTick(t *testing.T) {
	hc, err := ParseHost([]byte(validHost + agentJobsHost))
	if err != nil {
		t.Fatal(err)
	}
	pc := &ProjectConfig{Stages: map[string]StageConfig{
		"implementer":   {Schedule: "*/2 * * * *", RunsPerTick: intp(3)},
		"code-reviewer": {Cap: intp(5)},
	}}
	e := Effective(hc, pc)
	for _, c := range []struct{ stage, want string }{
		{"implementer", "*/2 * * * *"},
		{"code-reviewer", "*/5 * * * *"},
		{"nightly-qa", "55 2 * * *"},
		{"merge-handoff", "*/5 * * * *"},
	} {
		if got := e.Schedule(c.stage); got != c.want {
			t.Fatalf("Schedule(%s) = %q, want %q", c.stage, got, c.want)
		}
	}
	for _, c := range []struct {
		stage string
		want  int
	}{{"implementer", 3}, {"code-reviewer", 5}, {"reviser", 2}, {"nightly-qa", 1}} {
		if got := e.RunsPerTick(c.stage); got != c.want {
			t.Fatalf("RunsPerTick(%s) = %d, want %d", c.stage, got, c.want)
		}
	}
}

// Settings lists each setting once, sorted by key, with the value in force
// and where it comes from.
func TestEffectiveSettings(t *testing.T) {
	hc, err := ParseHost([]byte(validHost + agentJobsHost))
	if err != nil {
		t.Fatal(err)
	}
	pc, err := ParseProjectConfig([]byte("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n"))
	if err != nil {
		t.Fatal(err)
	}
	defaults := func(stage, field string) (string, bool) {
		if field == "model" {
			return "claude-opus-5-5", true
		}
		return "", false
	}
	rows := Effective(hc, pc).Settings(KnownStages(hc), defaults)
	by := map[string]Setting{}
	var keys []string
	for _, r := range rows {
		k := r.Key.String()
		if _, dup := by[k]; dup {
			t.Fatalf("%s is listed twice", k)
		}
		by[k] = r
		keys = append(keys, k)
	}
	if !slices.IsSorted(keys) {
		t.Fatalf("keys are not sorted: %v", keys)
	}
	for _, c := range []struct {
		key, value string
		source     Source
	}{
		{"stages.implementer.cap", "3", SourceProject},
		{"stages.implementer.enabled", "true", SourceProject},
		{"stages.implementer.model", "claude-opus-5-5", SourceDefault},
		{"stages.code-reviewer.cap", "8", SourceHost},
		{"stages.code-reviewer.enabled", "false", SourceDefault},
		{"stages.intake.cap", "8", SourceHost},
		{"stages.merge-handoff.schedule", "*/5 * * * *", SourceHost},
		{"stages.nightly-qa.schedule", "55 2 * * *", SourceHost},
		{"pools.reviewers.cap", "10", SourceHost},
		{"merge.mechanism", "direct", SourceProject},
		{"run_budget", "", ""},
	} {
		got, ok := by[c.key]
		if c.source == "" {
			if ok {
				t.Fatalf("%s is listed: %+v", c.key, got)
			}
			continue
		}
		if !ok || got.Value != c.value || got.Source != c.source {
			t.Fatalf("%s = %+v, want %q from %s", c.key, got, c.value, c.source)
		}
	}
	for _, k := range []string{"stages.intake.model", "stages.fixer.runs_per_tick", "stages.nightly-qa.cap", "stages.github-reactor.schedule"} {
		if _, ok := by[k]; ok {
			t.Fatalf("%s is listed; its stage kind holds no such field", k)
		}
	}
}

// ParseSettingKey takes only a field that its stage kind may hold.
func TestParseSettingKey(t *testing.T) {
	hc, err := ParseHost([]byte(validHost + agentJobsHost))
	if err != nil {
		t.Fatal(err)
	}
	k := KnownStages(hc)
	for _, s := range []string{"stages.implementer.cap", "stages.fixer.cap", "stages.intake.cap", "stages.implementer.escalated.model",
		"stages.merge-handoff.schedule", "stages.nightly-qa.enabled", "pools.reviewers.cap", "merge.mechanism", "merge.required_checks"} {
		key, err := ParseSettingKey(s, k)
		if err != nil || key.String() != s {
			t.Fatalf("ParseSettingKey(%s) = %v, %v", s, key, err)
		}
	}
	for _, s := range []string{"stages.implementr.cap", "stages.fixer.runs_per_tick", "stages.intake.model", "stages.nightly-qa.cap",
		"stages.overwatch.schedule", "stages.merge-handoff.cap", "pools.coders.cap", "pools.reviewers.schedule", "merge.bors", "tracker.adapter", "stages.implementer"} {
		if _, err := ParseSettingKey(s, k); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), s) {
			t.Fatalf("ParseSettingKey(%s) = %v, want a refusal that names it", s, err)
		}
	}
}
