package github

import (
	"context"
	"fmt"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// The rules a first-parent commit of the base fails (ADR 0003, G5).
const (
	ruleNoRecord       = "no_record"        // no item records the commit as its merge
	ruleOutran         = "outran"           // the document merged during its review or amendment
	ruleHeadMismatch   = "head_mismatch"    // the merge's second parent is not the reviewed head
	ruleNoReviewedHead = "no_reviewed_head" // the item held no reviewed head at its merge
)

// verdict is the judgement of one first-parent commit: a pass, a fail
// with its rule, or wait. Only wait stops the walk.
type verdict struct {
	rule string // the rule the commit fails, or "" for a pass
	wait bool   // no record, and the tick's read was partial
}

// mergeCheck answers the rule a recorded commit fails, or "".
type mergeCheck func(c Commit, rec machine.MergeRecord) string

// mergeRules are the checks of each record fact, in order. An adr_merged
// record passes with no check: no stage reviews an ADR, and the
// operator's merge is its sign-off (ADR 0012).
var mergeRules = map[string][]mergeCheck{
	machine.FactMergedSHA:  {headRule},
	machine.FactPRDMerged:  {outranRule, headRule},
	machine.FactSpecMerged: {outranRule, headRule},
	machine.FactADRMerged:  nil,
}

// outranRule fails a document that merged before its review passed,
// whatever its parents.
func outranRule(_ Commit, rec machine.MergeRecord) string {
	if rec.Outran {
		return ruleOutran
	}
	return ""
}

// headRule passes a merge commit whose second parent is the reviewed
// head. A squash leaves one parent and no head in git, so a one-parent
// commit passes when the item held a reviewed head.
func headRule(c Commit, rec machine.MergeRecord) string {
	switch {
	case rec.ReviewedHead == "":
		return ruleNoReviewedHead
	case len(c.Parents) > 1 && c.Parents[1] != rec.ReviewedHead:
		return ruleHeadMismatch
	}
	return ""
}

// judge reads one commit's record by the rule table. partial is true when
// the tick's pollPulls or adrs failed, so a merge they did not record
// waits rather than fails.
func judge(c Commit, rec machine.MergeRecord, ok, partial bool) (verdict, error) {
	if !ok {
		return verdict{rule: ruleNoRecord, wait: partial}, nil
	}
	checks, known := mergeRules[rec.Fact]
	if !known {
		return verdict{}, fmt.Errorf("github: commit %s: no rule for the record fact %q", c.SHA, rec.Fact)
	}
	for _, check := range checks {
		if rule := check(c, rec); rule != "" {
			return verdict{rule: rule}, nil
		}
	}
	return verdict{}, nil
}

// auditBase judges each first-parent commit of the base after the
// base_audit cursor, oldest first (ADR 0003, G5). A commit that fails
// raises one unreviewed_merge alert. The cursor moves past a commit once
// the store holds its alert, so a failed write stops the walk and the
// next tick judges that commit again. The first tick only writes head as
// the cursor. A head that does not reach the cursor, as after a forced
// push, raises one base_rewritten alert, and the cursor moves to head
// with no walk. partial is the joined error of pollPulls and adrs on this
// tick: then a commit with no record stops the walk before it. The step
// reads the mirror and the store, and sends no GitHub request.
func (r *Reactor) auditBase(ctx context.Context, p config.Project, head string, partial error) error {
	if head == "" {
		return nil
	}
	cursor, err := r.stream(ctx, p.ID, StreamBaseAudit)
	if err != nil {
		return err
	}
	if cursor == "" {
		return r.setStream(ctx, p.ID, StreamBaseAudit, head)
	}
	mirror := r.s.Mirror(p)
	reached, err := mirror.Reaches(ctx, head, cursor)
	if err != nil {
		return err
	}
	if !reached {
		if err := r.alert(ctx, rpc.AuditAlert{Actor: "server:reactor", Project: p.ID, Kind: AlertBaseRewritten,
			Subject: p.ID + "@" + cursor,
			Body: fmt.Sprintf("The base of %s no longer holds the audited commit %s. Its head is now %s, and the audit starts there.",
				p.ID, short(cursor), short(head)),
			Detail: map[string]any{"cursor": cursor, "head": head}}); err != nil {
			return err
		}
		return r.setStream(ctx, p.ID, StreamBaseAudit, head)
	}
	shas, err := mirror.FirstParents(ctx, cursor, head)
	if err != nil {
		return err
	}
	for _, sha := range shas {
		c, err := mirror.Commit(ctx, sha)
		if err != nil {
			return err
		}
		rec, ok, err := r.m.MergeOf(ctx, p.ID, sha)
		if err != nil {
			return err
		}
		v, err := judge(c, rec, ok, partial != nil)
		if err != nil {
			return err
		}
		if v.wait {
			r.log.InfoContext(ctx, "the base audit waits at a commit with no record, because the tick's read was partial",
				"project", p.ID, "sha", sha, "partial", partial)
			return nil
		}
		if v.rule != "" {
			if err := r.alert(ctx, rpc.AuditAlert{Actor: "server:reactor", Project: p.ID, Kind: AlertUnreviewedMerge,
				Subject: p.ID + "@" + sha,
				Body: fmt.Sprintf("Commit %s on the base of %s fails the base audit by the rule %s: %q by %s.",
					short(sha), p.ID, v.rule, c.Subject, c.Author),
				Detail: map[string]any{"sha": sha, "rule": v.rule, "item": rec.Item, "fact": rec.Fact}}); err != nil {
				return err
			}
		}
		if err := r.setStream(ctx, p.ID, StreamBaseAudit, sha); err != nil {
			return err
		}
	}
	return nil
}
