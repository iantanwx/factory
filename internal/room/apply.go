package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// DecisionFile is what `room apply --file` sends: what the operator
// decided, and the operator's own words that decided it. A room applies
// only what a person decided, so a file with no statement is refused.
type DecisionFile struct {
	// Decision is the decision, as the room agent states it.
	Decision string `json:"decision"`
	// Statement quotes the operator's own words. It is required.
	Statement string `json:"statement"`
	// Amend says the decision amends the item's task, so it enters the
	// item's inputs (R10). Optional.
	Amend bool `json:"amend,omitempty"`
}

// ParseDecisionFile reads a decision file. Unknown fields are refused.
func ParseDecisionFile(raw string) (DecisionFile, *cli.Refusal) {
	var f DecisionFile
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, &cli.Refusal{Code: cli.CodeUsage, Rule: "bad_decision_file",
			Hint: fmt.Sprintf("the decision file is one JSON object with decision, statement and an optional amend: %v", err)}
	}
	f.Decision, f.Statement = strings.TrimSpace(f.Decision), strings.TrimSpace(f.Statement)
	if f.Statement == "" {
		return f, &cli.Refusal{Code: cli.CodeNoOperatorStatement, Rule: "no_operator_statement",
			Hint: "the decision file cites no operator statement; quote the operator's own words in statement, or apply nothing"}
	}
	if f.Decision == "" {
		return f, &cli.Refusal{Code: cli.CodeUsage, Rule: "no_decision", Hint: "the decision file states no decision"}
	}
	return f, nil
}

// apply is `loop room apply`: the room's own token applies the decision
// file. An escalated item, or a fired decision, takes the decision as its
// answer, and the item resumes. The room then reads applied, and its hold
// drops. The room stays open until the coordinator closes it.
func (s *Service) apply(ctx context.Context, call *rpc.Call) cli.Result {
	p := call.Principal
	r, err := s.Get(ctx, call.Inv.Arg("room"))
	if errors.Is(err, sql.ErrNoRows) {
		return refuse(cli.CodeNotFound, "no_room", "no room "+call.Inv.Arg("room"))
	}
	if err != nil {
		return internalErr(err)
	}
	if p.Kind != cli.PrincipalRoom || p.RunID == "" || p.RunID != r.RunID {
		return refuse(cli.CodeCapabilityDenied, "other_room", "a room applies its own decision only, with its current token")
	}
	switch r.State {
	case Live, Idle:
	case Applied:
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "already_applied", Item: r.Item,
			Hint: "the room applied its decision already; the coordinator closes it"})
	default:
		return cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "room_not_live", Item: r.Item,
			Hint: fmt.Sprintf("room %s is %s", r.ID, r.State)})
	}
	content, ok := cli.FileContent(call.Inv)
	if !ok {
		return refuse(cli.CodeUsage, "no_file", "--file names the room's decision file")
	}
	if res := s.secretIn("decision file", content); res != nil {
		return *res
	}
	f, ref := ParseDecisionFile(content)
	if ref != nil {
		return cli.Refuse(*ref)
	}
	answered := false
	if r.Item != "" {
		it, ref := s.cfg.Machine.Show(ctx, r.Project, r.Item)
		if ref != nil {
			return cli.Refuse(*ref)
		}
		if it.Status == machine.Escalated || it.Kind == machine.KindDecision && it.Status == machine.NeedsHuman {
			answer := f.Decision + "\n\nThe operator said: " + f.Statement
			params := map[string]string{"answer": answer}
			if f.Amend && it.Kind != machine.KindDecision {
				params["amendment"] = f.Decision
			}
			res := s.cfg.Machine.Apply(ctx, machine.Request{Project: r.Project, Item: r.Item, Verb: "answer", Params: params,
				Actor: machine.Actor{Name: p.Actor(), RunID: p.RunID}, ExpectedStatus: it.Status})
			if res.Refusal != nil {
				return res
			}
			answered = true
		}
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return internalErr(err)
	}
	if err := s.setState(ctx, r.ID, Applied, func(tx *store.Tx) error {
		if _, err := tx.Exec(`UPDATE rooms SET decision = ? WHERE id = ?`, string(raw), r.ID); err != nil {
			return err
		}
		if answered {
			if err := escalation.MarkAnswered(tx, r.Project, r.Item); err != nil {
				return err
			}
		}
		return dropHold(tx, r.Project, r.Item, r.ID)
	}); err != nil {
		return internalErr(err)
	}
	s.changed()
	return cli.Found(map[string]any{"room": r.ID, "state": Applied, "answered": answered,
		"next": "the coordinator closes the room with `loop room close`"})
}
