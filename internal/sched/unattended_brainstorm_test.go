package sched

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
)

// C14, C10: the scheduler starts an unattended brainstorm only where the
// project enables it. A tick on a project whose config lists
// unattended-brainstorm starts one run on the epic in prd_needed, with that
// epic and its request in the brief, and the brief names the runbook
// loop-unattended-brainstorm. A project whose config does not list it
// starts none.
func TestUnattendedBrainstormStartsOnlyWhereTheProjectEnablesIt(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.writeHost(nil, map[string]string{"unattended-brainstorm": "*/5 * * * *"})
	if err := config.StoreProjectRow(w.st)(ctx, config.Project{ID: "beta", Repo: "iantanwx/beta", Path: "/srv/beta"}); err != nil {
		t.Fatal(err)
	}
	alphaCfg := snapshot(t, "stages:\n  unattended-brainstorm: {}\nmerge:\n  mechanism: direct\n", "")
	betaCfg := snapshot(t, "stages:\n  implementer: {}\nmerge:\n  mechanism: direct\n", "")
	w.cfg.Projects = func() ([]config.Project, error) {
		return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}, {ID: "beta", Repo: "iantanwx/beta", Path: "/srv/beta"}}, nil
	}
	w.cfg.ProjectConfig = func(_ context.Context, p string) (*config.ProjectSnapshot, error) {
		if p == "beta" {
			return betaCfg, nil
		}
		return alphaCfg, nil
	}
	w.rebuild()
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "Let a reader save a search", Description: "From the operator.", Type: "epic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: "prd_needed",
		SetMetadata: map[string]string{"loop.request": "let me save a search"}}); err != nil {
		t.Fatal(err)
	}

	w.clock.Set(at(t, "2026-10-21T10:00:30Z"))
	w.tick() // the first sight sets each deadline after now
	w.clock.Set(at(t, "2026-10-21T10:05:30Z"))
	w.tick()

	if n := w.count(`SELECT count(*) FROM runs WHERE project = 'beta'`); n != 0 {
		t.Fatalf("%d runs started in a project that does not list unattended-brainstorm", n)
	}
	l := w.host.Launches()
	if len(l) != 1 {
		t.Fatalf("%d runs started, want one unattended-brainstorm run", len(l))
	}
	if got := w.str(`SELECT stage FROM runs WHERE project = ? AND item = ?`, prj, is.ID); got != "unattended-brainstorm" {
		t.Fatalf("the run on %s is of stage %q, want unattended-brainstorm", is.ID, got)
	}
	if got := w.status(is.ID); got != "prd_drafting" {
		t.Fatalf("the epic is in %s, want prd_drafting", got)
	}
	raw, err := os.ReadFile(filepath.Join(l[0].Spec.Checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if b.Item == nil || b.Item.ID != is.ID || b.Item.Facts["request"] != "let me save a search" {
		t.Fatalf("brief item = %s", raw)
	}
	if b.Runbook != ".claude/skills/loop-unattended-brainstorm/SKILL.md" {
		t.Fatalf("brief runbook = %q, want .claude/skills/loop-unattended-brainstorm/SKILL.md", b.Runbook)
	}
}
