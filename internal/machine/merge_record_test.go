package machine

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/store"
)

// C1: a spec merge records its commit. doc_merged from spec_signoff sets
// spec_merged to the sha and still sets spec_path. On the outran path the
// second apply is that row.
func TestC1SpecMergeRecordsItsCommit(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		from   string
		merges int
	}{{SpecSignoff, 1}, {SpecReview, 2}} {
		t.Run(c.from, func(t *testing.T) {
			w := newWorld(t)
			ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
			w.force(ep, c.from, nil)
			ev := MergeEvent{Project: prj, Item: ep, Base: "main", Into: "main", SHA: "m1", SpecPath: "docs/plans/x-spec.md"}
			for range c.merges {
				mustOK(t, w.m.MergeObserved(ctx, ev))
			}
			it := w.show(ep)
			if it.Status != SpecApproved || it.Fact("spec_merged") != "m1" || it.Fact(FactSpecPath) != "docs/plans/x-spec.md" {
				t.Fatalf("after the spec merge: %s %v", it.Status, it.Facts)
			}
			h := w.history(ep)
			if last := h[len(h)-1]; last.From != SpecSignoff || last.Fact("spec_merged") != "m1" {
				t.Fatalf("the merge row records %+v", last)
			}
		})
	}
}

// merge applies doc_merged with sha until the epic leaves the document's
// review, amending and sign-off states, as the reactor's ticks do.
func (w *world) merge(ep, sha string) {
	w.t.Helper()
	ev := MergeEvent{Project: prj, Item: ep, Base: "main", Into: "main", SHA: sha}
	from := w.show(ep).Status
	mustOK(w.t, w.m.MergeObserved(context.Background(), ev))
	if from != PRDSignoff && from != SpecSignoff {
		mustOK(w.t, w.m.MergeObserved(context.Background(), ev))
	}
}

func (w *world) mergeOf(sha string) (MergeRecord, bool) {
	w.t.Helper()
	rec, ok, err := w.m.MergeOf(context.Background(), prj, sha)
	if err != nil {
		w.t.Fatal(err)
	}
	return rec, ok
}

// C4: MergeOf returns the reviewed head a ticket held at its merge, and
// an empty head for a ticket merged from in_review. An unknown commit
// has no record.
func TestC4MergeOfATicket(t *testing.T) {
	w := newWorld(t)
	reviewed := w.create(KindTicket, "create", nil)
	w.force(reviewed, InReview, nil)
	w.must(reviewed, "review_pass", map[string]string{"head": "h1"})
	w.must(reviewed, "merged", map[string]string{"sha": "t1"})
	unreviewed := w.create(KindTicket, "create", nil)
	w.force(unreviewed, InReview, nil)
	w.must(unreviewed, "merged", map[string]string{"sha": "t2"})

	rec, ok := w.mergeOf("t1")
	h := w.history(reviewed)
	want := MergeRecord{Item: reviewed, Kind: KindTicket, Verb: "merged", Fact: "merged_sha", ReviewedHead: "h1", Seq: h[len(h)-1].Seq}
	if !ok || rec != want {
		t.Fatalf("MergeOf(t1) = %+v %v, want %+v", rec, ok, want)
	}
	if rec, ok := w.mergeOf("t2"); !ok || rec.Item != unreviewed || rec.ReviewedHead != "" || rec.Outran {
		t.Fatalf("MergeOf(t2) = %+v %v", rec, ok)
	}
	if rec, ok := w.mergeOf("unknown"); ok {
		t.Fatalf("MergeOf(unknown) = %+v", rec)
	}
}

// C4: the PRD's record keeps the PRD's reviewed head after the spec
// passes review at another head, and the spec's record holds the spec's.
func TestC4MergeOfAPRDKeepsItsHeadAfterTheSpecPasses(t *testing.T) {
	w := newWorld(t)
	ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
	w.must(ep, "review_pass", map[string]string{"head": "h1"})
	w.merge(ep, "p1")
	w.force(ep, SpecReview, nil)
	w.must(ep, "review_pass", map[string]string{"head": "h2"})
	if rec, ok := w.mergeOf("p1"); !ok || rec.Item != ep || rec.Kind != KindEpic || rec.Verb != "doc_merged" ||
		rec.Fact != "prd_merged" || rec.ReviewedHead != "h1" || rec.Outran {
		t.Fatalf("MergeOf(p1) after the spec pass = %+v %v", rec, ok)
	}
	w.merge(ep, "s1")
	if rec, ok := w.mergeOf("s1"); !ok || rec.Item != ep || rec.Fact != "spec_merged" || rec.ReviewedHead != "h2" || rec.Outran {
		t.Fatalf("MergeOf(s1) = %+v %v", rec, ok)
	}
}

