package pipeline_test

import (
	"testing"

	"github.com/iantanwx/loop/internal/machine"
)

// C18: with no spec_path, decompose reads the one base file
// docs/specs/<epic>-*-spec.md.
func TestC18DecomposeReadsASpecUnderDocsSpecs(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposing()
	spec := "docs/specs/" + epic + "-widgets-spec.md"
	w.onBase(map[string]string{specPath(epic, "widgets"): "", spec: specWith(undecided)})
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(threeTickets)), epic, "open_questions")
	hintHolds(t, ref, spec, "line 9: Sort widgets by price?")

	w.onBase(map[string]string{spec: specWith(decided)})
	ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(threeTickets)))
	if got := w.show(epic).Status; got != machine.Execution {
		t.Fatalf("epic after the batch: %s", got)
	}
}

// C20: with no spec_path, one base spec of the epic under each directory
// refuses no_spec, and the hint names both directories.
func TestC20DecomposeRefusesOneSpecInEachDirectory(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposing()
	w.onBase(map[string]string{"docs/specs/" + epic + "-widgets-spec.md": specWith(decided)})
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(threeTickets)), epic, "no_spec")
	hintHolds(t, ref, "2 files", "docs/specs/"+epic+"-*-spec.md", "docs/plans/"+epic+"-*-spec.md")
}
