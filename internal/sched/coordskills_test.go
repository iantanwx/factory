package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
)

// coordinatorPack is impeccablePack with the coordinator's runbook.
func coordinatorPack(version string) brief.Pack {
	p := impeccablePack()
	fsys := p.FS.(fstest.MapFS)
	fsys["loop-coordinator/SKILL.md"] = &fstest.MapFile{Data: []byte("# coordinator runbook " + version + "\n")}
	p.Version = version
	return p
}

// C17: in a project that declares ui, the coordinator's start writes no
// loop-impeccable and lists none in its brief. After a new release, the
// refresh of its live session's checkout clears an untracked
// loop-impeccable that an earlier release wrote.
func TestC17TheCoordinatorHoldsNoConditionalSkill(t *testing.T) {
	ctx := context.Background()
	w := selectWorld(t, uiConfig)
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.cfg.Pack, w.cfg.Release = coordinatorPack("v1"), "v1"
	w.rebuild()
	if err := w.s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	w.tick()
	cs := w.coordinator()
	if cs.Session == nil {
		t.Fatalf("coordinator = %+v", cs)
	}
	impeccable := filepath.Join(cs.Checkout, brief.SkillRoot(agenthost.Claude), "loop-impeccable")
	if _, err := os.Stat(impeccable); err == nil {
		t.Fatal("the coordinator's start wrote loop-impeccable")
	}
	raw, err := os.ReadFile(filepath.Join(cs.Checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(b.Skills, "loop-coordinator") || slices.Contains(b.Skills, "loop-impeccable") || b.DesignSkill != "" {
		t.Fatalf("the coordinator's brief lists %v, design_skill %q; want the kernel without loop-impeccable", b.Skills, b.DesignSkill)
	}

	// An earlier release wrote loop-impeccable into the checkout.
	if err := os.MkdirAll(impeccable, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(impeccable, "SKILL.md"), []byte("# impeccable v0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.cfg.Pack, w.cfg.Release = coordinatorPack("v2"), "v2"
	w.rebuild()
	w.tick()
	if got := w.coordinator(); got.NotifiedRelease != "v2" {
		t.Fatalf("after the release = %+v, want the live session told about v2", got)
	}
	runbook, err := os.ReadFile(filepath.Join(cs.Checkout, brief.SkillRoot(agenthost.Claude), "loop-coordinator", "SKILL.md"))
	if err != nil || string(runbook) != "# coordinator runbook v2\n" {
		t.Fatalf("the refreshed runbook = %q, %v", runbook, err)
	}
	if _, err := os.Stat(impeccable); err == nil {
		t.Fatal("the refresh after a new release left loop-impeccable in the coordinator's checkout")
	}
}
