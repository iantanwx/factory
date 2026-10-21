package machine

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// U13: a spec author starts only on an epic whose PRD merged or whose ADR
// landed. An epic that reached spec_needed by any other road is refused.
func TestSpecAuthorNeedsAMergedPRDOrADR(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	bare := w.create(KindEpic, "prd_submit", nil)
	w.force(bare, SpecNeeded, nil)
	_, res := w.m.Claim(ctx, ClaimRequest{Project: prj, Item: bare, Stage: StageSpecAuthor, RunID: "r-spec"})
	wantCode(t, res, cli.CodePRDRequired)
	if it := w.show(bare); it.Status != SpecNeeded || it.Owner != "" {
		t.Fatalf("a refused claim moved the epic: %+v", it)
	}

	ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	if got := w.show(ep).Fact(FactDocPR); got != "7" {
		t.Fatalf("prd_submit stored doc_pr %q", got)
	}
	w.must(ep, "review_pass", nil)
	w.must(ep, "doc_merged", map[string]string{"sha": "m1"})
	if it := w.show(ep); it.Status != SpecNeeded || it.Fact(FactPRDMerged) != "m1" {
		t.Fatalf("the operator's merge: %+v", it)
	}
	w.claim(ep, StageSpecAuthor, "r-spec")

	arch := w.create(KindEpic, "create_architecture", nil)
	w.must(arch, "adr_landed", nil)
	w.claim(arch, StageSpecAuthor, "r-arch")
}

// The spec's PR replaces the PRD's as the epic's document PR.
func TestSpecPROpenRecordsTheDocumentPR(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	w.must(ep, "review_pass", nil)
	w.must(ep, "doc_merged", nil)
	w.claim(ep, StageSpecAuthor, "r-spec")
	w.must(ep, "pr_open", map[string]string{"pr": "9"})
	if it := w.show(ep); it.Status != SpecReview || it.Fact(FactDocPR) != "9" {
		t.Fatalf("after the spec's pr open: %+v", it)
	}
}

// An epic's PR merge that no document row takes is no fault: the merge
// already counted.
func TestEpicMergeWithNoDocumentRowIsNothing(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "prd_submit", nil)
	w.must(ep, "review_pass", nil)
	ctx := context.Background()
	mustOK(t, w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: ep, Base: "main", Into: "main", SHA: "m1"}))
	res := w.m.MergeObserved(ctx, MergeEvent{Project: prj, Item: ep, Base: "main", Into: "main", SHA: "m1"})
	if res.Refusal != nil || res.Exit != cli.ExitNothing {
		t.Fatalf("a second read of the merge = %d %+v", res.Exit, res.Refusal)
	}
	if got := w.show(ep).Status; got != SpecNeeded {
		t.Fatalf("epic is %s", got)
	}
}

// AE7: the history of the second reject tells the operator to open an
// intake room, and the inbox prints it.
func TestSecondPRDRejectNamesTheIntakeRoom(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "prd_submit", nil)
	w.must(ep, "review_reject", nil)
	w.must(ep, "prd_amended", nil)
	w.must(ep, "review_reject", nil)
	h := w.history(ep)
	if says := h[len(h)-1].Says; !strings.Contains(says, "intake room") {
		t.Fatalf("the second reject says %q", says)
	}
}

// List selects items by a fact, so work entry finds what an interrupted
// call already created.
func TestListSelectsByFact(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	id, r := w.m.CreateBead(ctx, CreateRequest{Project: prj, Kind: KindTicket, Title: "x", Facts: map[string]string{FactCreateKey: "k1"}})
	if r != nil {
		t.Fatalf("create bead: %+v", r)
	}
	w.create(KindTicket, "create", nil)
	got, r := w.m.List(ctx, prj, ListFilter{Kind: KindTicket, Facts: map[string]string{FactCreateKey: "k1"}})
	if r != nil || len(got) != 1 || got[0].ID != id || got[0].Status != Initial {
		t.Fatalf("list by key = %+v %+v", got, r)
	}
	it, r := w.m.Read(ctx, prj, id)
	if r != nil || it.Fact(FactCreateKey) != "k1" {
		t.Fatalf("read = %+v %+v", it, r)
	}
}
