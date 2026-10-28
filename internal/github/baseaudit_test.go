package github

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// startAudit attaches a machine and ticks once, so the base audit's
// cursor sits at the seeded base.
func startAudit(t *testing.T) (*env, *Reactor) {
	t.Helper()
	e := newEnv(t)
	e.withWorld()
	r := e.reactor()
	e.tick(r)
	return e, r
}

// pushBase pushes the checkout's HEAD to the remote base, and returns it.
func (e *env) pushBase() string {
	e.t.Helper()
	gitT(e.t, e.checkout, "push", "-q", e.git.RemoteDir(), "HEAD:main")
	return gitT(e.t, e.checkout, "rev-parse", "HEAD")
}

// mergeBranch commits files on a new branch off HEAD and merges it into
// the work branch with a merge commit. It returns the branch head and the
// merge commit.
func (e *env) mergeBranch(name string, files map[string]string) (side, merge string) {
	e.t.Helper()
	gitT(e.t, e.checkout, "checkout", "-q", "-b", name)
	side = commit(e.t, e.checkout, name, files)
	gitT(e.t, e.checkout, "checkout", "-q", "work")
	gitT(e.t, e.checkout, "merge", "-q", "--no-ff", "-m", "Merge pull request "+name, name)
	return side, gitT(e.t, e.checkout, "rev-parse", "HEAD")
}

func (e *env) apply(item, verb string, params map[string]string) {
	e.t.Helper()
	mustOK(e.t, e.m.Apply(context.Background(), machine.Request{Project: testProject, Item: item, Verb: verb, Params: params,
		Actor: machine.Server("test")}))
}

// docMerged records the merge of an epic's document at sha, as the
// reactor's reads do: twice when the document outran its review.
func (e *env) docMerged(ep, sha string) {
	e.t.Helper()
	ev := machine.MergeEvent{Project: testProject, Item: ep, Base: "main", Into: "main", SHA: sha}
	from := e.status(ep).Status
	mustOK(e.t, e.m.MergeObserved(context.Background(), ev))
	if from != machine.PRDSignoff && from != machine.SpecSignoff {
		mustOK(e.t, e.m.MergeObserved(context.Background(), ev))
	}
}

type auditAlert struct {
	Subject, Body string
	Count         int
}

// unreviewed lists the unreviewed_merge alerts, oldest first.
func (e *env) unreviewed() []auditAlert {
	e.t.Helper()
	return e.alertsOf(AlertUnreviewedMerge)
}

// alertsOf lists the alerts of kind, oldest first.
func (e *env) alertsOf(kind string) []auditAlert {
	e.t.Helper()
	var out []auditAlert
	if err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT subject, body, count FROM alerts WHERE kind = ? ORDER BY id`, kind)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var a auditAlert
			if err := rows.Scan(&a.Subject, &a.Body, &a.Count); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	}); err != nil {
		e.t.Fatal(err)
	}
	return out
}

// auditCursor is the base audit's cursor, or "".
func (e *env) auditCursor() string {
	e.t.Helper()
	var c string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT cursor FROM github_cursors WHERE project = ? AND stream = 'base_audit'`, testProject).Scan(&c)
	})
	if err != nil && err != sql.ErrNoRows {
		e.t.Fatal(err)
	}
	return c
}

// wantAlerts fails unless the unreviewed_merge alerts name shas, in order,
// and each body states rule.
func (e *env) wantAlerts(rule string, shas ...string) {
	e.t.Helper()
	got := e.unreviewed()
	if len(got) != len(shas) {
		e.t.Fatalf("unreviewed_merge alerts %+v, want one for each of %v", got, shas)
	}
	for i, sha := range shas {
		if got[i].Subject != testProject+"@"+sha || !strings.Contains(got[i].Body, rule) {
			e.t.Fatalf("alert %d is %+v, want subject %s@%s and rule %s", i, got[i], testProject, sha, rule)
		}
	}
}

// failGit makes every git log of the service fail, so adrs finds no
// adding commit. It returns the step that undoes it.
func (e *env) failGit() func() {
	e.t.Helper()
	bin := filepath.Join(e.t.TempDir(), "git")
	writeFile(e.t, bin, "#!/bin/sh\nif [ \"$1\" = log ]; then echo 'log fails' >&2; exit 1; fi\nexec git \"$@\"\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		e.t.Fatal(err)
	}
	old := e.svc.git.Binary
	e.svc.git.Binary = bin
	return func() { e.svc.git.Binary = old }
}

