package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

func filter(es []Entry, awaits string) []Entry {
	if awaits == "" {
		return es
	}
	out := es[:0]
	for _, e := range es {
		if e.Awaits == awaits {
			out = append(out, e)
		}
	}
	return out
}

// list is `loop inbox list`.
func (in *Inbox) list(ctx context.Context, call *rpc.Call) cli.Result {
	es, err := in.Entries(ctx)
	if err != nil {
		return cli.Internal(err)
	}
	return cli.List(filter(es, call.Inv.Flags["awaits"]))
}

// Since returns the entries newer than the cursor. An empty cursor returns
// every entry.
func (in *Inbox) Since(ctx context.Context, cursor string) ([]Entry, error) {
	var since int64 = -1
	if cursor != "" {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("the cursor %q is not one `inbox list` or `inbox wait` printed", cursor)
		}
		since = n
	}
	es, err := in.Entries(ctx)
	if err != nil {
		return nil, err
	}
	out := es[:0]
	for _, e := range es {
		if e.At > since {
			out = append(out, e)
		}
	}
	return out, nil
}

// waitVerb is `loop inbox wait --since`: it answers the entries newer than
// the cursor, or it holds the call until one arrives. When the wait budget
// runs out first it answers exit 4 with the same cursor, and the
// coordinator calls again.
func (in *Inbox) waitVerb(ctx context.Context, call *rpc.Call) cli.Result {
	cursor := call.Inv.Flags["since"]
	deadline := in.cfg.Now().Add(WaitBudget)
	if d, ok := ctx.Deadline(); ok && d.Add(-waitMargin).Before(deadline) {
		deadline = d.Add(-waitMargin)
	}
	for {
		wake := in.woken()
		es, err := in.Since(ctx, cursor)
		if err != nil {
			return cli.Reject(cli.CodeUsage, "bad_cursor", err.Error())
		}
		if len(es) > 0 {
			return cli.Found(map[string]any{"entries": es, "cursor": es[len(es)-1].Cursor})
		}
		left := time.Until(deadline)
		if left <= 0 {
			return cli.Result{Exit: cli.ExitWait, Data: map[string]any{"entries": []Entry{}, "cursor": cursor,
				"next": "nothing new arrived; call `loop inbox wait` again with the same cursor"}}
		}
		t := time.NewTimer(min(left, in.cfg.PollInterval))
		select {
		case <-ctx.Done():
			t.Stop()
			return cli.Result{Exit: cli.ExitWait, Data: map[string]any{"entries": []Entry{}, "cursor": cursor}}
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// answer is `loop inbox answer`: the operator's answer, as the coordinator
// relays it. An escalated item resumes to its stored target once each
// question prepared about its entry has an answer (D17), and an answer
// with --amend enters the item's inputs, so a twice-failed item is
// admitted again (R10). An asked bug report returns to reported with its
// reporter's answer. A fired decision settles as answered. A sign-off
// merges the document at the head its entry named. A question records the
// answer, and an alert closes.
func (in *Inbox) answer(ctx context.Context, call *rpc.Call) cli.Result {
	id, body := call.Inv.Arg("entry"), strings.TrimSpace(call.Inv.Flags["body"])
	if body == "" {
		return cli.Reject(cli.CodeUsage, "no_body", "--body holds the operator's answer")
	}
	if r := in.cfg.Redactor.Refusal("secret_in_answer", "answer", body); r != nil {
		return r.Result()
	}
	if strings.HasPrefix(id, KindFault+"/") {
		f := strings.TrimPrefix(id, KindFault+"/")
		return cli.Reject(cli.CodeUsage, "fault_entry", fmt.Sprintf("a fault entry settles with `loop fault ack %s` and `loop fault resolve %s`, not with an answer", f, f))
	}
	kind, project, item, row, ok := parseEntry(id)
	if !ok {
		return cli.Reject(cli.CodeUsage, "bad_entry", fmt.Sprintf("%q is no entry id that `loop inbox list` prints", id))
	}
	actor := machine.Actor{Name: call.Principal.Actor(), RunID: call.Principal.RunID}
	switch kind {
	case KindEscalation, KindDecision:
		params := map[string]string{"answer": body}
		if call.Inv.Flags["amend"] == "true" {
			if kind != KindEscalation {
				return cli.Reject(cli.CodeUsage, "amend_decision", "a decision has no task inputs; --amend applies to an escalated item")
			}
			params["amendment"] = body
		}
		want := machine.Escalated
		if kind == KindDecision {
			want = machine.NeedsHuman
		} else {
			var open []string
			if err := in.cfg.Store.Read(ctx, func(tx *sql.Tx) (err error) {
				open, err = openQuestions(ctx, tx, id)
				return err
			}); err != nil {
				return cli.Internal(err)
			}
			if len(open) > 0 {
				return cli.Reject(cli.CodeTransitionNotAllowed, "question_open",
					"answer each open question about "+id+" first: "+questionCommands(open))
			}
		}
		res := in.cfg.Machine.Apply(ctx, machine.Request{Project: project, Item: item, Verb: machine.VerbAnswer, Params: params,
			Actor: actor, ExpectedStatus: want})
		if res.Refusal != nil {
			return res
		}
		if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			if err := escalation.MarkAnswered(tx, project, item); err != nil {
				return err
			}
			return clearDefer(tx, id)
		}); err != nil {
			return cli.Internal(err)
		}
		in.Changed()
		return res
	case KindAsk:
		if call.Inv.Flags["amend"] == "true" {
			return cli.Reject(cli.CodeUsage, "amend_ask", "an ask's answer is a fact of the report, not a change of the task; --amend applies to an escalated item")
		}
		res := in.cfg.Machine.Apply(ctx, machine.Request{Project: project, Item: item, Verb: machine.VerbAnswer,
			Params: map[string]string{"answer": body}, Actor: actor, ExpectedStatus: machine.Asked})
		if res.Refusal != nil {
			return res
		}
		if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error { return clearDefer(tx, id) }); err != nil {
			return cli.Internal(err)
		}
		in.Changed()
		return res
	case KindQuestion:
		var n int64
		if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			r, err := tx.Exec(`UPDATE questions SET answer = ?, answered_at = ? WHERE id = ? AND answered_at IS NULL`,
				body, in.cfg.Now().UnixMilli(), row)
			if err != nil {
				return err
			}
			n, _ = r.RowsAffected()
			return clearDefer(tx, id)
		}); err != nil {
			return cli.Internal(err)
		}
		if n == 0 {
			return cli.Reject(cli.CodeNotFound, "no_question", fmt.Sprintf("no open question %d", row))
		}
		in.Changed()
		return cli.Found(map[string]any{"entry": id, "answered": true})
	case KindAlert:
		var n int64
		if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			r, err := tx.Exec(`UPDATE alerts SET state = 'closed', closed_at = ? WHERE id = ? AND state = 'open'`,
				in.cfg.Now().UnixMilli(), row)
			if err != nil {
				return err
			}
			n, _ = r.RowsAffected()
			if _, err := tx.Exec(`INSERT INTO audit (at, actor, action, detail) VALUES (?, ?, 'alert_answered', json_object('alert', ?, 'answer', ?))`,
				in.cfg.Now().UnixMilli(), actor.Name, row, body); err != nil {
				return err
			}
			return clearDefer(tx, id)
		}); err != nil {
			return cli.Internal(err)
		}
		if n == 0 {
			return cli.Reject(cli.CodeNotFound, "no_alert", fmt.Sprintf("no open alert %d", row))
		}
		in.Changed()
		return cli.Found(map[string]any{"entry": id, "closed": true})
	case KindSignoff:
		return in.signoff(ctx, call, id, project, item, body)
	case KindArchitecture:
		return cli.Reject(cli.CodeUsage, "architecture_entry",
			"an architecture entry ends when a ticket is filed under the epic or a room opens on it, not with an answer: "+archActs(project, item))
	case KindPanel:
		return cli.Reject(cli.CodeTransitionNotAllowed, "panel_pending",
			"the fixer's panel routes this escalation; a person answers it only after the panel sends it to a person")
	}
	return cli.Reject(cli.CodeTransitionNotAllowed, "room_entry", "a room is settled with `loop room apply` from the room, and `loop room close`")
}

