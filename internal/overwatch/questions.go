package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
)

// questionsPart reads the escalations that the fixer's panel sent to a
// person, and the questions the coordinator prepared (C12). An escalation
// whose entry holds no unanswered question is work: the coordinator
// prepares one. A prepared question waits on the operator, so it reads
// code 0. An escalation in awaiting_panel belongs to stalls.
func questionsPart() Part {
	return Part{Name: "questions", Command: "loop inbox list", Read: readQuestions}
}

// personEscalation is one escalation in state person.
type personEscalation struct {
	ID      int64
	Project string
	Item    string
}

// preparedQuestion is one question the coordinator prepared about an
// inbox entry.
type preparedQuestion struct {
	ID       int64
	Entry    string
	Answered bool
}

func readQuestions(ctx context.Context, w World) (Reading, error) {
	st := w.Store()
	if st == nil {
		return Reading{}, errors.New("no store to read escalations and questions from")
	}
	var (
		escs []personEscalation
		qs   []preparedQuestion
	)
	err := st.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id, project, item FROM escalations WHERE state = ? ORDER BY id`, escalation.StatePerson)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var e personEscalation
			if err := rs.Scan(&e.ID, &e.Project, &e.Item); err != nil {
				return err
			}
			escs = append(escs, e)
		}
		if err := rs.Err(); err != nil {
			return err
		}
		qrs, err := tx.QueryContext(ctx, `SELECT id, entry, answered_at IS NOT NULL FROM questions WHERE entry != '' ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = qrs.Close() }()
		for qrs.Next() {
			var q preparedQuestion
			if err := qrs.Scan(&q.ID, &q.Entry, &q.Answered); err != nil {
				return err
			}
			qs = append(qs, q)
		}
		return qrs.Err()
	})
	if err != nil {
		return Reading{}, fmt.Errorf("read the escalations and questions: %w", err)
	}
	escalated := map[string]bool{}
	read := map[string]bool{}
	for _, e := range escs {
		if read[e.Project] {
			continue
		}
		read[e.Project] = true
		items, err := w.Items(ctx, e.Project, machine.ListFilter{Status: machine.Escalated})
		if err != nil {
			return Reading{}, fmt.Errorf("read the escalated items of %s: %w", e.Project, err)
		}
		for _, it := range items {
			escalated[escalationEntry(e.Project, it.ID)] = true
		}
	}
	return classifyQuestions(escs, qs, escalated), nil
}

// escalationEntry is the inbox entry of an escalated item, as `loop inbox
// list` prints it.
func escalationEntry(project, item string) string {
	return inbox.KindEscalation + "/" + project + "/" + item
}

// classifyQuestions is the questions part's rule. escalated holds the
// entry of each item that stands in escalated. An escalation whose item
// left escalated has no entry to prepare a question for, so it prints a
// note. Otherwise an unanswered question about its entry waits on the
// operator, and its absence is work.
func classifyQuestions(escs []personEscalation, qs []preparedQuestion, escalated map[string]bool) Reading {
	var r Reading
	for _, e := range escs {
		entry := escalationEntry(e.Project, e.Item)
		if !escalated[entry] {
			r.Notes = append(r.Notes, fmt.Sprintf("escalation %d on %s went to a person, and the item no longer stands in escalated, so no entry waits",
				e.ID, e.Item))
			continue
		}
		var open, answered []int64
		for _, q := range qs {
			if q.Entry != entry {
				continue
			}
			if q.Answered {
				answered = append(answered, q.ID)
			} else {
				open = append(open, q.ID)
			}
		}
		if len(open) > 0 {
			r.Notes = append(r.Notes, fmt.Sprintf("escalation %d on %s waits on the operator: question %d is prepared for %s and unanswered",
				e.ID, e.Item, open[0], entry))
			continue
		}
		key := Key("questions", fmt.Sprintf("escalation-%d", e.ID))
		line := fmt.Sprintf("escalation %d on %s waits for a person, and %s holds no prepared question: run `loop inbox prepare --entry %s` (%s)",
			e.ID, e.Item, entry, entry, key)
		if len(answered) > 0 {
			line = fmt.Sprintf("escalation %d on %s waits for a person, and the operator answered question %d about %s: relay it with `loop inbox answer %s`, or prepare a new question (%s)",
				e.ID, e.Item, answered[len(answered)-1], entry, entry, key)
		}
		r.Findings = append(r.Findings, Finding{Key: key, Line: line})
	}
	return r
}
