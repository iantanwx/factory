package sched

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-1: a stage run's session names its work. The terminal title and
// the first line of the first message carry the stage, the item id and the
// item title. The checkout is named loop-<project>-<stage>-<item>-<slug>.
func TestStageSessionNamesItsWork(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo) // titled "Fix the thing"
	st := w.dispatch(machine.StageImplementer)

	l := w.host.Launches()
	if len(l) != 1 {
		t.Fatalf("launches = %+v", l)
	}
	spec := l[0].Spec
	for _, want := range []string{machine.StageImplementer, id, "Fix the thing"} {
		if !strings.Contains(spec.Title, want) {
			t.Errorf("terminal title %q does not name %q", spec.Title, want)
		}
	}
	argv, err := agenthost.HarnessArgv(spec, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := strings.Cut(argv[len(argv)-1], "\n")
	for _, want := range []string{machine.StageImplementer, id, "Fix the thing"} {
		if !strings.Contains(first, want) {
			t.Errorf("first line of the first message %q does not name %q", first, want)
		}
	}
	if name, want := filepath.Base(st.Checkout), "loop-"+prj+"-"+machine.StageImplementer+"-"+id+"-fix-the-thing"; !strings.HasPrefix(name, want) {
		t.Errorf("checkout name %q, want prefix %q", name, want)
	}
}

// C-bug-1: the coordinator keeps the title loop coordinator.
func TestCoordinatorKeepsItsTitle(t *testing.T) {
	w := newWorld(t)
	w.cfg.Coordinator = &CoordinatorConfig{Project: prj, Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.tick()
	var titles []string
	for _, l := range w.host.Launches() {
		if l.Spec.Kind == agenthost.KindCoordinator {
			titles = append(titles, l.Spec.Title)
		}
	}
	if len(titles) != 1 || titles[0] != "loop coordinator" {
		t.Fatalf("coordinator titles = %q, want [\"loop coordinator\"]", titles)
	}
}
