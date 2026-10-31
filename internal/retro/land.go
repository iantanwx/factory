package retro

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// FactLearning is the fact of a ticket or a bug that a landed learning
// made: the learning's id. It takes the place of the operator's request,
// because the operator's standing word lands a passed learning (US-04).
const FactLearning = "retro_learning"

// landActor is the actor of each item a landing makes.
var landActor = machine.Server("retro")

// Land makes what the landed learning l of r becomes, and returns its id.
// A project_policy, userspace_skill or pack_pr learning becomes a PRD-less
// ticket in todo, a project_ticket learning a bug in reported, and a
// loop_fault learning a loop fault that the host files. A ticket or a bug
// carries the create key retro/<learning id>, so a repeat finds the item
// the first call made. The caller records a fault's id in landed_ref,
// which keeps a repeat from filing it again.
func (w World) Land(ctx context.Context, r Retro, l Stored) (string, *cli.Refusal) {
	if l.Target == LoopFault {
		if w.Faults == nil {
			return "", cli.InternalRefusal(fmt.Errorf("retro: no fault service files the loop fault of %s", l.ID))
		}
		f, err := w.Faults.Report(ctx, rpc.Principal{Kind: cli.PrincipalHost}, faultText(r, l))
		if err != nil {
			return "", cli.InternalRefusal(err)
		}
		return f.ID, nil
	}
	var answers []Answer
	if err := w.Store.Read(ctx, func(tx *sql.Tx) error {
		n, _ := parseLearningID(l.ID)
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT panel FROM learnings WHERE id = ?`, n).Scan(&raw); err != nil {
			return err
		}
		var p Panel
		err := json.Unmarshal([]byte(raw), &p)
		answers = p.Answers
		return err
	}); err != nil {
		return "", cli.InternalRefusal(err)
	}
	kind, verb := machine.KindTicket, machine.VerbCreate
	if l.Target == ProjectTicket {
		kind, verb = machine.KindBug, machine.VerbFile
	}
	key := "retro/" + l.ID
	found, ref := w.Machine.List(ctx, r.Project, machine.ListFilter{Kind: kind, Facts: map[string]string{machine.FactCreateKey: key}})
	if ref != nil {
		return "", ref
	}
	var it machine.Item
	if len(found) > 0 {
		it = found[0]
	} else {
		id, ref := w.Machine.CreateBead(ctx, machine.CreateRequest{Project: r.Project, Kind: kind,
			Title: fmt.Sprintf("Retro %s: %s", l.ID, strings.SplitN(l.Statement, "\n", 2)[0]), Description: itemBody(r, l, answers),
			Facts: map[string]string{machine.FactCreateKey: key, FactLearning: l.ID}})
		if ref != nil {
			return "", ref
		}
		it = machine.Item{ID: id, Status: machine.Initial}
	}
	if it.Status == machine.Initial {
		res := w.Machine.Apply(ctx, machine.Request{Project: r.Project, Item: it.ID, Verb: verb, ExpectedStatus: machine.Initial, Actor: landActor})
		if res.Refusal != nil {
			return "", res.Refusal
		}
	}
	return it.ID, nil
}

// itemBody is the task text of the ticket or the bug a learning becomes:
// the learning, its target, its cases and, where Q3 applies, each judge's
// change size. A userspace skill ticket names both skill roots of ADR 0013.
func itemBody(r Retro, l Stored, answers []Answer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Learning\n\n%s\n\nThe retro of %s landed learning %s at %s.\n\n## Cases\n\n", l.Statement, r.Item, l.ID, l.Target)
	for _, c := range l.Cases {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	if targets[l.Target].sizeApplies {
		b.WriteString("\n## Change size\n\n")
		for _, a := range answers {
			fmt.Fprintf(&b, "- %s: %s. %s\n", a.Judge, a.Change, a.Reason)
		}
	}
	if l.Target == UserspaceSkill {
		b.WriteString("\n## Skill\n\nThe learning names a userspace skill. By ADR 0013 its directory is `.claude/skills/<name>` or " +
			"`.agents/skills/<name>`. Edit the one that git tracks. The server refuses a push under the directory of a pinned skill, " +
			"so escalate that refusal.\n")
	}
	return b.String()
}

// faultText is the report of a loop fault learning: its id, the retro's
// project and item, the learning and each case.
func faultText(r Retro, l Stored) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The retro of %s/%s landed learning %s as a loop fault.\n\n%s\n\nCases:\n", r.Project, r.Item, l.ID, l.Statement)
	for _, c := range l.Cases {
		fmt.Fprintf(&b, "- %s\n", c)
	}
	return b.String()
}