func clearDefer(tx *store.Tx, entry string) error {
	_, err := tx.Exec(`UPDATE schedule SET state = 'done', updated_at = ? WHERE kind = ? AND stage = ? AND state = 'active'`,
		tx.Now().UnixMilli(), DeferKind, entry)
	return err
}

// deferVerb is `loop inbox defer --until`: the entry leaves the inbox and
// returns at that time. The time lives in SQLite's schedule table, never
// in beads' deferred status, so it holds across a restart. A deferred
// decision also goes back to pending, and decision-timers fires it again
// at that time.
func (in *Inbox) deferVerb(ctx context.Context, call *rpc.Call) cli.Result {
	id := call.Inv.Arg("entry")
	until, err := time.Parse(time.RFC3339, call.Inv.Flags["until"])
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_until", "--until is an RFC 3339 time")
	}
	if !until.After(in.cfg.Now()) {
		return cli.Reject(cli.CodeUsage, "until_past", "--until must be in the future")
	}
	kind, project, item, _, ok := parseEntry(id)
	if !ok {
		return cli.Reject(cli.CodeUsage, "bad_entry", fmt.Sprintf("%q is no entry id that `loop inbox list` prints", id))
	}
	es, err := in.Entries(ctx)
	if err != nil {
		return cli.Internal(err)
	}
	found := false
	for _, e := range es {
		if e.ID == id {
			found = true
			break
		}
	}
	if !found {
		return cli.Reject(cli.CodeNotFound, "no_entry", fmt.Sprintf("the inbox holds no entry %s now", id))
	}
	if kind == KindDecision {
		res := in.cfg.Machine.Apply(ctx, machine.Request{Project: project, Item: item, Verb: machine.VerbDefer,
			Actor: machine.Actor{Name: call.Principal.Actor(), RunID: call.Principal.RunID}, ExpectedStatus: machine.NeedsHuman})
		if res.Refusal != nil {
			return res
		}
	}
	due := until.UnixMilli()
	if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if err := clearDefer(tx, id); err != nil {
			return err
		}
		now := tx.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO schedule (project, kind, item, stage, due_at, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'active', ?, ?)`, project, DeferKind, item, id, due, now, now)
		return err
	}); err != nil {
		return cli.Internal(err)
	}
	in.Changed()
	return cli.Found(map[string]any{"entry": id, "until": until.UTC().Format(time.RFC3339)})
}

// prepare is `loop inbox prepare`: a question the coordinator wrote for the
// operator, with its options and their costs. It waits on a person.
func (in *Inbox) prepare(ctx context.Context, call *rpc.Call) cli.Result {
	body := strings.TrimSpace(call.Inv.Flags["body"])
	if body == "" {
		return cli.Reject(cli.CodeUsage, "no_body", "--body holds the question, its options and their costs")
	}
	if r := in.cfg.Redactor.Refusal("secret_in_question", "question", body); r != nil {
		return r.Result()
	}
	about := call.Inv.Flags["entry"]
	if about != "" {
		if _, _, _, _, ok := parseEntry(about); !ok {
			return cli.Reject(cli.CodeUsage, "bad_entry", fmt.Sprintf("%q is no entry id that `loop inbox list` prints", about))
		}
	}
	var id int64
	if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		r, err := tx.Exec(`INSERT INTO questions (entry, body, run_id, created_at) VALUES (?, ?, ?, ?)`,
			about, body, call.Principal.RunID, in.cfg.Now().UnixMilli())
		if err != nil {
			return err
		}
		id, err = r.LastInsertId()
		return err
	}); err != nil {
		return cli.Internal(err)
	}
	in.Changed()
	return cli.Found(map[string]any{"entry": rowEntry(KindQuestion, id), "awaits": AwaitsPerson})
}

// note is `loop inbox note`: the root cause of an open alert, as the
// coordinator recorded it from its root-cause subagent. The alert stays
// open until an answer closes it. A later note replaces the finding. The
// row keeps its count and last_at at the note, and the entry keeps that
// time, so a repeat of the same fault rings no one.
func (in *Inbox) note(ctx context.Context, call *rpc.Call) cli.Result {
	id, body := call.Inv.Arg("entry"), strings.TrimSpace(call.Inv.Flags["body"])
	if body == "" {
		return cli.Reject(cli.CodeUsage, "no_body", "--body holds the finding: the root cause, its evidence and the act that ends it")
	}
	if r := in.cfg.Redactor.Refusal("secret_in_finding", "finding", body); r != nil {
		return r.Result()
	}
	kind, _, _, row, ok := parseEntry(id)
	if !ok || kind != KindAlert {
		return cli.Reject(cli.CodeUsage, "not_alert", fmt.Sprintf("%q is no alert entry; only an alert holds a finding", id))
	}
	var n int64
	if err := in.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		now := in.cfg.Now().UnixMilli()
		r, err := tx.Exec(`UPDATE alerts SET finding = ?, finding_run = ?, finding_at = ?, finding_count = count, finding_last_at = last_at
			WHERE id = ? AND state = 'open'`, body, call.Principal.RunID, now, row)
		if err != nil {
			return err
		}
		if n, _ = r.RowsAffected(); n == 0 {
			return nil
		}
		_, err = tx.Exec(`INSERT INTO audit (at, actor, action, detail) VALUES (?, ?, 'alert_noted', json_object('alert', ?, 'finding', ?))`,
			now, call.Principal.Actor(), row, body)
		return err
	}); err != nil {
		return cli.Internal(err)
	}
	if n == 0 {
		return cli.Reject(cli.CodeNotFound, "no_alert", fmt.Sprintf("no open alert %d", row))
	}
	in.Changed()
	return cli.Found(map[string]any{"entry": id, "found": true})
}

// Question reads one prepared question and its answer.
func (in *Inbox) Question(ctx context.Context, id int64) (body, answer string, err error) {
	err = in.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT body, answer FROM questions WHERE id = ?`, id).Scan(&body, &answer)
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = fmt.Errorf("no question %d", id)
	}
	return body, answer, err
}
