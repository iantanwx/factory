package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
)

// C8: the scheduler's project loader checks design.skill against the
// pack's kernel skills and the project's pins. A design.skill that names
// the kernel skill loop-impeccable loads. One that names neither keeps the
// last good config in force, and the project_config_invalid alert names
// design.skill and the skill.
func TestC8SchedLoaderChecksTheCanvasSkillAgainstTheKernel(t *testing.T) {
	kernel := fstest.MapFS{"loop-impeccable/SKILL.md": {Data: []byte("---\nname: loop-impeccable\n---\n"), Mode: 0o644}}
	w := newSchedWorld(t, Options{Sched: SchedOptions{Pack: brief.Pack{FS: kernel}}})
	ctx := context.Background()
	p, err := w.srv.Projects.Show("alpha")
	if err != nil {
		t.Fatal(err)
	}
	commitConfig := func(skill string) {
		cfg := "stages:\n  code-reviewer: {}\nmerge:\n  mechanism: direct\nui:\n  kind: web\n  paths: [web/**]\n" +
			"design:\n  run: make serve\n  capture: make shot\n  skill: " + skill + "\n"
		if err := os.WriteFile(filepath.Join(p.Path, config.ProjectConfigPath), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
		gitT(t, p.Path, "commit", "-q", "-am", "name the canvas skill "+skill)
		gitT(t, p.Path, "push", "-q", "origin", "main")
		w.srv.caches.forget("alpha")
	}

	commitConfig("loop-impeccable")
	snap, err := w.srv.projectLoader(LocalRefs)(ctx, "alpha")
	if err != nil || snap.Config.Design == nil || snap.Config.Design.Skill != "loop-impeccable" {
		t.Fatalf("load = %+v, %v; want design.skill loop-impeccable in force", snap, err)
	}

	commitConfig("zz-stub-canvas")
	snap, err = w.srv.projectLoader(LocalRefs)(ctx, "alpha")
	if err != nil || snap.Config.Design == nil || snap.Config.Design.Skill != "loop-impeccable" {
		t.Fatalf("load = %+v, %v; want the last good config's design.skill loop-impeccable", snap, err)
	}
	var body string
	err = w.srv.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT body FROM alerts WHERE project = 'alpha' AND kind = ? AND state = 'open'`,
			config.AlertProjectConfigInvalid).Scan(&body)
	})
	if err != nil || !strings.Contains(body, "design.skill") || !strings.Contains(body, "zz-stub-canvas") {
		t.Fatalf("alert body = %q, %v; want one that names design.skill and zz-stub-canvas", body, err)
	}
}