// C8: the first tick audits nothing. With no cursor, the tick writes the
// base head as the cursor and raises no alert, though ten direct pushes
// hold no record.
func TestC8FirstTickAuditsNothing(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	for i := range 10 {
		commit(t, e.checkout, fmt.Sprintf("direct push %d", i), map[string]string{"README.md": fmt.Sprintf("%d\n", i)})
	}
	head := e.pushBase()
	r := e.reactor()
	e.tick(r)
	if a, c := e.unreviewed(), e.auditCursor(); len(a) != 0 || c != head {
		t.Fatalf("the first tick raised %+v and left the cursor at %q, want no alert and the cursor at %s", a, c, head)
	}
}

// C5: a commit with no merge record raises one unreviewed_merge alert
// with the rule no_record, and its body names the commit's subject and
// author. The next tick raises no second alert and does not count up the
// open one.
func TestC5CommitWithNoRecordRaisesOneAlertOnce(t *testing.T) {
	e, r := startAudit(t)
	gitT(t, e.checkout, "commit", "-q", "--allow-empty", "--author=Mallory <mallory@example.invalid>", "-m", "Push past the loop")
	sha := e.pushBase()
	e.tick(r)
	e.tick(r)
	e.wantAlerts("no_record", sha)
	a := e.unreviewed()[0]
	if a.Count != 1 || !strings.Contains(a.Body, "Push past the loop") || !strings.Contains(a.Body, "Mallory") {
		t.Fatalf("the alert is %+v, want count 1 and a body that names the subject and the author", a)
	}
}

// C6: each record passes only by its rule. An outran document merge
// fails whatever its parents. Any other recorded merge commit passes at
// its reviewed head, and a one-parent commit passes when a reviewed head
// is set. An adr_merged commit passes with no reviewed head.
func TestC6EachRecordPassesOnlyByItsRule(t *testing.T) {
	ctx := context.Background()
	// specInReview drives an epic whose PRD passed at shaA and merged to
	// spec_review.
	specInReview := func(e *env) string {
		ep := e.epicInReview(7)
		e.apply(ep, "review_pass", map[string]string{"head": shaA})
		e.docMerged(ep, shaM)
		_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: ep, Stage: machine.StageSpecAuthor, RunID: "run-spec"})
		mustOK(e.t, res)
		e.apply(ep, "pr_open", map[string]string{"pr": "8"})
		return ep
	}
	// amendedPRD drives a PRD through a pass at head, a person's comment
	// and an amendment back to prd_review. Its reviewed head stays head.
	amendedPRD := func(e *env, head string) string {
		ep := e.epicInReview(7)
		e.apply(ep, "review_pass", map[string]string{"head": head})
		e.apply(ep, machine.VerbPersonComment, map[string]string{"head": head})
		e.apply(ep, "prd_amended", map[string]string{"head": shaB})
		return ep
	}
	for _, c := range []struct {
		name, rule string
		land       func(e *env) string // records one commit and returns it
	}{
		{"direct ticket merge at its reviewed head", "", func(e *env) string {
			side, merge := e.mergeBranch("tk", map[string]string{"a.txt": "a\n"})
			e.apply(e.ticket(machine.ReadyToMerge, side), "merged", map[string]string{"sha": merge})
			return merge
		}},
		{"prd merge at its pinned head", "", func(e *env) string {
			side, merge := e.mergeBranch("prd", map[string]string{"docs/prd.md": "prd\n"})
			ep := e.epicInReview(7)
			e.apply(ep, "review_pass", map[string]string{"head": side})
			e.docMerged(ep, merge)
			return merge
		}},
		{"ticket merged from in_review", "no_reviewed_head", func(e *env) string {
			side, merge := e.mergeBranch("tk", map[string]string{"a.txt": "a\n"})
			e.apply(e.ticket(machine.InReview, side), "merged", map[string]string{"sha": merge})
			return merge
		}},
		{"ticket merge commit at another head", "head_mismatch", func(e *env) string {
			_, merge := e.mergeBranch("tk", map[string]string{"a.txt": "a\n"})
			e.apply(e.ticket(machine.ReadyToMerge, shaA), "merged", map[string]string{"sha": merge})
			return merge
		}},
		{"recorded squash ticket commit at a reviewed head", "", func(e *env) string {
			sq := commit(e.t, e.checkout, "squash", map[string]string{"a.txt": "a\n"})
			e.apply(e.ticket(machine.ReadyToMerge, shaA), "merged", map[string]string{"sha": sq})
			return sq
		}},
		{"spec squash-merged during its review after its prd passed", "outran", func(e *env) string {
			ep := specInReview(e)
			sq := commit(e.t, e.checkout, "spec", map[string]string{"docs/spec.md": "spec\n"})
			e.docMerged(ep, sq)
			return sq
		}},
		{"prd squash-merged during review after a pass, a comment and an amendment", "outran", func(e *env) string {
			ep := amendedPRD(e, shaA)
			sq := commit(e.t, e.checkout, "prd", map[string]string{"docs/prd.md": "prd\n"})
			e.docMerged(ep, sq)
			return sq
		}},
		{"outran merge commit at the stale reviewed head", "outran", func(e *env) string {
			side, merge := e.mergeBranch("prd", map[string]string{"docs/prd.md": "prd\n"})
			ep := amendedPRD(e, side)
			e.docMerged(ep, merge)
			return merge
		}},
		{"adr merge commit on an epic with no reviewed head", "", func(e *env) string {
			ep := e.architectureEpic("Share the skill pack")
			_, merge := e.mergeBranch("adr", map[string]string{adr0007: adrText(ep)})
			e.apply(ep, machine.VerbADRLanded, map[string]string{"path": adr0007, "added": merge})
			return merge
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, r := startAudit(t)
			sha := c.land(e)
			e.pushBase()
			e.tick(r)
			if c.rule == "" {
				e.wantAlerts("")
			} else {
				e.wantAlerts(c.rule, sha)
			}
			if got := e.auditCursor(); got != sha {
				t.Fatalf("the cursor is at %q, want %s", got, sha)
			}
		})
	}
}

