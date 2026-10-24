package sched

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
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

// C-bug-1: the slug is short lowercase [a-z0-9-], and the run id keeps two
// checkouts of one item and stage apart.
func TestCheckoutNameSlugIsShortAndUnique(t *testing.T) {
	ad := admission.Admitted{Project: "factorio", Stage: machine.StageImplementer, Item: "factorio-kva", RunID: "r-1"}
	title := "Name the work in each session's Orca title, first message line and stage worktree\n\x1b[31m!"
	name := checkoutName(ad, title)
	prefix := "loop-factorio-implementer-factorio-kva-"
	slug, ok := strings.CutPrefix(name, prefix)
	if !ok {
		t.Fatalf("checkout name %q, want prefix %q", name, prefix)
	}
	slug, ok = strings.CutSuffix(slug, "-r-1")
	if !ok {
		t.Fatalf("checkout name %q does not end with the run id", name)
	}
	if !regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`).MatchString(slug) || len(slug) > maxSlug {
		t.Errorf("slug %q is not short lowercase [a-z0-9-]", slug)
	}
	if !strings.HasPrefix(slug, "name-the-work") {
		t.Errorf("slug %q does not follow the title", slug)
	}
	ad.RunID = "r-2"
	if other := checkoutName(ad, title); other == name {
		t.Errorf("two runs of one item share the checkout name %q", name)
	}
	ad.Item = ""
	if got := checkoutName(ad, ""); got != "loop-factorio-implementer-r-2" {
		t.Errorf("a run with no item = %q", got)
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