// C4: Outran is true for a document merged from its review or amending
// state, and false for one merged from sign-off, also when the PRD
// before it outran its own review.
func TestC4MergeOfOutran(t *testing.T) {
	prdAt := func(w *world, ep, state string) {
		switch state {
		case PRDSignoff:
			w.must(ep, "review_pass", map[string]string{"head": "h1"})
		case PRDAmending:
			w.must(ep, VerbPersonComment, map[string]string{"head": "h1"})
		}
	}
	specAt := func(w *world, ep, state string) {
		w.merge(ep, "p1")
		w.force(ep, SpecReview, nil)
		switch state {
		case SpecSignoff:
			w.must(ep, "review_pass", map[string]string{"head": "h2"})
		case SpecAmending:
			w.must(ep, VerbPersonComment, map[string]string{"head": "h2"})
		}
	}
	for _, c := range []struct {
		name, sha, fact string
		at              func(w *world, ep string)
		outran          bool
	}{
		{"prd from review", "p1", "prd_merged", func(w *world, ep string) { prdAt(w, ep, PRDReview) }, true},
		{"prd from amending", "p1", "prd_merged", func(w *world, ep string) { prdAt(w, ep, PRDAmending) }, true},
		{"spec from review", "s1", "spec_merged", func(w *world, ep string) { prdAt(w, ep, PRDSignoff); specAt(w, ep, SpecReview) }, true},
		{"spec from amending", "s1", "spec_merged", func(w *world, ep string) { prdAt(w, ep, PRDSignoff); specAt(w, ep, SpecAmending) }, true},
		{"prd from sign-off", "p1", "prd_merged", func(w *world, ep string) { prdAt(w, ep, PRDSignoff) }, false},
		{"spec from sign-off after the prd outran", "s1", "spec_merged", func(w *world, ep string) { specAt(w, ep, SpecSignoff) }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			ep := w.create(KindEpic, "prd_submit", map[string]string{"pr": "7"})
			c.at(w, ep)
			w.merge(ep, c.sha)
			if rec, ok := w.mergeOf(c.sha); !ok || rec.Fact != c.fact || rec.Outran != c.outran {
				t.Fatalf("MergeOf(%s) = %+v %v, want fact %s outran %v", c.sha, rec, ok, c.fact, c.outran)
			}
		})
	}
}

// historyRow writes one applied intent and its transition row, as an
// apply does, at a chosen intent_id and seq.
func (w *world) historyRow(id, seq int64, item, verb, facts string) {
	w.t.Helper()
	if err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		if _, err := tx.Exec(`INSERT INTO intents (id, project, item, seq, kind, verb, epoch, state, created_at)
			VALUES (?, ?, ?, ?, 'epic', ?, 0, 'applied', 0)`, id, prj, item, seq, verb); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO transitions (intent_id, project, item, seq, kind, verb, from_status, to_status, epoch, facts, at)
			VALUES (?, ?, ?, ?, 'epic', ?, '', '', 0, ?, 0)`, id, prj, item, seq, verb, facts)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// C4: when two epics record one commit as adr_merged, MergeOf returns the
// row with the lower intent_id. A seq order and an order by item both
// return ep-a. A later row that clears the fact leaves the record, and an
// adr_merged record reads no reviewed head.
func TestC4MergeOfTwoRowsForOneCommit(t *testing.T) {
	w := newWorld(t)
	w.historyRow(9, 1, "ep-b", "review_pass", `{"set":{"loop.reviewed_head":"h9"}}`)
	w.historyRow(10, 2, "ep-b", VerbADRLanded, `{"set":{"loop.adr":"docs/adr/0001-x.md","loop.adr_merged":"c1"}}`)
	w.historyRow(11, 1, "ep-a", VerbADRLanded, `{"set":{"loop.adr":"docs/adr/0001-x.md","loop.adr_merged":"c1"}}`)
	w.historyRow(12, 3, "ep-b", VerbADRMisjoined, `{"unset":["loop.adr","loop.adr_merged"]}`)
	rec, ok := w.mergeOf("c1")
	want := MergeRecord{Item: "ep-b", Kind: KindEpic, Verb: VerbADRLanded, Fact: "adr_merged", Seq: 2}
	if !ok || rec != want {
		t.Fatalf("MergeOf(c1) = %+v %v, want %+v", rec, ok, want)
	}
}
