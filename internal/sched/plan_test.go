package sched

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// listedProject lists the implementer with its own schedule, the
// code-reviewer, the agent job nightly-qa, and the doc-reviewer disabled.
const listedProject = "stages:\n  implementer:\n    schedule: \"*/5 * * * *\"\n  code-reviewer: {}\n  nightly-qa: {}\n" +
	"  doc-reviewer:\n    enabled: false\nmerge:\n  mechanism: direct\n"

func (w *world) stageList(project string) []StageRow {
	w.t.Helper()
	rows, err := w.s.StageList(context.Background(), project)
	if err != nil {
		w.t.Fatal(err)
	}
	return rows
}

func rowOf(t *testing.T, rows []StageRow, project, stage string) StageRow {
	t.Helper()
	for _, r := range rows {
		if r.Project == project && r.Stage == stage {
			return r
		}
	}
	t.Fatalf("stage list holds no row %s/%s", project, stage)
	return StageRow{}
}

// C11: stage list prints one row for each firing of the host, enabled or
// not: the instance job once with no project, then each run stage, each
// agent job and each registered native job of the project. Each row
// prints the values the scheduler and admission run it on.
func TestC11StageListPrintsEachFiringWithItsEffectiveValues(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	// A codex login gives each stage a fallback profile after its own.
	w.writeHost(nil, nil, nightlyQA, func(c *config.HostConfig) {
		c.Harnesses = []config.HarnessLogin{{Name: "claude"}, {Name: "codex"}}
	})
	w.project.set(snapshot(t, listedProject, ""))
	w.rebuild()
	if err := w.s.RegisterJob("merge-handoff", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := w.s.RegisterInstanceJob("github-reactor", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	w.ticket(machine.Todo)
	w.clock.Set(at(t, "2026-10-21T01:00:00Z"))
	w.tick() // the first sight of the override sets its deadline after now
	fired := at(t, "2026-10-21T01:05:30Z")
	w.clock.Set(fired)
	w.tick() // the implementer fires and starts one run
	hold := admission.StagePause{Project: prj, Stage: machine.StageImplementer, Reason: "hold for the upgrade", By: "coordinator:c-1"}
	for _, p := range []admission.StagePause{hold, {Project: "beta", Stage: machine.StageImplementer, Reason: "another project", By: "coordinator:c-1"}} {
		if _, err := admission.Pause(ctx, w.st, p); err != nil {
			t.Fatal(err)
		}
	}

	rows := w.stageList("")
	var got []Firing
	for _, r := range rows {
		got = append(got, r.Firing)
	}
	never := "0 0 1 1 *"
	want := []Firing{
		{Project: "", Stage: "github-reactor", Kind: FiringInstanceJob, Enabled: true, Schedule: never},
		{Project: prj, Stage: "implementer", Kind: FiringRun, Enabled: true, Schedule: "*/5 * * * *"},
		{Project: prj, Stage: "code-reviewer", Kind: FiringRun, Enabled: true, Schedule: never},
		{Project: prj, Stage: "doc-reviewer", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "amender", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "unattended-brainstorm", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "spec-author", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "decomposer", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "fixer", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "reproducer", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "retro", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "brainstorm", Kind: FiringRun, Enabled: false, Schedule: never},
		{Project: prj, Stage: "nightly-qa", Kind: FiringAgentJob, Enabled: true, Schedule: "55 2 * * *"},
		{Project: prj, Stage: "merge-handoff", Kind: FiringJob, Enabled: true, Schedule: never},
		{Project: prj, Stage: "dependency-advance", Kind: FiringJob, Enabled: true, Schedule: never},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("firings =\n%+v\nwant\n%+v", got, want)
	}

	impl := rowOf(t, rows, prj, machine.StageImplementer)
	hold.At = impl.Pauses[0].At
	next := at(t, "2026-10-21T01:10:00Z")
	wantImpl := StageRow{
		Firing: want[1],
		Limits: []config.Limit{{Name: "implementer", Cap: 8, Stages: []string{"implementer"}}},
		Profiles: []Profile{
			{Harness: agenthost.Claude, Model: "claude-opus-5-5", Effort: "high"},
			{Harness: agenthost.Codex, Model: "gpt-5-codex", Effort: "high"},
		},
		Escalated: &Profile{Harness: agenthost.Claude, Model: "claude-opus-5-5", Effort: "high", Escalated: true},
		Pauses:    []admission.StagePause{hold},
		LastFire:  &fired,
		NextFire:  &next,
		Live:      1,
	}
	if !reflect.DeepEqual(impl, wantImpl) {
		t.Fatalf("implementer row =\n%+v\nwant\n%+v", impl, wantImpl)
	}

	cr := rowOf(t, rows, prj, machine.StageCodeReviewer)
	if want := []config.Limit{{Name: "code-reviewer", Cap: 8, Stages: []string{"code-reviewer"}},
		{Name: "reviewers", Cap: 10, Stages: []string{"code-reviewer", "doc-reviewer"}}}; !reflect.DeepEqual(cr.Limits, want) {
		t.Fatalf("code-reviewer limits = %+v, want its own cap and the reviewers pool", cr.Limits)
	}
	if cr.Escalated != nil || cr.Pauses != nil || cr.Live != 0 || cr.LastFire != nil || cr.NextFire == nil {
		t.Fatalf("code-reviewer row = %+v", cr)
	}

	qa := rowOf(t, rows, prj, "nightly-qa")
	if want := []config.Limit{{Name: "nightly-qa", Cap: 1, Stages: []string{"nightly-qa"}}}; !reflect.DeepEqual(qa.Limits, want) ||
		!reflect.DeepEqual(qa.Profiles, []Profile{{Harness: agenthost.Claude, Model: "claude-opus-5-5"}, {Harness: agenthost.Codex, Model: "gpt-5-codex"}}) {
		t.Fatalf("nightly-qa row = %+v", qa)
	}

	for _, stage := range []string{"merge-handoff", "dependency-advance"} {
		j := rowOf(t, rows, prj, stage)
		if j.Profiles != nil || j.Escalated != nil || j.Pauses != nil || j.Error != "" ||
			!reflect.DeepEqual(j.Limits, []config.Limit{{Name: stage, Cap: 1, Stages: []string{stage}}}) {
			t.Fatalf("%s row = %+v", stage, j)
		}
	}

	if only := w.stageList(prj); len(only) != len(rows)-1 || only[0].Stage != machine.StageImplementer {
		t.Fatalf("stage list --project %s = %+v, want every row but the instance job's", prj, only)
	}
}

// C11: a project whose config does not read prints its native jobs as the
// scheduler fires them, and the error on each run-stage row.
func TestC11StageListOfAProjectWhoseConfigDoesNotRead(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, nightlyQA)
	w.project.failWith(errors.New("stages.implementer.schedule: must be a nonempty schedule"))

	rows := w.stageList(prj)
	if len(rows) != 13 {
		t.Fatalf("%d rows, want 11 run stages, 1 agent job and 1 native job: %+v", len(rows), rows)
	}
	for _, r := range rows {
		switch r.Kind {
		case FiringRun, FiringAgentJob:
			if r.Enabled || r.Error != "stages.implementer.schedule: must be a nonempty schedule" || r.Profiles != nil || r.Escalated != nil {
				t.Fatalf("run-stage row = %+v", r)
			}
		case FiringJob:
			if !r.Enabled || r.Error != "" || r.Stage != DependencyAdvanceJob {
				t.Fatalf("native job row = %+v", r)
			}
		default:
			t.Fatalf("row of kind %s with --project", r.Kind)
		}
	}
}

// C12: the schedule of each enabled row is the cron that fireDue writes on
// its next tick: host config's for the code-reviewer, the agent job's own
// for nightly-qa, and the project's override for the implementer. A row
// that is not enabled gets no cron row.
func TestC12StageListPrintsTheScheduleTheSchedulerFiresOn(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, map[string]string{machine.StageCodeReviewer: "7 * * * *"}, nightlyQA)
	w.project.set(snapshot(t, listedProject, ""))
	w.rebuild()
	w.tick()

	want := map[string]string{
		machine.StageImplementer:  "*/5 * * * *",
		machine.StageCodeReviewer: "7 * * * *",
		"nightly-qa":              "55 2 * * *",
	}
	enabled := 0
	for _, r := range w.stageList(prj) {
		crons := w.count(`SELECT count(*) FROM schedule WHERE kind = 'cron' AND project = ? AND stage = ?`, r.Project, r.Stage)
		if !r.Enabled {
			if crons != 0 {
				t.Fatalf("%s is not enabled, and holds %d cron rows", r.Stage, crons)
			}
			continue
		}
		enabled++
		cron := w.str(`SELECT cron_expr FROM schedule WHERE kind = 'cron' AND project = ? AND stage = ?`, r.Project, r.Stage)
		if r.Schedule != cron {
			t.Fatalf("%s prints %q, and the scheduler fires on %q", r.Stage, r.Schedule, cron)
		}
		if s, ok := want[r.Stage]; ok && r.Schedule != s {
			t.Fatalf("%s prints %q, want %q", r.Stage, r.Schedule, s)
		}
		delete(want, r.Stage)
	}
	if len(want) != 0 || enabled != 4 {
		t.Fatalf("rows not enabled: %v; %d enabled rows, want 4 with dependency-advance", want, enabled)
	}
}
