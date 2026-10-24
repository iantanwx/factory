package escalation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// Sources of a recorded decision.
const (
	SourceAnswer   = "inbox answer"     // the operator's answer through the inbox
	SourceNote     = "coordinator note" // a note the coordinator wrote on the item
	SourceQuestion = "inbox question"   // a question the coordinator prepared about the item, and its answer
)

// RecordedDecision is one decision of the operator that the server
// recorded on an item.
type RecordedDecision struct {
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Actor  string    `json:"actor"`
	Text   string    `json:"text"`
}

// String is the decision as a judge reads it.
func (d RecordedDecision) String() string {
	return fmt.Sprintf("Recorded %s, %s by %s:\n\n%s", d.At.UTC().Format(time.RFC3339), d.Source, d.Actor, d.Text)
}

// decisions is `loop escalation decisions`: the operator's decisions
// recorded on the escalation's item, oldest first. They are the answers
// the inbox recorded on the item, the coordinator's notes on it, and each
// answered question the coordinator prepared about one of its entries.
// `escalation judge` hands them to both judges, so a question the operator
// settled does not reach a person again (F-26).
func (s *Service) decisions(ctx context.Context, call *rpc.Call) cli.Result {
	p, inv := call.Principal, call.Inv
	if p.Stage != FixerStage {
		return cli.Reject(cli.CodeCapabilityDenied, "fixer_only", "only a fixer run reads the decisions recorded for an escalation")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(inv.Arg("escalation")), 10, 64)
	if err != nil || id < 1 {
		return cli.Reject(cli.CodeUsage, "bad_escalation", "the escalation id is a positive number; `loop work peek --stage fixer` lists them")
	}
	rec, err := s.Get(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return cli.Reject(cli.CodeNotFound, "no_escalation", fmt.Sprintf("no escalation %d", id))
	}
	if err != nil {
		return cli.Internal(err)
	}
	if p.Project != "" && rec.Project != p.Project {
		return cli.Reject(cli.CodeCapabilityDenied, "other_project", "a fixer reads the escalations of its own project only")
	}
	out, err := s.Recorded(ctx, rec.Project, rec.Item)
	if err != nil {
		return cli.Internal(err)
	}
	if s.Redactor != nil {
		for i := range out {
			out[i].Text = s.Redactor.Redact(out[i].Text)
		}
	}
	return cli.Found(map[string]any{"escalation": id, "item": rec.Item, "decisions": out})
}

// Recorded reads the operator's decisions recorded on one item, oldest
// first.
func (s *Service) Recorded(ctx context.Context, project, item string) ([]RecordedDecision, error) {
	hist, err := s.Machine.History(ctx, project, item)
	if err != nil {
		return nil, err
	}
	out := []RecordedDecision{}
	for _, t := range hist {
		switch {
		case t.Verb == machine.VerbAnswer && t.Fact(machine.FactAnswer) != "":
			out = append(out, RecordedDecision{At: t.At, Source: SourceAnswer, Actor: t.Actor, Text: t.Fact(machine.FactAnswer)})
		case t.Verb == machine.VerbNote && strings.HasPrefix(t.Actor, cli.PrincipalCoordinator+":") && t.Fact(machine.FactNote) != "":
			out = append(out, RecordedDecision{At: t.At, Source: SourceNote, Actor: t.Actor, Text: t.Fact(machine.FactNote)})
		}
	}
	suffix := "/" + project + "/" + item
	err = s.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT entry, body, answer, run_id, answered_at FROM questions
			WHERE answered_at IS NOT NULL AND answer != '' AND entry != '' ORDER BY id`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				entry, body, answer, run string
				at                       int64
			)
			if err := rows.Scan(&entry, &body, &answer, &run, &at); err != nil {
				return err
			}
			// An item entry is <kind>/<project>/<item>.
			if strings.Count(entry, "/") != 2 || !strings.HasSuffix(entry, suffix) {
				continue
			}
			out = append(out, RecordedDecision{At: time.UnixMilli(at), Source: SourceQuestion, Actor: cli.PrincipalCoordinator + ":" + run,
				Text: "The question, about " + entry + ":\n\n" + body + "\n\nThe operator's answer:\n\n" + answer})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// RecordRefusal is the server's refusal of `escalation decisions`.
// `escalation judge` answers it as it is, so a server that is not ready
// still exits 4.
type RecordRefusal struct{ Result cli.Result }

func (r *RecordRefusal) Error() string { return r.Result.Refusal.Code + ": " + r.Result.Refusal.Hint }

// ServerRecorded is the production Recorded: it asks the server for
// `escalation decisions <escalation>` through remote.
func ServerRecorded(reg *cli.Registry, remote cli.Handler) func(ctx context.Context, escalation string) ([]string, error) {
	return func(ctx context.Context, escalation string) ([]string, error) {
		v, ok := reg.Verb("escalation decisions")
		if !ok {
			return nil, errors.New("this build has no escalation decisions verb")
		}
		res := remote(ctx, cli.Invocation{Verb: v, Args: []string{escalation}, Flags: map[string]string{}})
		if res.Refusal != nil {
			return nil, &RecordRefusal{Result: res}
		}
		raw, err := json.Marshal(res.Data)
		if err != nil {
			return nil, err
		}
		var body struct {
			Decisions []RecordedDecision `json:"decisions"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("the server answered decisions that do not parse: %w", err)
		}
		out := make([]string, 0, len(body.Decisions))
		for _, d := range body.Decisions {
			out = append(out, d.String())
		}
		return out, nil
	}
}
