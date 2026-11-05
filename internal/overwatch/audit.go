package overwatch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// auditPart reads the newest base-branch run of the audit workflow that
// each project's config names. A failed or stale one is work (spec, C25).
var auditPart = Part{Name: "audit", Read: readAudit}

// AuditStale is the age past which the newest audit run means its
// schedule stopped. The audit is daily (STALE_HOURS, overwatch-audit.ts,
// alpha).
const AuditStale = 36 * time.Hour

func readAudit(ctx context.Context, w World) (Reading, error) {
	if w.GitHub() == nil {
		return noGitHub, nil
	}
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	for _, p := range projects {
		snap, err := w.ProjectConfig(ctx, p.ID)
		if err != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("UNREAD: %s: read the project config: %v", p.ID, err))
			continue
		}
		wf := ""
		if snap != nil && snap.Config != nil {
			wf = snap.Config.Health.AuditWorkflow
		}
		if wf == "" {
			r.Notes = append(r.Notes, fmt.Sprintf("%s: project config names no health.audit_workflow, so no audit is read", p.ID))
			continue
		}
		runs, err := w.GitHub().WorkflowRuns(ctx, p, github.RunQuery{Workflow: wf, Branch: p.BaseBranch()})
		var api *github.APIError
		switch {
		case errors.As(err, &api) && api.Status == http.StatusNotFound:
			r.Notes = append(r.Notes, fmt.Sprintf("UNREAD: %s: GitHub has no workflow %s in %s (404). Correct health.audit_workflow",
				p.ID, wf, p.Repo))
			continue
		case err != nil:
			r.Notes = append(r.Notes, unreadLine(p, "the runs of "+wf, err))
			continue
		}
		r.add(classifyAudit(p, wf, runs, w.Now()))
	}
	return r, nil
}

// classifyAudit reads the newest completed base-branch run of the audit
// workflow wf. A red conclusion is work, and so is a run older than
// AuditStale, since the schedule then stopped. No completed run at all is
// neither, so it reads as a note (spec, C25).
func classifyAudit(p config.Project, wf string, runs []github.RunView, now time.Time) Reading {
	base := p.BaseBranch()
	runs = slices.DeleteFunc(slices.Clone(runs), func(run github.RunView) bool {
		return run.Branch != base || run.Workflow != wf || run.Status != "completed"
	})
	if len(runs) == 0 {
		return Reading{Notes: []string{fmt.Sprintf("%s: %s has no completed run on %s yet", p.ID, wf, base)}}
	}
	slices.SortStableFunc(runs, newestFirst)
	run := runs[0]
	key := Key("audit", p.ID, fmt.Sprintf("%s@%d", Slug(wf), run.ID))
	age := now.Sub(run.CreatedAt).Truncate(time.Minute)
	switch {
	case redRun(run.Conclusion):
		return Reading{Findings: []Finding{{Key: key, Project: p.ID,
			Line: fmt.Sprintf("%s: the newest %s run %d on %s concluded %s. Read its log. %s", p.ID, wf, run.ID, base, run.Conclusion, key)}}}
	case age > AuditStale:
		return Reading{Findings: []Finding{{Key: key, Project: p.ID,
			Line: fmt.Sprintf("%s: the newest %s run %d on %s started %s ago, past %s, so its schedule stopped. %s",
				p.ID, wf, run.ID, base, age, AuditStale, key)}}}
	}
	said := "concluded " + run.Conclusion
	if run.Conclusion == "success" {
		said = "passed"
	}
	return Reading{Notes: []string{fmt.Sprintf("%s: the newest %s run %d on %s %s %s ago", p.ID, wf, run.ID, base, said, age)}}
}