// C7: an ADR change with no record raises one unreviewed_merge alert with
// the rule no_record, one tick after the epic's first ADR landed. No
// commit passes because of the paths its diff touches.
func TestC7ADRChangeWithNoRecordRaisesAnAlert(t *testing.T) {
	const second = "docs/adr/0008-second.md"
	for _, c := range []struct {
		name  string
		files func(ep, other string) map[string]string
	}{
		{"an edit of the landed ADR", func(ep, _ string) map[string]string {
			return map[string]string{adr0007: adrText(ep) + "\nAmended.\n"}
		}},
		{"a second ADR for an epic past architecture", func(ep, _ string) map[string]string {
			return map[string]string{second: adrText(ep)}
		}},
		{"an ADR whose Ticket line reads none", func(string, string) map[string]string {
			return map[string]string{second: adrText("none")}
		}},
		{"an ADR for an epic not in architecture", func(_, other string) map[string]string {
			return map[string]string{second: adrText(other)}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e, r := startAudit(t)
			ep := e.architectureEpic("Share the skill pack")
			other, res := e.m.Create(context.Background(), machine.CreateRequest{Project: testProject, Kind: machine.KindEpic,
				Title: "Write a PRD", Verb: machine.VerbCreatePRD, Actor: machine.Server("test")})
			mustOK(t, res)
			commit(t, e.checkout, "adr", map[string]string{adr0007: adrText(ep)})
			e.pushBase()
			e.tick(r)
			if it := e.status(ep); it.Status != machine.SpecNeeded {
				t.Fatalf("the first ADR left epic %s in %s", ep, it.Status)
			}
			e.wantAlerts("")
			commit(t, e.checkout, "adr change", c.files(ep, other))
			sha := e.pushBase()
			e.tick(r)
			e.wantAlerts("no_record", sha)
		})
	}
}

