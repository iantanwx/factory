package sched

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// C10: a project's job schedule fires on that schedule, and another
// project keeps the host schedule. Timers gives each project its
// expression, and over one simulated hour each project's merge-handoff
// fires on its own minutes.
func TestC10AProjectJobScheduleFiresOnThatScheduleAndAnotherKeepsTheHosts(t *testing.T) {
	const hostExpr, projectExpr = "*/15 * * * *", "7,37 * * * *"
	w := newWorld(t)
	w.writeHost(nil, map[string]string{"merge-handoff": hostExpr})
	p := snapshot(t, "stages:\n  merge-handoff: {schedule: \""+projectExpr+"\"}\nmerge:\n  mechanism: direct\n", "")
	q := snapshot(t, "merge:\n  mechanism: direct\n", "")
	projects := []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}, {ID: "beta", Repo: "iantanwx/beta", Path: "/srv/beta"}}
	snap := func(project string) *config.ProjectSnapshot {
		if project == "beta" {
			return q
		}
		return p
	}
	w.cfg.Projects = func() ([]config.Project, error) { return projects, nil }
	w.cfg.ProjectConfig = func(_ context.Context, project string) (*config.ProjectSnapshot, error) { return snap(project), nil }
	w.rebuild()
	var mu sync.Mutex
	fired := map[string][]int{}
	if err := w.s.RegisterJob("merge-handoff", func(_ context.Context, project string) error {
		mu.Lock()
		defer mu.Unlock()
		fired[project] = append(fired[project], w.clock.Now().Minute())
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	hc, err := w.cfg.Host.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exprs := map[string]string{}
	for _, tm := range Timers(hc, projects, snap, w.s.Jobs()) {
		if tm.Stage == "merge-handoff" {
			exprs[tm.Project] = tm.Expr
		}
	}
	if exprs[prj] != projectExpr || exprs["beta"] != hostExpr {
		t.Fatalf("Timers gives merge-handoff %q in %s and %q in beta, want %q and %q", exprs[prj], prj, exprs["beta"], projectExpr, hostExpr)
	}

	start := at(t, "2026-10-21T10:00:30Z")
	for m := range 61 {
		w.clock.Set(start.Add(time.Duration(m) * time.Minute))
		w.tick()
	}
	if want := []int{7, 37}; !slices.Equal(fired[prj], want) {
		t.Fatalf("%s's merge-handoff fired at minutes %v, want %v", prj, fired[prj], want)
	}
	if want := []int{15, 30, 45, 0}; !slices.Equal(fired["beta"], want) {
		t.Fatalf("beta's merge-handoff fired at minutes %v, want %v", fired["beta"], want)
	}
}
