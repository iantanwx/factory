//go:build acceptance

package scenarios

import (
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/machine"
)

// factorio-ku99 C9 and C16: the coordinator sends a PRD epic to the ADR
// stage. A send in sign-off keeps the status, and the operator's merge of
// the PRD lands the epic on architecture. A send in the spec stage moves
// the epic to architecture at once, and keeps its document PR.
func TestEpicADRSendEndToEnd(t *testing.T) {
	w := newWorld(t, acceptance.Options{})

	room, co := openRoom(w, "intake", "", "the operator wants widgets")
	e1 := submitPRD(w, co, "Widgets")
	w.Must(w.Coordinator("room", "close", room))
	docReview(w, e1, "pass")
	w.Must(w.Coordinator("epic", "adr", e1, "--project", acceptance.Project, "--request", "Ian: widgets need an ADR first."))
	wantStatus(w, e1, machine.PRDSignoff)
	w.MergeByHand(docPR(w, e1))
	w.React()
	wantStatus(w, e1, machine.Architecture)

	room, co = openRoom(w, "intake", "", "the operator wants gizmos")
	e2 := submitPRD(w, co, "Gizmos")
	w.Must(w.Coordinator("room", "close", room))
	docReview(w, e2, "pass")
	w.MergeByHand(docPR(w, e2))
	w.React()
	wantStatus(w, e2, machine.SpecNeeded)
	pr := w.Show(e2).Facts[machine.FactDocPR]
	w.Must(w.Coordinator("epic", "adr", e2, "--project", acceptance.Project, "--request", "Ian: gizmos need an ADR before the spec."))
	wantStatus(w, e2, machine.Architecture)
	if got := w.Show(e2).Facts[machine.FactDocPR]; got != pr {
		t.Fatalf("doc_pr = %q after the send, want %q", got, pr)
	}
}