// C9: a rewritten base raises one alert and moves the cursor. A forced
// push replaces the audited head, so the new head does not reach the
// cursor. The tick raises one base_rewritten alert that names the old
// cursor and the new head, raises no unreviewed_merge for the new
// commit, and writes the new head as the cursor. The next tick raises
// no second alert.
func TestC9RewrittenBaseRaisesOneAlertAndMovesTheCursor(t *testing.T) {
	e, r := startAudit(t)
	old := e.auditCursor()
	gitT(t, e.checkout, "commit", "-q", "--amend", "--allow-empty", "-m", "Rewrite the base")
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), "+HEAD:main")
	head := gitT(t, e.checkout, "rev-parse", "HEAD")
	e.tick(r)
	e.tick(r)
	got := e.alertsOf("base_rewritten")
	if len(got) != 1 || got[0].Subject != testProject+"@"+old || got[0].Count != 1 ||
		!strings.Contains(got[0].Body, short(old)) || !strings.Contains(got[0].Body, short(head)) {
		t.Fatalf("base_rewritten alerts %+v, want one with subject %s@%s, count 1 and a body that names %s and %s",
			got, testProject, old, short(old), short(head))
	}
	e.wantAlerts("")
	if c := e.auditCursor(); c != head {
		t.Fatalf("the cursor is at %q, want %s", c, head)
	}
}

// C10: on a tick whose pull list fails, the audit judges the recorded
// merge and stops before the direct push. It never skips the push for
// the recorded merge after it. A later tick with a working pull list
// raises one alert for the push and passes the merge after it.
func TestC10PartialPullListStopsBeforeACommitWithNoRecord(t *testing.T) {
	e, r := startAudit(t)
	sq := commit(t, e.checkout, "squash", map[string]string{"a.txt": "a\n"})
	e.apply(e.ticket(machine.ReadyToMerge, shaA), "merged", map[string]string{"sha": sq})
	push := commit(t, e.checkout, "direct push", map[string]string{"b.txt": "b\n"})
	after := commit(t, e.checkout, "squash after", map[string]string{"c.txt": "c\n"})
	e.apply(e.ticket(machine.ReadyToMerge, shaA), "merged", map[string]string{"sha": after})
	e.pushBase()
	e.api.Route("GET /repos/"+testRepo+"/pulls", func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, 422, map[string]string{"message": "synthetic failure"})
	})
	if err := r.Tick(context.Background()); err == nil {
		t.Fatal("the tick with a failing pull list answered no error")
	}
	e.wantAlerts("")
	if got := e.auditCursor(); got != sq {
		t.Fatalf("after the partial tick the cursor is at %q, want the recorded merge %s", got, sq)
	}
	e.api.Route("GET /repos/"+testRepo+"/pulls", nil)
	e.tick(r)
	e.wantAlerts("no_record", push)
	if got := e.auditCursor(); got != after {
		t.Fatalf("the cursor is at %q, want %s", got, after)
	}
}

// C10: on a tick whose adrs fails, the walk stops before the unrecorded
// ADR merge. A later tick with a working adrs lands the epic and passes
// the merge with no alert.
func TestC10FailedADRsStopsBeforeTheADRMerge(t *testing.T) {
	e, r := startAudit(t)
	ep := e.architectureEpic("Share the skill pack")
	x := commit(t, e.checkout, "adr", map[string]string{adr0007: adrText(ep)})
	e.pushBase()
	undo := e.failGit()
	if err := r.Tick(context.Background()); err == nil {
		t.Fatal("the tick with a failing adrs answered no error")
	}
	e.wantAlerts("")
	if got := e.auditCursor(); got != e.base {
		t.Fatalf("after the partial tick the cursor is at %q, want %s before the ADR merge", got, e.base)
	}
	undo()
	e.tick(r)
	if it := e.status(ep); it.Status != machine.SpecNeeded || it.Fact(machine.FactADRMerged) != x {
		t.Fatalf("epic %s: %s %v", ep, it.Status, it.Facts)
	}
	e.wantAlerts("")
	if got := e.auditCursor(); got != x {
		t.Fatalf("the cursor is at %q, want %s", got, x)
	}
}

// C10: when the alert write for a commit fails, the cursor stays before
// that commit, and the next tick audits it.
func TestC10FailedAlertWriteKeepsTheCursor(t *testing.T) {
	e, r := startAudit(t)
	push := commit(t, e.checkout, "direct push", map[string]string{"b.txt": "b\n"})
	e.pushBase()
	e.exec(`CREATE TRIGGER fail_alert BEFORE INSERT ON alerts WHEN NEW.kind = 'unreviewed_merge'
		BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END`)
	if err := r.Tick(context.Background()); err == nil {
		t.Fatal("the tick whose alert write failed answered no error")
	}
	if got := e.auditCursor(); got != e.base {
		t.Fatalf("after the failed write the cursor is at %q, want %s", got, e.base)
	}
	e.exec(`DROP TRIGGER fail_alert`)
	e.tick(r)
	e.wantAlerts("no_record", push)
	if got := e.auditCursor(); got != push {
		t.Fatalf("the cursor is at %q, want %s", got, push)
	}
}

