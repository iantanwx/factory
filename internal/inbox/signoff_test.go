package inbox

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/gitx/gittest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// C23: a spec sign-off whose Needs an ADR question holds a **Deferred run,
// or an answer that cites no ADR, refuses with rule open_questions and
// merges nothing. An answer that cites an ADR lets the spec merge.
func TestSpecSignoffHeldByANeedsAnADRQuestion(t *testing.T) {
	const ask = "# Spec\n\n## Open questions\n\n- **Needs an ADR:** Decisions, \"The engine stores Parquet\".\n"
	for _, c := range []struct {
		name   string
		answer string
		held   bool
	}{
		{"deferred", "  **Deferred:** to the next epic.\n", true},
		{"answered with no ADR", "  **Answered (operator, 2026-10-31):** Parquet.\n", true},
		{"answered with an ADR", "  **Answered (operator, 2026-11-02):** ADR 0014 decides it.\n", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, ep := newSignoffWorld(t, machine.SpecSignoff, nil, nil)
			spec := "docs/plans/" + ep + "-engine-spec.md"
			w.gh.files = []github.File{{Path: spec, Status: "added"}}
			w.gh.text = map[string]string{spec: ask + c.answer}
			res := w.answer(ep)
			if !c.held {
				if res.Refusal != nil || !slices.Contains(w.gh.calls, "MergePR 7 "+signoffHead) {
					t.Fatalf("answer = %+v, calls %v, want a merge at %s", res.Refusal, w.gh.calls, signoffHead)
				}
				return
			}
			r := res.Refusal
			if r == nil || r.Code != cli.CodeShapeRefused || r.Rule != "open_questions" {
				t.Fatalf("answer = %+v, want %s rule open_questions", res, cli.CodeShapeRefused)
			}
			if !strings.Contains(r.Hint, "line 5: **Needs an ADR:**") {
				t.Fatalf("hint = %q, want it to name the entry at line 5", r.Hint)
			}
			if w.gh.merged() || w.answered() != 0 {
				t.Fatalf("a held sign-off merged or recorded: calls %v, %d audit rows", w.gh.calls, w.answered())
			}
			if it := w.status(ep); it.Status != machine.SpecSignoff {
				t.Fatalf("status = %s, want %s", it.Status, machine.SpecSignoff)
			}
		})
	}
}

// C18, C19: a spec sign-off finds the spec under docs/specs/ or under
// docs/plans/, merges it and records its path.
func TestC18C19SpecSignoffFindsASpecInEitherDirectory(t *testing.T) {
	decided := "# Spec\n\n## Open questions\n\nNone.\n"
	for _, dir := range []string{"docs/specs/", "docs/plans/"} {
		w, ep := newSignoffWorld(t, machine.SpecSignoff, nil, nil)
		spec := dir + ep + "-cache-spec.md"
		w.gh.files = []github.File{{Path: spec, Status: "added"}}
		w.gh.text = map[string]string{spec: decided}
		if res := w.answer(ep); res.Refusal != nil {
			t.Fatalf("%s: the spec sign-off refused: %+v", spec, res.Refusal)
		}
		if it := w.status(ep); it.Status != machine.SpecApproved || it.Fact(machine.FactSpecPath) != spec {
			t.Fatalf("after the sign-off: %s %v, want %s with spec_path %s", it.Status, it.Facts, machine.SpecApproved, spec)
		}
	}
}

// C20: a spec PR with one spec of the epic under each directory has no
// single spec. The sign-off refuses no_spec, names both directories and
// merges nothing.
func TestC20SpecSignoffRefusesOneSpecInEachDirectory(t *testing.T) {
	decided := "# Spec\n\n## Open questions\n\nNone.\n"
	w, ep := newSignoffWorld(t, machine.SpecSignoff, nil, map[string]string{})
	for _, dir := range []string{"docs/specs/", "docs/plans/"} {
		w.gh.files = append(w.gh.files, github.File{Path: dir + ep + "-cache-spec.md", Status: "added"})
		w.gh.text[dir+ep+"-cache-spec.md"] = decided
	}
	r := w.answer(ep).Refusal
	if r == nil || r.Code != cli.CodeShapeRefused || r.Rule != "no_spec" {
		t.Fatalf("refusal = %+v, want %s rule no_spec", r, cli.CodeShapeRefused)
	}
	for _, want := range []string{"docs/specs/" + ep + "-*-spec.md", "docs/plans/" + ep + "-*-spec.md", "2 files"} {
		if !strings.Contains(r.Hint, want) {
			t.Fatalf("hint = %q, want it to name %q", r.Hint, want)
		}
	}
	if w.gh.merged() || w.status(ep).Status != machine.SpecSignoff {
		t.Fatalf("a refused sign-off merged or moved: calls %v", w.gh.calls)
	}
}

