package admission

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

const other = "beta"

func capOf(n int) *int { return &n }

// stageCaps is a project config that sets the caps of stages.
func stageCaps(caps map[string]int) *config.ProjectConfig {
	pc := &config.ProjectConfig{Stages: map[string]config.StageConfig{}}
	for stage, n := range caps {
		pc.Stages[stage] = config.StageConfig{Cap: capOf(n)}
	}
	return pc
}

func reviewersCap(n int) *config.ProjectConfig {
	return &config.ProjectConfig{Pools: map[string]config.PoolConfig{"reviewers": {Cap: capOf(n)}}}
}

func wantHint(t *testing.T, ref *cli.Refusal, want string) {
	t.Helper()
	wantRefusal(t, ref, cli.CodeCapFull)
	if !strings.Contains(ref.Hint, want) {
		t.Fatalf("hint %q does not name %q", ref.Hint, want)
	}
}

// C3: with stages.implementer.cap: 3 and a host cap of 8, admission admits
// 3 implementer runs of the project and refuses the fourth.
func TestC3ProjectCapBoundsTheProjectsLiveRuns(t *testing.T) {
	w := newWorld(t)
	w.configs.set(prj, stageCaps(map[string]int{machine.StageImplementer: 3}))
	for range 3 {
		w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	}
	_, ref := w.admit(machine.StageImplementer, w.ticket(machine.Todo))
	wantRefusal(t, ref, cli.CodeCapFull)
	if c, r := w.a.Peek(context.Background(), prj, machine.StageImplementer); c != nil || r != nil {
		t.Fatalf("peek at the project cap = %+v %+v, want nothing", c, r)
	}
}

// C5: with a host implementer cap of 2 and no project cap, a project admits
// 2 implementer runs while another project holds 2. A project with
// stages.implementer.cap: 1 beside them holds 1.
func TestC5HostCapIsEachProjectsOwnCap(t *testing.T) {
	w := newWorld(t)
	w.addProject(other)
	w.addProject("gamma")
	w.setCaps(map[string]int{machine.StageImplementer: 2})
	w.configs.set("gamma", stageCaps(map[string]int{machine.StageImplementer: 1}))
	for range 2 {
		if _, ref := w.admitIn(other, machine.StageImplementer, w.ticket(machine.Todo)); ref != nil {
			t.Fatalf("admit in %s: %+v", other, ref)
		}
	}
	first := w.ticket(machine.Todo)
	if c, r := w.a.Peek(context.Background(), prj, machine.StageImplementer); c == nil || c.Item != first || r != nil {
		t.Fatalf("peek in %s while %s holds 2 = %+v %+v, want %s", prj, other, c, r, first)
	}
	w.mustAdmit(machine.StageImplementer, first)
	w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	_, ref := w.admit(machine.StageImplementer, w.ticket(machine.Todo))
	wantRefusal(t, ref, cli.CodeCapFull)
	_, ref = w.admitIn(other, machine.StageImplementer, w.ticket(machine.Todo))
	wantRefusal(t, ref, cli.CodeCapFull)

	if _, ref := w.admitIn("gamma", machine.StageImplementer, w.ticket(machine.Todo)); ref != nil {
		t.Fatalf("admit in gamma: %+v", ref)
	}
	_, ref = w.admitIn("gamma", machine.StageImplementer, w.ticket(machine.Todo))
	wantRefusal(t, ref, cli.CodeCapFull)
	if n := w.count(`SELECT count(*) FROM runs WHERE ended_at IS NULL AND stage = ?`, machine.StageImplementer); n != 5 {
		t.Fatalf("live implementer runs = %d, want 2 + 2 + 1", n)
	}
}

// C6: with pools.reviewers.cap: 2, one live code-reviewer run and one live
// doc-reviewer run fill the pool, and the hint names the pool's field. Two
// live reviewer runs of another project change nothing.
func TestC6ProjectReviewersPoolBoundsBothReviewerStages(t *testing.T) {
	w := newWorld(t)
	w.addProject(other)
	w.configs.set(prj, reviewersCap(2))
	w.configs.set(other, reviewersCap(2))
	if _, ref := w.admitIn(other, machine.StageCodeReviewer, w.ticket(machine.InReview)); ref != nil {
		t.Fatalf("admit in %s: %+v", other, ref)
	}
	if _, ref := w.admitIn(other, machine.StageDocReviewer, w.put(machine.KindEpic, machine.PRDReview, "prd")); ref != nil {
		t.Fatalf("admit in %s: %+v", other, ref)
	}
	w.mustAdmit(machine.StageCodeReviewer, w.ticket(machine.InReview))
	w.mustAdmit(machine.StageDocReviewer, w.put(machine.KindEpic, machine.PRDReview, "prd"))
	_, ref := w.admit(machine.StageCodeReviewer, w.ticket(machine.InReview))
	wantHint(t, ref, "pools.reviewers.cap")
	_, ref = w.admit(machine.StageDocReviewer, w.put(machine.KindEpic, machine.SpecReview, "spec"))
	wantHint(t, ref, "pools.reviewers.cap")
}

