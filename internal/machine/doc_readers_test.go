package machine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// readersOf makes the doc reviews of the world's project name readers, as
// the brief of a doc-reviewer run names them.
func (w *world) readersOf(readers ...string) {
	w.m.SetDocReadersSource(func(context.Context, string) ([]string, error) { return readers, nil })
}

func (w *world) rounds(item, stage string) []Round {
	w.t.Helper()
	res := w.call("review rounds", w.runOn(item, stage, "reader-of-rounds"), []string{item}, nil)
	if res.Exit == cli.ExitNothing {
		return nil
	}
	mustOK(w.t, res)
	rounds, _ := res.Data.([]Round)
	return rounds
}

// C2: with the brief set to all three readers, a verdict with no
// --readers, with two of the three, or with an unknown name, fails with
// SHAPE_REFUSED rule readers. Nothing is posted or recorded, the epic keeps
// its state, and the hint names the three readers.
func TestC2ReviewSubmitRefusesAVerdictThatLacksAReader(t *testing.T) {
	cases := []struct{ name, readers string }{
		{"no --readers", ""},
		{"two of the three", "unslop,asd-ste100"},
		{"an unknown name in place of one", "unslop,asd-ste100,lens"},
		{"an unknown name beside the three", "unslop,asd-ste100,content-lens,tone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			log := &commentLog{}
			w.m.SetReviewCommenter(log.post)
			w.readersOf("unslop", "asd-ste100", "content-lens")
			ep, rev := w.prdInReview(nil)
			before := w.show(ep)

			flags := map[string]string{"verdict": "reject", "head": shaA, "body": "Name the user."}
			if c.readers != "" {
				flags["readers"] = c.readers
			}
			r := wantCode(t, w.call("review submit", rev, []string{ep}, flags), cli.CodeShapeRefused)
			if r.Rule != "readers" || !strings.Contains(r.Hint, "unslop,asd-ste100,content-lens") {
				t.Fatalf("refusal = %+v, want rule readers with a hint that names the three readers", *r)
			}
			if after := w.show(ep); after.Status != PRDReview || after.Seq != before.Seq {
				t.Fatalf("the refusal moved the epic: %s seq %d, want %s seq %d", after.Status, after.Seq, PRDReview, before.Seq)
			}
			if n := len(w.rounds(ep, StageDocReviewer)); n != 0 || len(log.calls) != 0 {
				t.Fatalf("the refusal recorded %d rounds and posted %v", n, log.calls)
			}
		})
	}
}

// C3: an accepted submit records the readers on its round, review rounds
// shows them, and the posted comment ends with the Readers line. A round
// recorded with no readers reads as an empty list.
func TestC3TheRoundRecordsTheReaders(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	ep, rev := w.prdInReview(nil)

	// The first round is recorded with the readers off, as a round before
	// this change was.
	w.readersOf()
	mustOK(t, w.call("review submit", rev, []string{ep}, map[string]string{"verdict": "reject", "head": shaA, "body": "Name the user."}))
	w.force(ep, PRDReview, nil)
	rev = w.claimAs(ep, StageDocReviewer, "rev-2")

	w.readersOf("unslop", "asd-ste100", "content-lens")
	mustOK(t, w.call("review submit", rev, []string{ep}, map[string]string{
		"verdict": "pass", "head": shaA, "body": "The PRD holds.", "readers": "content-lens, unslop,asd-ste100"}))

	rounds := w.rounds(ep, StageDocReviewer)
	if len(rounds) != 2 {
		t.Fatalf("rounds = %+v, want two", rounds)
	}
	raw, err := json.Marshal(rounds)
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Verdict string   `json:"verdict"`
		Head    string   `json:"head"`
		Readers []string `json:"readers"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"readers":[]`) || got[0].Readers == nil || len(got[0].Readers) != 0 {
		t.Fatalf("round 1, recorded with no readers: %s, want \"readers\":[]", raw)
	}
	if r := got[1]; r.Verdict != "pass" || r.Head != shaA || strings.Join(r.Readers, ",") != "unslop,asd-ste100,content-lens" {
		t.Fatalf("round 2 = %+v, want a pass at %s with readers unslop, asd-ste100, content-lens", r, shaA)
	}
	if len(log.calls) != 2 || strings.Split(log.calls[1], "|")[2] != "The PRD holds.\n\nReaders: unslop, asd-ste100, content-lens." {
		t.Fatalf("the posted comment does not end with the Readers line: %q", log.calls)
	}
}

