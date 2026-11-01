package sched

import (
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/userspace"
)

// acmeTree is the tree of the acme-design pin.
var acmeTree = fstest.MapFS{"SKILL.md": {Data: []byte("---\nname: acme-design\n---\n"), Mode: 0o644}}

// overrideWorld is selectWorld in a project whose ui block names the pin
// acme-design its design skill. The store holds the pin when stored.
func overrideWorld(t *testing.T, stored bool) *world {
	t.Helper()
	sum, err := pack.TreeHash(acmeTree)
	if err != nil {
		t.Fatal(err)
	}
	pin := config.SkillPin{Name: "acme-design", Repo: "acme/skills",
		Commit: "0123456789abcdef0123456789abcdef01234567", SHA256: sum}
	cfg := uiConfig + "  design_skill: acme-design\nskills:\n  - name: acme-design\n    repo: " + pin.Repo +
		"\n    commit: " + pin.Commit + "\n    path: \"\"\n    sha256: " + pin.SHA256 + "\n"
	w := selectWorld(t, cfg)
	w.cfg.SkillStore = userspace.Store{Dir: filepath.Join(t.TempDir(), "userspace-skills")}
	if stored {
		if err := w.cfg.SkillStore.Put(prj, pin, acmeTree); err != nil {
			t.Fatal(err)
		}
	}
	w.rebuild()
	return w
}

// assertDesign fails the test unless the run holds no loop-impeccable,
// holds the acme-design directory, and reads design as its design skill.
func assertDesign(t *testing.T, what string, st *Started, design string) {
	t.Helper()
	b, _, dirs := held(t, st)
	if slices.Contains(dirs, "loop-impeccable") || slices.Contains(b.Skills, "loop-impeccable") {
		t.Fatalf("%s holds %v, lists %v; want no loop-impeccable", what, dirs, b.Skills)
	}
	if !slices.Contains(dirs, "acme-design") || b.DesignSkill != design {
		t.Fatalf("%s holds %v, design_skill %q; want acme-design held and design_skill %q", what, dirs, b.DesignSkill, design)
	}
}

// C20: with design_skill acme-design and the pin held, an epic run and a
// ticket run that C14 gives loop-impeccable hold acme-design in its place
// and name it. A ticket run that C14 gives no skill holds acme-design and
// names no design skill.
func TestC20TheOverrideReplacesLoopImpeccable(t *testing.T) {
	w := overrideWorld(t, true)
	w.specEpic()
	assertDesign(t, "a planner run on an epic", w.dispatch(machine.StagePlanner), "acme-design")
	w.pathTicket("web/src/App.tsx")
	assertDesign(t, "a ticket run on web/src/App.tsx", w.dispatch(machine.StageImplementer), "acme-design")
	w.pathTicket("internal/x.go")
	assertDesign(t, "a ticket run on internal/x.go", w.dispatch(machine.StageImplementer), "")
}

// C22: when the store does not hold the acme-design pin, the runs of C20
// hold no loop-impeccable and name no design skill.
func TestC22AnOverrideTheRunDoesNotHoldNamesNoDesignSkill(t *testing.T) {
	w := overrideWorld(t, false)
	w.specEpic()
	assertNoImpeccable(t, "a planner run on an epic", w.dispatch(machine.StagePlanner))
	w.pathTicket("web/src/App.tsx")
	assertNoImpeccable(t, "a ticket run on web/src/App.tsx", w.dispatch(machine.StageImplementer))
}