// C7: with a host intake cap of 8, stages.intake.cap: 1 and one epic in
// prd_review, intake is refused. With the epic in prd_signoff, it opens.
func TestC7ProjectIntakeCapReplacesTheHostValue(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.configs.set(prj, stageCaps(map[string]int{config.StageIntake: 1}))
	epic := w.put(machine.KindEpic, machine.PRDReview, "epic")
	wantHint(t, w.a.IntakeCap(ctx, prj), "stages.intake.cap in "+config.ProjectConfigPath)
	if _, err := w.bx.Update(ctx, epic, beadsx.UpdateSpec{IfStatus: machine.PRDReview, Status: machine.PRDSignoff}); err != nil {
		t.Fatal(err)
	}
	if ref := w.a.IntakeCap(ctx, prj); ref != nil {
		t.Fatalf("no counted epic under a cap of 1: %+v", ref)
	}
}

// C8: with 5 live implementer runs, a change from cap 5 to cap 2 ends none
// of them, and admission refuses every implementer run until 1 is live.
func TestC8LoweredCapStopsNoLiveRun(t *testing.T) {
	w := newWorld(t)
	w.configs.set(prj, stageCaps(map[string]int{machine.StageImplementer: 5}))
	var live []Admitted
	for range 5 {
		live = append(live, w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo)))
	}
	w.configs.set(prj, stageCaps(map[string]int{machine.StageImplementer: 2}))
	for len(live) > 1 {
		_, ref := w.admit(machine.StageImplementer, w.ticket(machine.Todo))
		wantRefusal(t, ref, cli.CodeCapFull)
		if n := w.count(`SELECT count(*) FROM runs WHERE ended_at IS NULL`); n != len(live) {
			t.Fatalf("live runs = %d, want %d: the lowered cap ended a run", n, len(live))
		}
		w.settle(live[0], Settlement{Finish: "done"})
		live = live[1:]
	}
	w.mustAdmit(machine.StageImplementer, w.ticket(machine.Todo))
	_, ref := w.admit(machine.StageImplementer, w.ticket(machine.Todo))
	wantRefusal(t, ref, cli.CodeCapFull)
}

// C9: a CAP_FULL hint names the config field that set the cap, for a stage
// cap and a pool cap from each source.
func TestC9CapFullHintNamesTheField(t *testing.T) {
	project := " in " + config.ProjectConfigPath
	cases := []struct {
		name  string
		host  map[string]int
		pc    *config.ProjectConfig
		stage string
		item  func(w *world) string
		want  string
	}{
		{"project stage cap", nil, stageCaps(map[string]int{machine.StageImplementer: 1}), machine.StageImplementer,
			func(w *world) string { return w.ticket(machine.Todo) }, "stages.implementer.cap" + project},
		{"host stage cap", map[string]int{machine.StageImplementer: 1}, nil, machine.StageImplementer,
			func(w *world) string { return w.ticket(machine.Todo) }, "throughput.implementer.cap in host config"},
		{"project pool cap", nil, reviewersCap(1), machine.StageCodeReviewer,
			func(w *world) string { return w.ticket(machine.InReview) }, "pools.reviewers.cap" + project},
		{"host pool cap", map[string]int{"reviewers": 1}, nil, machine.StageCodeReviewer,
			func(w *world) string { return w.ticket(machine.InReview) }, "throughput.reviewers.cap in host config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.setCaps(c.host)
			if c.pc != nil {
				w.configs.set(prj, c.pc)
			}
			w.mustAdmit(c.stage, c.item(w))
			_, ref := w.admit(c.stage, c.item(w))
			wantHint(t, ref, c.want)
		})
	}
	t.Run("host intake cap", func(t *testing.T) {
		w := newWorld(t)
		w.setCaps(map[string]int{config.StageIntake: 1})
		w.put(machine.KindEpic, machine.PRDReview, "epic")
		wantHint(t, w.a.IntakeCap(context.Background(), prj), "throughput.intake.cap in host config")
	})
}