// C11: the base audit sends no request to the GitHub API, across three
// commits, one of them an ADR merge.
func TestC11BaseAuditMakesNoGitHubRequest(t *testing.T) {
	e, r := startAudit(t)
	ctx := context.Background()
	sq := commit(t, e.checkout, "squash", map[string]string{"a.txt": "a\n"})
	e.apply(e.ticket(machine.ReadyToMerge, shaA), "merged", map[string]string{"sha": sq})
	ep := e.architectureEpic("Share the skill pack")
	_, merge := e.mergeBranch("adr", map[string]string{adr0007: adrText(ep)})
	e.apply(ep, machine.VerbADRLanded, map[string]string{"path": adr0007, "added": merge})
	push := commit(t, e.checkout, "direct push", map[string]string{"b.txt": "b\n"})
	e.pushBase()
	_, head, err := e.svc.SyncBase(ctx, e.project)
	if err != nil {
		t.Fatal(err)
	}
	before := len(e.api.Calls())
	if err := r.auditBase(ctx, e.project, head, nil); err != nil {
		t.Fatal(err)
	}
	if calls := e.api.Calls()[before:]; len(calls) != 0 {
		t.Fatalf("the base audit sent %v", calls)
	}
	e.wantAlerts("no_record", push)
	if got := e.auditCursor(); got != push {
		t.Fatalf("the cursor is at %q, want %s", got, push)
	}
}

// C12: the audit judges each first-parent commit after the cursor, oldest
// first, and leaves the cursor at the newest. The two commits that a
// merge brought in on its second parent are never judged.
func TestC12BaseAuditWalksEveryFirstParentCommitInOrder(t *testing.T) {
	e, r := startAudit(t)
	a := commit(t, e.checkout, "first", map[string]string{"a.txt": "a\n"})
	gitT(t, e.checkout, "checkout", "-q", "-b", "side")
	commit(t, e.checkout, "side one", map[string]string{"s1.txt": "1\n"})
	commit(t, e.checkout, "side two", map[string]string{"s2.txt": "2\n"})
	gitT(t, e.checkout, "checkout", "-q", "work")
	gitT(t, e.checkout, "merge", "-q", "--no-ff", "-m", "Merge side", "side")
	m := gitT(t, e.checkout, "rev-parse", "HEAD")
	c := commit(t, e.checkout, "last", map[string]string{"c.txt": "c\n"})
	e.pushBase()
	e.tick(r)
	e.wantAlerts("no_record", a, m, c)
	if got := e.auditCursor(); got != c {
		t.Fatalf("the cursor is at %q, want %s", got, c)
	}
}

// C14: an ADR merge passes on the tick that first sees it. The cursor
// sits before the ADR commit, and a ticket merge follows it, so the ADR
// commit is not the tick's head. One tickProject lands the epic and
// raises no alert.
func TestC14ADRMergePassesOnTheTickThatFirstSeesIt(t *testing.T) {
	e, r := startAudit(t)
	ep := e.architectureEpic("Share the skill pack")
	x := commit(t, e.checkout, "adr", map[string]string{adr0007: adrText(ep)})
	side, merge := e.mergeBranch("tk", map[string]string{"a.txt": "a\n"})
	id := e.ticket(machine.ReadyToMerge, side)
	e.api.World.Add(&gt.PR{Head: BranchFor(id), HeadSHA: side, State: "closed", Merged: true, MergeSHA: merge})
	e.pushBase()
	if err := r.tickProject(context.Background(), e.project); err != nil {
		t.Fatal(err)
	}
	if it := e.status(ep); it.Status != machine.SpecNeeded || it.Fact(machine.FactADRMerged) != x {
		t.Fatalf("epic %s: %s %v", ep, it.Status, it.Facts)
	}
	if it := e.status(id); it.Fact(machine.FactMergedSHA) != merge {
		t.Fatalf("ticket %s: %s %v", id, it.Status, it.Facts)
	}
	e.wantAlerts("")
	if got := e.auditCursor(); got != merge {
		t.Fatalf("the cursor is at %q, want %s", got, merge)
	}
}
