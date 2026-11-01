package inbox

import (
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
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
