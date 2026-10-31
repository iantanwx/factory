package machine

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// C3: only an unattended-brainstorm run claims a PRD epic. Its claim lands on
// prd_drafting and takes the lease. A claim by any other stage is refused,
// and the epic stays in prd_needed with no owner.
func TestOnlyAnUnattendedBrainstormClaimsAPRDEpic(t *testing.T) {
	for _, stage := range config.RunStages() {
		if stage == "unattended-brainstorm" {
			continue
		}
		t.Run(stage, func(t *testing.T) {
			w := newWorld(t)
			id := w.create(KindEpic, "create_prd", nil)
			_, res := w.m.Claim(context.Background(), ClaimRequest{Project: prj, Item: id, Stage: stage, RunID: "r-other"})
			if res.Refusal == nil {
				t.Fatalf("a %s run claimed a PRD epic", stage)
			}
			if it := w.show(id); it.Status != "prd_needed" || it.Owner != "" {
				t.Fatalf("the refused claim moved the epic: %+v", it)
			}
		})
	}
	w := newWorld(t)
	id := w.create(KindEpic, "create_prd", nil)
	w.claim(id, "unattended-brainstorm", "r-prd")
	if it := w.show(id); it.Status != "prd_drafting" || it.Owner != "r-prd" {
		t.Fatalf("after the unattended-brainstorm claim: %+v", it)
	}
}

// C3: release and reclaim return prd_drafting to prd_needed and drop the
// lease, so the next unattended-brainstorm run takes the epic again.
func TestASettledUnattendedBrainstormReturnsTheEpicToPRDNeeded(t *testing.T) {
	for _, verb := range []string{"release", "reclaim"} {
		t.Run(verb, func(t *testing.T) {
			w := newWorld(t)
			id := w.create(KindEpic, "create_prd", nil)
			w.claim(id, "unattended-brainstorm", "r-prd")
			w.must(id, verb, nil)
			if it := w.show(id); it.Status != "prd_needed" || it.Owner != "" {
				t.Fatalf("after %s: %+v", verb, it)
			}
		})
	}
}

// C3: escalate from prd_drafting stores the resume target prd_needed, and
// answer lands there with no owner.
func TestAnEscalatedUnattendedBrainstormResumesToPRDNeeded(t *testing.T) {
	w := newWorld(t)
	id := w.create(KindEpic, "create_prd", nil)
	w.claim(id, "unattended-brainstorm", "r-prd")
	w.must(id, "escalate", nil)
	if got := w.show(id).Fact(FactResume); got != "prd_needed" {
		t.Fatalf("prd_drafting resumes to %q, want prd_needed", got)
	}
	w.must(id, "answer", nil)
	if it := w.show(id); it.Status != "prd_needed" || it.Owner != "" {
		t.Fatalf("epic after answer: %+v", it)
	}
}
