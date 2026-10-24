package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// workItemPart is the shipped part of that name.
func workItemPart(t *testing.T, name string) Part {
	t.Helper()
	i := slices.IndexFunc(Parts(), func(p Part) bool { return p.Name == name })
	if i < 0 {
		t.Fatalf("Parts() holds no part %q", name)
	}
	return Parts()[i]
}

// sweepWorkItemPart sweeps the shipped part of that name alone.
func sweepWorkItemPart(t *testing.T, w World, name string) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{workItemPart(t, name)})
	if len(r.Parts) != 1 {
		t.Fatalf("parts = %+v", r.Parts)
	}
	return r.Parts[0]
}

// escalate writes one escalation row on item in state.
func (w *fakeWorld) escalate(t *testing.T, project, item, state string) {
	t.Helper()
	err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO escalations (project, item, level, choice, finding, state, created_at, updated_at)
			VALUES (?, ?, 4, 'inside', 'the owner picks the name', ?, ?, ?)`, project, item, state, now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// prepare writes one question about entry, answered or not.
func (w *fakeWorld) prepare(t *testing.T, entry, body string, answered bool) {
	t.Helper()
	err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		var at sql.NullInt64
		if answered {
			at = sql.NullInt64{Int64: now, Valid: true}
		}
		_, err := tx.Exec(`INSERT INTO questions (entry, body, run_id, created_at, answer, answered_at) VALUES (?, ?, '', ?, '', ?)`,
			entry, body, now, at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// C12: an escalation in state person whose entry holds no prepared
// question is work. A prepared unanswered question waits on the operator,
// and an escalation in awaiting_panel belongs to stalls.
func TestC12AnEscalationForAPersonWithNoQuestionIsWork(t *testing.T) {
	const entry = "escalation/factorio/factorio-x1y"
	key := "overwatch:questions:escalation-1"
	escalated := func(w *fakeWorld) {
		w.add("factorio", machine.Item{ID: "factorio-x1y", Kind: machine.KindTicket, Status: machine.Escalated}, "")
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, w *fakeWorld)
		code  Code
		lines []string
	}{
		{"a person and no question", func(t *testing.T, w *fakeWorld) {
			escalated(w)
			w.escalate(t, "factorio", "factorio-x1y", escalation.StatePerson)
		}, Work, []string{"escalation 1 on factorio-x1y waits for a person, and " + entry +
			" holds no prepared question: run `loop inbox prepare --entry " + entry + "` (" + key + ")"}},
		{"a person and a prepared question with no answer", func(t *testing.T, w *fakeWorld) {
			escalated(w)
			w.escalate(t, "factorio", "factorio-x1y", escalation.StatePerson)
			w.prepare(t, entry, "Name it a or b?", false)
		}, Quiet, []string{"escalation 1 on factorio-x1y waits on the operator: question 1 is prepared for " + entry + " and unanswered"}},
		{"a person and an answered question only", func(t *testing.T, w *fakeWorld) {
			escalated(w)
			w.escalate(t, "factorio", "factorio-x1y", escalation.StatePerson)
			w.prepare(t, entry, "Name it a or b?", true)
		}, Work, []string{"escalation 1 on factorio-x1y waits for a person, and the operator answered question 1 about " + entry +
			": relay it with `loop inbox answer " + entry + "`, or prepare a new question (" + key + ")"}},
		{"a question about another entry", func(t *testing.T, w *fakeWorld) {
			escalated(w)
			w.escalate(t, "factorio", "factorio-x1y", escalation.StatePerson)
			w.prepare(t, "escalation/factorio/factorio-z9", "Name it a or b?", false)
		}, Work, nil},
		{"awaiting the panel", func(t *testing.T, w *fakeWorld) {
			escalated(w)
			w.escalate(t, "factorio", "factorio-x1y", escalation.StateAwaitingPanel)
		}, Quiet, []string{}},
		{"answered", func(t *testing.T, w *fakeWorld) {
			w.escalate(t, "factorio", "factorio-x1y", escalation.StateAnswered)
		}, Quiet, []string{}},
		{"a person, and the item left escalated", func(t *testing.T, w *fakeWorld) {
			w.add("factorio", machine.Item{ID: "factorio-x1y", Kind: machine.KindTicket, Status: machine.Canceled}, "")
			w.escalate(t, "factorio", "factorio-x1y", escalation.StatePerson)
		}, Quiet, []string{"escalation 1 on factorio-x1y went to a person, and the item no longer stands in escalated, so no entry waits"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newFakeWorld(t)
			c.setup(t, w)
			pr := sweepWorkItemPart(t, w, "questions")
			if pr.Code != c.code {
				t.Fatalf("code = %d, want %d: %q", pr.Code, c.code, pr.Lines)
			}
			if c.lines != nil && !slices.Equal(pr.Lines, c.lines) {
				t.Fatalf("lines\n got %q\nwant %q", pr.Lines, c.lines)
			}
		})
	}
}

// C12: a question that names the finding's key files it, whatever entry it
// was prepared for, and a failed read is code 2.
func TestC12AQuestionThatNamesTheKeyFilesItAndAFailedReadIsUnread(t *testing.T) {
	w := newFakeWorld(t)
	w.add("factorio", machine.Item{ID: "factorio-x1y", Kind: machine.KindTicket, Status: machine.Escalated}, "")
	w.escalate(t, "factorio", "factorio-x1y", escalation.StatePerson)
	w.prepare(t, "", "About overwatch:questions:escalation-1: name it a or b?", false)
	if pr := sweepWorkItemPart(t, w, "questions"); pr.Code != Quiet || !strings.HasSuffix(pr.Lines[0], "(filed in question 1)") {
		t.Fatalf("a question that names the key: %+v", pr)
	}
	w.itemsErr = errors.New("beads is down")
	if pr := sweepWorkItemPart(t, w, "questions"); pr.Code != Unread || !strings.Contains(pr.Lines[0], "beads is down") {
		t.Fatalf("a failed read: %+v", pr)
	}
}
