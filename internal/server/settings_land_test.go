package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/merge"
	"github.com/iantanwx/loop/internal/merge/fake"
)

// The ticket's done case, and C28: with the fake mechanism, a change goes
// from open to merged across three hand-off ticks. The fourth tick's
// admission admits against the new cap, inside one server process, with
// the 30-second config cache in place. The inbox holds one notice.
func TestC28MergedSettingsChangeIsInForceAtTheNextTick(t *testing.T) {
	mech := fake.New()
	mg := &Merge{Mechanisms: map[config.MergeMechanism]merge.Mechanism{config.MergeDirect: mech}}
	w, _ := setWorldWith(t, "merge:\n  mechanism: direct\nstages:\n  implementer: {}\n", mg)
	ctx := context.Background()
	// Admission reads the clone's origin/main, which the merge fetches.
	gt.Git(t, w.checkout, "fetch", "-q", "origin")
	adm, err := w.srv.composeAdmission()
	if err != nil {
		t.Fatal(err)
	}
	adm.Attach(w.srv.Machine)
	admit := func(run string) *cli.Refusal {
		t.Helper()
		id, res := w.srv.Machine.Create(ctx, machine.CreateRequest{Project: "alpha", Kind: machine.KindTicket, Title: run,
			Verb: "create", Actor: machine.Server("test")})
		if res.Refusal != nil {
			t.Fatalf("create: %+v", *res.Refusal)
		}
		_, ref := adm.Admit(ctx, admission.Request{Project: "alpha", Stage: machine.StageImplementer, Item: id, RunID: run})
		return ref
	}
	// The host cap of 8 admits one run, and admission caches the config.
	if ref := admit("run-a"); ref != nil {
		t.Fatalf("admit under the host cap: %+v", *ref)
	}

	env, code := w.as(cli.PrincipalCoordinator, "settings", "set", "alpha", "stages.implementer.cap", "--value", "1",
		"--reason", "one implementer at a time")
	if code != 0 || env["data"].(map[string]any)["state"] != "open" {
		t.Fatalf("settings set = %d %v", code, env)
	}
	data := env["data"].(map[string]any)
	n, head := int(data["pr"].(float64)), data["head"].(string)
	pr := w.api.World.PR(n)
	w.api.World.Update(pr, func(p *gt.PR) { p.HeadSHA = head })
	w.api.World.SetRuns(head, gt.CheckRun("ci", head, "completed", "success", time.Now()))

	tick := func() {
		t.Helper()
		if err := mg.HandOff.TickProject(ctx, "alpha"); err != nil {
			t.Fatalf("hand-off tick: %v", err)
		}
	}
	tick()
	if !mech.HandedOff(n) {
		t.Fatalf("the first tick did not hand #%d to the mechanism", n)
	}
	tick() // the mechanism still holds it

	// The mechanism merges: main gains the proposed file.
	proposed := w.api.World.Commits(firstSettingsBranch)[0].Content
	merged := gt.Commit(t, w.checkout, "merge the settings change", map[string]string{config.ProjectConfigPath: proposed})
	gt.Git(t, w.checkout, "push", "-q", w.git.RemoteDir(), "main")
	mech.Set(n, merge.Result{Outcome: merge.Merged, SHA: merged})
	tick()

	ref := admit("run-b")
	if ref == nil || ref.Code != cli.CodeCapFull || !strings.Contains(ref.Hint, "stages.implementer.cap") {
		t.Fatalf("admit after the merge = %+v; want CAP_FULL from stages.implementer.cap", ref)
	}

	env, code = w.as(cli.PrincipalCoordinator, "inbox", "list")
	if code != 0 {
		t.Fatalf("inbox list = %d %v", code, env)
	}
	var notices []map[string]any
	for _, e := range env["data"].([]any) {
		if e := e.(map[string]any); e["kind"] == "notice" {
			notices = append(notices, e)
		}
	}
	if len(notices) != 1 || !strings.Contains(notices[0]["text"].(string), merged) {
		t.Fatalf("notices = %v; want one that names %s", notices, merged)
	}
}

// C28: a merge that lands while a config load is in flight still takes
// force at the next read. The load that read the old config does not keep
// it in the cache past the merge's forget.
func TestC28MergeDuringALoadIsInForceAtTheNextRead(t *testing.T) {
	w := newSchedWorld(t, Options{})
	ctx := context.Background()
	p, err := w.srv.Projects.Show("alpha")
	if err != nil {
		t.Fatal(err)
	}
	merged := false
	load := w.srv.projectLoader(func(ctx context.Context, p config.Project) (config.RefReader, string, error) {
		read, branch, err := LocalRefs(ctx, p)
		return func(ctx context.Context, ref, path string) ([]byte, error) {
			raw, err := read(ctx, ref, path)
			if path == config.ProjectConfigPath && !merged {
				// The merge lands after this read: main gains a stage, and
				// the server forgets the cached config.
				merged = true
				cfg := "stages:\n  code-reviewer: {}\n  implementer: {}\nmerge:\n  mechanism: direct\n"
				if err := os.WriteFile(filepath.Join(p.Path, config.ProjectConfigPath), []byte(cfg), 0o644); err != nil {
					t.Fatal(err)
				}
				gitT(t, p.Path, "commit", "-q", "-am", "merge the settings change")
				gitT(t, p.Path, "push", "-q", "origin", "main")
				w.srv.caches.forget(p.ID)
			}
			return raw, err
		}, branch, err
	})
	enabled := func() bool {
		t.Helper()
		snap, err := load(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		return snap.Config.StageEnabled("implementer")
	}
	if enabled() {
		t.Fatal("the load in flight read the merged config; want the config before the merge")
	}
	if !enabled() {
		t.Fatal("the read after the merge serves the config before it")
	}
}
