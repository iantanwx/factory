package settings

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/sched"
)

// C16: for any host config and project config, each cap row equals the cap
// Limits gives admission, each schedule row the expression the scheduler
// fires, each runs_per_tick row the limit launch passes to fill, and each
// enabled row StageEnabled and the firing's Enabled.
func TestC16SettingsReadShowsWhatIsEnforced(t *testing.T) {
	rng := rand.New(rand.NewPCG(16, 16))
	crons := []string{"*/5 * * * *", "0 * * * *", "7,37 * * * *", "15 3 * * *"}
	jobs := sched.Jobs{Project: config.ProjectJobs()}
	for i := range 1000 {
		hc := config.DefaultHost("/var/lib/loop")
		for name, tp := range hc.Throughput {
			tp.Cap = 1 + rng.IntN(9)
			if tp.Schedule != "" {
				tp.Schedule = crons[rng.IntN(len(crons))]
			}
			hc.Throughput[name] = tp
		}
		hc.AgentJobs = map[string]config.AgentJob{}
		for j := range rng.IntN(3) {
			hc.AgentJobs["job"+strconv.Itoa(j)] = config.AgentJob{Cap: 1 + rng.IntN(4), Schedule: crons[rng.IntN(len(crons))], Runbook: "docs/job.md"}
		}
		k := config.KnownStages(hc)
		pc := &config.ProjectConfig{Stages: map[string]config.StageConfig{}, Pools: map[string]config.PoolConfig{}}
		for _, name := range append(append(k.RunStages, k.ProjectJobs...), k.AgentJobs...) {
			if rng.IntN(2) == 0 {
				continue
			}
			kind, _ := config.KindOf(name, k)
			var sc config.StageConfig
			if kind != config.KindIntake && rng.IntN(2) == 0 {
				sc.Schedule = crons[rng.IntN(len(crons))]
			}
			if kind == config.KindItemStage || kind == config.KindFixer || kind == config.KindIntake {
				if rng.IntN(2) == 0 {
					sc.Cap = ptr(1 + rng.IntN(9))
				}
			}
			if kind == config.KindItemStage && rng.IntN(2) == 0 {
				sc.RunsPerTick = ptr(1 + rng.IntN(9))
			}
			if kind != config.KindIntake && kind != config.KindProjectJob && rng.IntN(4) == 0 {
				off := false
				sc.Enabled = &off
			}
			pc.Stages[name] = sc
		}
		if rng.IntN(2) == 0 {
			pc.Pools["reviewers"] = config.PoolConfig{Cap: ptr(1 + rng.IntN(9))}
		}

		t2 := config.Effective(hc, pc)
		firings := map[string]sched.Firing{}
		snap := &config.ProjectSnapshot{Config: pc}
		for _, f := range sched.Firings(hc, []config.Project{{ID: "alpha"}}, func(string) *config.ProjectSnapshot { return snap }, jobs) {
			firings[f.Stage] = f
		}
		for _, r := range rows(hc, pc, nil) {
			section, rest, _ := strings.Cut(r.Key, ".")
			name, field, _ := strings.Cut(rest, ".")
			var want string
			switch {
			case section == "pools":
				want = strconv.Itoa(t2.Limits("alpha", "code-reviewer")[1].Cap)
			case section != "stages":
				continue
			case field == "cap":
				want = strconv.Itoa(t2.Limits("alpha", name)[0].Cap)
			case field == "runs_per_tick":
				want = strconv.Itoa(t2.RunsPerTick(name))
			case field == "schedule":
				want = firings[name].Schedule
			case field == "enabled":
				want = strconv.FormatBool(pc.StageEnabled(name))
				if f, ok := firings[name]; !ok || strconv.FormatBool(f.Enabled) != want {
					t.Fatalf("case %d: %s fires enabled %v, the read says %s", i, name, f.Enabled, want)
				}
			default:
				continue
			}
			if r.Value != want {
				t.Fatalf("case %d: the read says %s = %q, enforcement uses %q\nhost %+v\nproject %+v", i, r.Key, r.Value, want, hc.Throughput, pc.Stages)
			}
		}
	}
}

func ptr(n int) *int { return &n }
