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