// C4: for any live runs spread over two projects, and any stage and
// reviewers pool caps on each, admission of a run stage in a project
// refuses with CAP_FULL exactly when that project's own live runs of a
// limit's stages reach its cap in force. The model counts by hand.
func TestC4OneProjectsRunsNeverCountAgainstAnother(t *testing.T) {
	w := newWorld(t)
	w.addProject(other)
	projects := []string{prj, other}
	stages := []string{machine.StageImplementer, machine.StageCodeReviewer, machine.StageDocReviewer}
	capped := append(slices.Clone(stages), "reviewers")
	item := map[string]func() string{
		machine.StageImplementer:  func() string { return w.ticket(machine.Todo) },
		machine.StageCodeReviewer: func() string { return w.ticket(machine.InReview) },
		machine.StageDocReviewer:  func() string { return w.put(machine.KindEpic, machine.PRDReview, "prd") },
	}
	rng := rand.New(rand.NewPCG(4, 4))
	for i := range 1000 {
		host := map[string]int{}
		for _, k := range capped {
			host[k] = 1 + rng.IntN(4)
		}
		inForce := map[string]map[string]int{}
		for _, p := range projects {
			inForce[p] = maps.Clone(host)
			pc := &config.ProjectConfig{Stages: map[string]config.StageConfig{}, Pools: map[string]config.PoolConfig{}}
			for _, k := range capped {
				if rng.IntN(2) == 0 {
					continue
				}
				n := 1 + rng.IntN(4)
				inForce[p][k] = n
				if k == "reviewers" {
					pc.Pools[k] = config.PoolConfig{Cap: capOf(n)}
				} else {
					pc.Stages[k] = config.StageConfig{Cap: capOf(n)}
				}
			}
			w.configs.set(p, pc)
		}
		w.setCaps(host)
		live := map[string]map[string]int{}
		var rows [][2]string
		for _, p := range projects {
			live[p] = map[string]int{}
			for _, s := range stages {
				n := rng.IntN(5)
				live[p][s] = n
				for range n {
					rows = append(rows, [2]string{p, s})
				}
			}
		}
		w.liveRuns(i, rows)

		p, s := projects[rng.IntN(len(projects))], stages[rng.IntN(len(stages))]
		full := live[p][s] >= inForce[p][s]
		if s != machine.StageImplementer {
			full = full || live[p][machine.StageCodeReviewer]+live[p][machine.StageDocReviewer] >= inForce[p]["reviewers"]
		}
		_, ref := w.admitIn(p, s, item[s]())
		switch {
		case full && (ref == nil || ref.Code != cli.CodeCapFull):
			t.Fatalf("case %d: %s in %s with live %v and caps %v: got %+v, want CAP_FULL", i, s, p, live, inForce, ref)
		case !full && ref != nil:
			t.Fatalf("case %d: %s in %s with live %v and caps %v: got %+v, want an admission", i, s, p, live, inForce, ref)
		}
	}
}

// liveRuns replaces every run with one live run for each project and stage
// in rows.
func (w *world) liveRuns(round int, rows [][2]string) {
	w.t.Helper()
	err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.Exec(`DELETE FROM runs`); err != nil {
			return err
		}
		for j, r := range rows {
			if _, err := tx.Exec(`INSERT INTO runs (run_id, project, stage, state, started_at) VALUES (?, ?, ?, 'running', 1)`,
				fmt.Sprintf("live-%d-%d", round, j), r[0], r[1]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
}

// An agent job's cap still counts the whole instance: a run of the job in
// one project fills its cap of 1 for every project, whatever cap a
// project lists for it.
func TestAgentJobCapStillCountsTheWholeInstance(t *testing.T) {
	w := newWorld(t)
	w.addProject(other)
	cfg := config.DefaultHost("/var/lib/loop")
	cfg.AgentJobs = map[string]config.AgentJob{"daily": {Cap: 1, Schedule: "0 9 * * *", Runbook: "docs/daily.md"}}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.hostPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	w.configs.set(prj, stageCaps(map[string]int{"daily": 2}))
	if _, ref := w.admitIn(other, "daily", ""); ref != nil {
		t.Fatalf("admit daily in %s: %+v", other, ref)
	}
	_, ref := w.admitIn(prj, "daily", "")
	wantHint(t, ref, "agent_jobs.daily.cap in host config")
}