// C12: the sign-off of a PRD records the UI stories that DocMerge read at
// the merge commit, and the epic lands on design.
func TestC12PRDSignoffRoutesThroughDocMerge(t *testing.T) {
	w, ep := newSignoffWorld(t, machine.PRDSignoff, nil, nil)
	w.gh.ui = "US-02"
	if res := w.answer(ep); res.Refusal != nil {
		t.Fatalf("the PRD sign-off refused: %+v", res.Refusal)
	}
	if !slices.Contains(w.gh.calls, "DocMerge "+ep+" m-"+signoffHead) {
		t.Fatalf("calls %v, want a DocMerge at the merge commit m-%s", w.gh.calls, signoffHead)
	}
	if it := w.status(ep); it.Status != machine.Design || it.Fact(machine.FactUIStories) != "US-02" {
		t.Fatalf("after the sign-off: %s with ui_stories=%q, want design with US-02", it.Status, it.Fact(machine.FactUIStories))
	}
}

type oneProject []config.Project

func (o oneProject) List() ([]config.Project, error)     { return o, nil }
func (o oneProject) Show(string) (config.Project, error) { return o[0], nil }

// C12: one PRD merge of a UI project, through the operator's sign-off and
// through the reactor, gives the same status and the same UI stories. Each
// path runs over the same GitHub service, which reads the config and the
// PRD at the merge commit.
func TestC12BothMergePathsRouteAUIPRDAlike(t *testing.T) {
	ctx := context.Background()
	api := gt.NewAPI(t)
	git := gt.NewGitServer(t, api)
	checkout := t.TempDir()
	git.Seed(t, checkout)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := config.Project{ID: prj, Repo: gt.Repo, Path: checkout, Branch: "main"}
	if err := config.StoreProjectRow(st)(ctx, p); err != nil {
		t.Fatal(err)
	}
	bx := fake.New("tk")
	if err := bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	m, err := machine.New(machine.Config{Store: st, Beads: func(string) (beadsx.Client, error) { return bx, nil },
		Registry: cli.Surface(), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := github.New(github.Config{
		Apps: github.OneApp(gt.AppID, func(context.Context) ([]byte, error) { return gt.KeyPEM(), nil }),
		API:  api.URL(), GitURL: git.URL, Protocols: []string{"http"}, MirrorRoot: t.TempDir(), GitConfig: gittest.Config,
		Store: st, Projects: oneProject{p}, Machine: m, Logger: quiet,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Mirror(p).Init(ctx); err != nil {
		t.Fatal(err)
	}
	in := New(Config{Store: st, Machine: m, GitHub: svc, Projects: oneProject{p}.List})
	const prd, head = "docs/prd/widgets.md", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	text := "# Widgets\n\n### US-01 Show\n\n**UI:** yes\n\n### US-02 Store\n\n**UI:** no\n\n### US-03 Edit\n\n**UI:** yes\n"
	epic := func(n int) string {
		id, res := m.Create(ctx, machine.CreateRequest{Project: prj, Kind: machine.KindEpic, Title: "Widgets", Verb: machine.VerbCreatePRD,
			Actor: machine.Server("test"), Facts: map[string]string{machine.FactPRDPath: prd}})
		if res.Refusal != nil {
			t.Fatal(res.Refusal.Hint)
		}
		is, err := bx.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := bx.Update(ctx, id, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.PRDSignoff, SetMetadata: map[string]string{
			"loop." + machine.FactDocPR: strconv.Itoa(n), "loop." + machine.FactReviewedHead: head}}); err != nil {
			t.Fatal(err)
		}
		api.World.SetFile(gt.MergeSHA(n), ".loop/config.yaml", "merge:\n  mechanism: direct\nui:\n  kind: web\n  paths: [web/**]\n")
		api.World.SetFile(gt.MergeSHA(n), prd, text)
		return id
	}
	signed, reacted := epic(1), epic(2)
	api.World.Add(&gt.PR{Head: github.BranchFor(signed), HeadSHA: head})
	byReactor := api.World.Add(&gt.PR{Head: github.BranchFor(reacted), HeadSHA: head})

	if res := in.signoff(ctx, &rpc.Call{}, "signoff/"+prj+"/"+signed, prj, signed, `The operator: "LGTM"`); res.Refusal != nil {
		t.Fatalf("the sign-off refused: %+v", res.Refusal)
	}
	api.World.Update(byReactor, func(pr *gt.PR) { pr.State, pr.Merged, pr.MergeSHA = "closed", true, gt.MergeSHA(byReactor.Number) })
	if err := github.NewReactor(svc).Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	var got [2]machine.Item
	for i, id := range []string{signed, reacted} {
		if got[i], _ = m.Read(ctx, prj, id); got[i].Status != machine.Design || got[i].Fact(machine.FactUIStories) != "US-01,US-03" {
			t.Fatalf("epic %s: %s with ui_stories=%q, want design with US-01,US-03", id, got[i].Status, got[i].Fact(machine.FactUIStories))
		}
	}
	if got[0].Fact(machine.FactPRDMerged) != gt.MergeSHA(1) || got[1].Fact(machine.FactPRDMerged) != gt.MergeSHA(2) {
		t.Fatalf("prd_merged: %q by the sign-off, %q by the reactor", got[0].Fact(machine.FactPRDMerged), got[1].Fact(machine.FactPRDMerged))
	}
	if _, res := m.Claim(ctx, machine.ClaimRequest{Project: prj, Item: signed, Stage: machine.StagePlanner, RunID: "r-spec"}); res.Refusal == nil {
		t.Fatalf("the planner claimed %s in design", signed)
	}
}
