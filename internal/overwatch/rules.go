package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// rulesPart reads each rule that a retro landed in one project and that
// no comparison with the other projects read yet (factorio-1xq0, C25): a
// learning in landed, at project_policy or userspace_skill, whose ticket
// is done and that holds no compared_at. Whether another project holds the
// same rule is judgement, so the coordinator compares, and
// `loop retro compared` ends the finding.
func rulesPart() Part { return Part{Name: "rules", Read: readRules} }

// landedRule is one uncompared learning and the ticket that landed it.
type landedRule struct {
	id     int64
	target string
	ticket string
}

func readRules(ctx context.Context, w World) (Reading, error) {
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the projects: %w", err)
	}
	if w.Store() == nil {
		return Reading{}, errors.New("no store to read the learnings from")
	}
	var r Reading
	for _, p := range projects {
		rules, err := uncomparedRules(ctx, w, p.ID)
		if err != nil {
			return Reading{}, fmt.Errorf("read the landed learnings of %s: %w", p.ID, err)
		}
		if len(rules) == 0 {
			continue
		}
		done, err := w.Items(ctx, p.ID, machine.ListFilter{Kind: machine.KindTicket, Status: machine.Done})
		if err != nil {
			r.Notes = append(r.Notes, unreadLine(p, "the done tickets", err))
			continue
		}
		r.add(classifyRules(p, rules, done))
	}
	return r, nil
}

// uncomparedRules reads the landed project_policy and userspace_skill
// learnings of project that hold no compared_at, oldest first.
func uncomparedRules(ctx context.Context, w World, project string) ([]landedRule, error) {
	var out []landedRule
	err := w.Store().Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id, target, landed_ref FROM learnings
			WHERE project = ? AND status = 'landed' AND target IN ('project_policy', 'userspace_skill') AND compared_at IS NULL
			ORDER BY id`, project)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var l landedRule
			if err := rs.Scan(&l.id, &l.target, &l.ticket); err != nil {
				return err
			}
			out = append(out, l)
		}
		return rs.Err()
	})
	return out, err
}

// classifyRules is the rules part's rule: one finding for each learning
// whose ticket is among the done tickets of p.
func classifyRules(p config.Project, rules []landedRule, done []machine.Item) Reading {
	merged := map[string]bool{}
	for _, it := range done {
		merged[it.ID] = true
	}
	var r Reading
	for _, l := range rules {
		if !merged[l.ticket] {
			continue
		}
		id := fmt.Sprintf("L-%d", l.id)
		key := Key("rules", p.ID, id)
		r.Findings = append(r.Findings, Finding{Key: key, Project: p.ID, Line: fmt.Sprintf(
			"learning %s of %s landed a %s rule in %s, which is done, and no comparison read it yet. Compare it with the other projects by section 9, then run `loop retro compared %s`: %s",
			id, p.ID, l.target, l.ticket, id, key)})
	}
	return r
}