// C3: a verdict with the readers still records only at the head it names.
// A moved head gets STATUS_CHANGED rule head_changed.
func TestC3AVerdictWithReadersAtAMovedHeadIsRefused(t *testing.T) {
	const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	w.m.SetPRHeadReader(func(context.Context, string, string) (string, *cli.Refusal) { return shaB, nil })
	w.readersOf("unslop", "asd-ste100", "content-lens")
	ep, rev := w.prdInReview(nil)

	r := wantCode(t, w.call("review submit", rev, []string{ep}, map[string]string{
		"verdict": "pass", "head": shaA, "body": "The PRD holds.", "readers": "unslop,asd-ste100,content-lens"}), cli.CodeStatusChanged)
	if r.Rule != "head_changed" {
		t.Fatalf("rule %q, want head_changed", r.Rule)
	}
	if it := w.show(ep); it.Status != PRDReview || len(log.calls) != 0 || len(w.rounds(ep, StageDocReviewer)) != 0 {
		t.Fatalf("a verdict at a moved head wrote: %s %v", it.Status, log.calls)
	}
}

// C4: with the readers off, the server records a doc review with no
// --readers.
func TestC4ReadersOffRecordsAVerdictWithNoReaders(t *testing.T) {
	w := newWorld(t)
	w.readersOf()
	ep, rev := w.prdInReview(nil)

	mustOK(t, w.call("review submit", rev, []string{ep}, map[string]string{"verdict": "pass", "head": shaA, "body": "The PRD holds."}))
	if it := w.show(ep); it.Status != PRDSignoff {
		t.Fatalf("status %s, want %s", it.Status, PRDSignoff)
	}
	if rounds := w.rounds(ep, StageDocReviewer); len(rounds) != 1 || rounds[0].Readers == nil || len(rounds[0].Readers) != 0 {
		t.Fatalf("rounds = %+v, want one round with no readers", rounds)
	}
}

// C4: with the readers off, a doc review with --readers set to any value
// fails with SHAPE_REFUSED rule readers.
func TestC4ReadersOffRefusesAVerdictWithReaders(t *testing.T) {
	for _, readers := range []string{"unslop,asd-ste100,content-lens", "unslop"} {
		w := newWorld(t)
		log := &commentLog{}
		w.m.SetReviewCommenter(log.post)
		w.readersOf()
		ep, rev := w.prdInReview(nil)

		r := wantCode(t, w.call("review submit", rev, []string{ep}, map[string]string{
			"verdict": "pass", "head": shaA, "body": "The PRD holds.", "readers": readers}), cli.CodeShapeRefused)
		if r.Rule != "readers" {
			t.Fatalf("--readers %s: rule %q, want readers", readers, r.Rule)
		}
		if it := w.show(ep); it.Status != PRDReview || len(log.calls) != 0 {
			t.Fatalf("--readers %s: the refusal wrote: %s %v", readers, it.Status, log.calls)
		}
	}
}

// C6: a code-reviewer submit with --readers fails with SHAPE_REFUSED rule
// readers_scope, and one with no --readers behaves as today.
func TestC6ReadersOnATicketAreRefused(t *testing.T) {
	w := newWorld(t)
	log := &commentLog{}
	w.m.SetReviewCommenter(log.post)
	w.readersOf("unslop", "asd-ste100", "content-lens")
	id, rev := w.inReview()

	r := wantCode(t, w.call("review submit", rev, []string{id}, map[string]string{
		"verdict": "reject", "head": shaA, "body": "Fix the test.", "readers": "unslop,asd-ste100,content-lens"}), cli.CodeShapeRefused)
	if r.Rule != "readers_scope" {
		t.Fatalf("rule %q, want readers_scope", r.Rule)
	}
	if it := w.show(id); it.Status != InReview || len(log.calls) != 0 {
		t.Fatalf("the refusal wrote: %s %v", it.Status, log.calls)
	}

	mustOK(t, w.call("review submit", rev, []string{id}, map[string]string{"verdict": "reject", "head": shaA, "body": "Fix the test."}))
	if it := w.show(id); it.Status != Todo || len(log.calls) != 1 || strings.Contains(log.calls[0], "Readers:") {
		t.Fatalf("a ticket verdict with no --readers: %s %v", it.Status, log.calls)
	}
}
