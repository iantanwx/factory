package server

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// C4: every loader that writes the last good config refuses a pin whose
// directory the default branch tracks. A load through the merge hand-off
// source or the settings service, then a load through the scheduler's
// loader, keeps the pin out of force.
func TestC4NoLoaderLetsAPinWhoseDirectoryIsTrackedIntoForce(t *testing.T) {
	for name, load := range map[string]func(*testing.T, *ghWorld, *Merge){
		"merge hand-off": func(t *testing.T, _ *ghWorld, mg *Merge) {
			// The hand-off resolves the mechanism of the pinned config. The
			// tick's own error is not under test.
			_ = mg.HandOff.TickProject(context.Background(), "alpha")
		},
		"settings show": func(t *testing.T, w *ghWorld, _ *Merge) {
			if _, ref := w.srv.settingsService().Show(context.Background(), "alpha"); ref != nil {
				t.Fatalf("settings show: %+v", *ref)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			mg := &Merge{}
			w, _ := setWorldWith(t, "merge:\n  mechanism: direct\nstages:\n  code-reviewer: {}\n", mg)
			ctx := context.Background()
			p, err := w.srv.Projects.Show("alpha")
			if err != nil {
				t.Fatal(err)
			}
			// The scheduler's loader reads the clone's origin/main.
			gt.Git(t, w.checkout, "fetch", "-q", "origin")
			if _, err := w.srv.projectLoader(LocalRefs)(ctx, "alpha"); err != nil {
				t.Fatal(err)
			}

			gt.Commit(t, w.checkout, "pin neon and commit its directory", map[string]string{
				config.ProjectConfigPath: "merge:\n  mechanism: direct\nstages:\n  code-reviewer: {}\nskills:\n" +
					"  - name: neon\n    repo: owner/repo\n    commit: 0123456789abcdef0123456789abcdef01234567\n" +
					"    path: \"\"\n    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
				".claude/skills/neon/SKILL.md": "neon\n",
			})
			gt.Git(t, w.checkout, "push", "-q", "origin", "main")
			if _, _, err := w.gh.Service.SyncBase(ctx, p); err != nil {
				t.Fatal(err)
			}

			load(t, w, mg)
			w.srv.caches.forget("alpha")
			snap, err := w.srv.projectLoader(LocalRefs)(ctx, "alpha")
			if err != nil {
				t.Fatal(err)
			}
			if len(snap.Config.Skills) != 0 {
				t.Fatalf("skills in force after the %s load = %+v, want the last good config", name, snap.Config.Skills)
			}
		})
	}
}
