package github

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/qa"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"

	loopv1 "github.com/iantanwx/loop/gen/loop/v1"
)

// qaRun is the QA run that holds each ticket of these tests.
const qaRun = "r-qa"

// The verify skills of the fixture trees: web over apps/web/** and api
// over apps/api/**.
var (
	verifyWeb = map[string]string{".claude/skills/verify-web/SKILL.md": "---\nname: verify-web\ndescription: drive the web app\npaths:\n  - apps/web/**\n---\n"}
	verifyAPI = map[string]string{".claude/skills/verify-api/SKILL.md": "---\nname: verify-api\ndescription: drive the api\npaths: [apps/api/**]\n---\n"}
)

func union(sets ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, s := range sets {
		for k, v := range s {
			out[k] = v
		}
	}
	return out
}

// mayChange is a ticket body whose "May change" list names path.
func mayChange(path string) string {
	return "## Files\n\nMay change: `" + path + "`.\n"
}

// qaEnv is a GitHub world with a machine whose qa report verb is wired as
// the server wires it.
func qaEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.withWorld()
	e.m.SetQAGate(e.svc.QAGate)
	e.m.SetQAReported(e.svc.QAReported)
	return e
}

// qaTicket makes a ticket with body whose review passed with the stage on
// at a head of its branch that holds files, and that qaRun holds in in_qa
// with designSkill on its row. It returns the ticket and the head.
func (e *env) qaTicket(body string, files map[string]string, designSkill string) (string, string) {
	e.t.Helper()
	ctx := context.Background()
	id, res := e.m.Create(ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindTicket, Title: "t",
		Description: body, Verb: "create", Actor: machine.Server("test")})
	mustOK(e.t, res)
	_, res = e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageImplementer, RunID: "run-1"})
	mustOK(e.t, res)
	head := commit(e.t, e.checkout, "work on "+id, union(files, map[string]string{id + ".txt": id + "\n"}))
	if _, err := e.push(id, head); err != nil {
		e.t.Fatal(err)
	}
	for _, verb := range []string{"pr_open", "review_pass"} {
		mustOK(e.t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: verb,
			Params: map[string]string{"head": head, "qa": "on"}, Actor: machine.Server("test")}))
	}
	_, res = e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageQA, RunID: qaRun})
	mustOK(e.t, res)
	if err := e.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO runs (run_id, project, stage, item, state, started_at, design_skill)
			VALUES (?, ?, ?, ?, 'running', 0, ?) ON CONFLICT (run_id) DO UPDATE SET item = excluded.item, design_skill = excluded.design_skill`,
			qaRun, testProject, machine.StageQA, id, designSkill)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return id, head
}

// qaReport hands in r as qaRun, through the qa report verb, at the epoch
// the run's claim gave it. key is the call's idempotency key, or "".
func (e *env) qaReport(item string, epoch int64, key string, r qa.Report) cli.Result {
	e.t.Helper()
	return e.qaReportWith(item, epoch, key, r, nil)
}

// qaReportWith is qaReport with the evidence files that --evidence sends.
func (e *env) qaReportWith(item string, epoch int64, key string, r qa.Report, files map[string][]byte) cli.Result {
	e.t.Helper()
	v, ok := cli.Surface().Verb("qa report")
	if !ok {
		e.t.Fatal("the surface has no qa report verb")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		e.t.Fatal(err)
	}
	call := &rpc.Call{
		Inv: cli.Invocation{Verb: v, Args: []string{item}, RequestID: "req-qa-" + key,
			Flags: map[string]string{"head": r.Head, "file": "report.json"},
			Extra: map[string]string{cli.ExtraFile: base64.StdEncoding.EncodeToString(raw), cli.ExtraContentHash: "c"}},
		Principal: rpc.Principal{Kind: cli.PrincipalRun, RunID: qaRun, Project: testProject, Item: item,
			Stage: machine.StageQA, Epoch: epoch},
		Request: &loopv1.VerbRequest{IdempotencyKey: key},
	}
	if files != nil {
		call.Inv.Flags["evidence"] = "evidence"
		call.Inv.Extra[cli.ExtraEvidence] = cli.EncodeEvidence(files)
	}
	return e.m.Handlers()["qa report"](context.Background(), call)
}

// postQAChecks runs the reactor's QA part: the checkrun effects of stored
// reports, then their posts.
func (e *env) postQAChecks() {
	e.t.Helper()
	if _, err := e.svc.EnqueueQAChecks(context.Background(), e.project); err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.RunCheckRunEffects(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func fuzzy(app, result string) qa.Check {
	return qa.Check{Kind: "fuzzy_uat", App: app, Result: result, Goal: "save a strategy", Steps: 12, Minutes: 5,
		Findings: []qa.Finding{}}
}

func visual(app, result string) qa.Check {
	return qa.Check{Kind: "visual", App: app, Result: result, Minutes: 3, Screens: 2, Findings: []qa.Finding{}}
}

func present(head string, bugs []string, checks ...qa.Check) qa.Report {
	return qa.Report{Head: head, Verify: "present", Checks: checks, Bugs: bugs}
}

func wantRefused(t *testing.T, res cli.Result, code, rule, hint string) {
	t.Helper()
	if res.Refusal == nil || res.Refusal.Code != code || res.Refusal.Rule != rule || !strings.Contains(res.Refusal.Hint, hint) {
		t.Fatalf("answer = %+v, want %s rule %s with a hint that names %q", res, code, rule, hint)
	}
}

// wantCheckRun fails unless the fake GitHub holds exactly one loop/qa
// check run at head, with conclusion, whose output text holds each of
// texts.
func (e *env) wantCheckRun(head, conclusion string, texts ...string) {
	e.t.Helper()
	runs := e.api.World.CheckRunsAt(head)
	if len(runs) != 1 || runs[0]["name"] != QAContext || runs[0]["conclusion"] != conclusion {
		e.t.Fatalf("check runs at %s = %v, want one %s with %s", head, runs, QAContext, conclusion)
	}
	out, _ := json.Marshal(runs[0]["output"])
	for _, s := range texts {
		if !strings.Contains(string(out), s) {
			e.t.Fatalf("the check run's output %s does not name %q", out, s)
		}
	}
}

// qaBug files a bug with the facts that work create --kind bug writes for
// source and run.
func (e *env) qaBug(source, run string) string {
	e.t.Helper()
	facts := map[string]string{machine.FactSource: source}
	if run != "" {
		facts[machine.FactQARun] = run
	}
	id, res := e.m.Create(context.Background(), machine.CreateRequest{Project: testProject, Kind: machine.KindBug, Title: "b",
		Description: "the api does not start", Verb: "file", Actor: machine.Server("test"), Facts: facts})
	mustOK(e.t, res)
	return id
}

// Done means: a report through the verb moves the ticket to
// ready_to_merge with qa_head at the head and the rendered report as its
// review, and the fake GitHub holds one loop/qa check run at the head.
func TestC26AReportThroughTheVerbPassesTheTicketAndPostsOneCheckRun(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	mustOK(t, e.qaReport(id, e.status(id).Epoch, "k1", present(head, nil, fuzzy("web", "pass"))))
	it := e.status(id)
	if it.Status != machine.ReadyToMerge || it.Fact(machine.FactQAHead) != head || !strings.Contains(it.Fact(machine.FactReview), "fuzzy_uat web: pass") {
		t.Fatalf("ticket = %s, qa_head %q, review %q; want ready_to_merge at %s with the report", it.Status,
			it.Fact(machine.FactQAHead), it.Fact(machine.FactReview), head)
	}
	if err := e.svc.RunCheckRunEffects(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.wantCheckRun(head, "success", "fuzzy_uat web: pass")
	var key string
	if err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT natural_key FROM outbox WHERE effect_kind = ?`, EffectCheckRun).Scan(&key)
	}); err != nil {
		t.Fatal(err)
	}
	if want := testProject + ":" + head + ":loop/qa:" + qaRun; key != want {
		t.Fatalf("the effect's key = %q, want %q", key, want)
	}
}

// C26: a repeat of qa report after a lost answer replays the stored
// answer, and the head keeps one check run.
func TestC26ARepeatOfTheReportReplaysAndKeepsOneCheckRun(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	epoch := e.status(id).Epoch
	r := present(head, nil, fuzzy("web", "pass"))
	mustOK(t, e.qaReport(id, epoch, "k1", r))
	e.postQAChecks()
	again := e.qaReport(id, epoch, "k1", r)
	if again.Refusal != nil || !again.Replayed {
		t.Fatalf("the repeat = %+v, want the stored answer replayed", again)
	}
	e.postQAChecks()
	e.wantCheckRun(head, "success")
	if calls := e.api.World.CheckRunCalls(); len(calls) != 1 {
		t.Fatalf("check run calls = %q, want one create", calls)
	}
}

// C26: a server that dies after it stored the report and before it
// enqueued the effect leaves a report that EnqueueQAChecks enqueues once
// at its next tick. A stored report whose verdict never applied gets no
// effect.
func TestC26EnqueueQAChecksEnqueuesTheEffectOfAStoredReportThatHoldsNone(t *testing.T) {
	e := qaEnv(t)
	e.m.SetQAReported(nil) // the server died before the enqueue
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	mustOK(t, e.qaReport(id, e.status(id).Epoch, "k1", present(head, nil, fuzzy("web", "pass"))))
	if err := e.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO qa_reports (run_id, project, item, head, conclusion, report, created_at)
			VALUES ('r-never', ?, ?, ?, 'success', '{}', 0)`, testProject, id, shaB)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if n, err := e.svc.EnqueueQAChecks(ctx, e.project); err != nil || n != 1 {
		t.Fatalf("EnqueueQAChecks = %d, %v; want 1", n, err)
	}
	if n, err := e.svc.EnqueueQAChecks(ctx, e.project); err != nil || n != 0 {
		t.Fatalf("the second EnqueueQAChecks = %d, %v; want 0", n, err)
	}
	if err := e.svc.RunCheckRunEffects(ctx); err != nil {
		t.Fatal(err)
	}
	e.wantCheckRun(head, "success")
	if runs := e.api.World.CheckRunsAt(shaB); len(runs) != 0 {
		t.Fatalf("check runs at the head of a verdict that never applied = %v", runs)
	}
}

// C11: a push in in_qa returns the ticket to review, and the QA run's
// report with the old lease refuses with LEASE_NOT_HELD. No report is
// stored, and no check run posts.
func TestC11AReportWithTheOldLeaseRefusesAfterANewHead(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	epoch := e.status(id).Epoch
	mustOK(t, e.m.HeadChanged(context.Background(), testProject, id, shaB))
	res := e.qaReport(id, epoch, "k1", present(head, nil, fuzzy("web", "pass")))
	if res.Refusal == nil || res.Refusal.Code != cli.CodeLeaseNotHeld {
		t.Fatalf("the report = %+v, want LEASE_NOT_HELD", res)
	}
	e.postQAChecks()
	if runs := e.api.World.CheckRunsAt(head); len(runs) != 0 {
		t.Fatalf("check runs at the old head = %v, want none", runs)
	}
	var n int
	if err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM qa_reports`).Scan(&n)
	}); err != nil || n != 0 {
		t.Fatalf("qa_reports holds %d rows (%v), want none", n, err)
	}
	if it := e.status(id); it.Status != machine.InReview {
		t.Fatalf("ticket = %s, want in_review", it.Status)
	}
}

// C18: a plan that drives web and api refuses a report with no fuzzy UAT
// of api, and a report with a check of an app outside the plan.
func TestC18TheVerbRefusesAReportThatMissesAPlannedApp(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("docs/x.md"), union(verifyWeb, verifyAPI), "")
	epoch := e.status(id).Epoch
	wantRefused(t, e.qaReport(id, epoch, "k1", present(head, nil, fuzzy("web", "pass"))),
		cli.CodeQAReportRefused, "check_missing", "fuzzy_uat api")
	wantRefused(t, e.qaReport(id, epoch, "k2", present(head, nil, fuzzy("web", "pass"), fuzzy("api", "pass"), fuzzy("cli", "pass"))),
		cli.CodeQAReportRefused, "check_unplanned", "fuzzy_uat cli")
	if it := e.status(id); it.Status != machine.InQA {
		t.Fatalf("ticket = %s after the refusals, want in_qa", it.Status)
	}
	mustOK(t, e.qaReport(id, epoch, "k3", present(head, nil, fuzzy("web", "pass"), fuzzy("api", "pass"))))
}

// C19: a run whose row holds a design skill owes visual QA of each driven
// app, and a run whose row holds none owes none.
func TestC19VisualQAIsOwedExactlyWhenTheRunHoldsADesignSkill(t *testing.T) {
	t.Run("loop-impeccable", func(t *testing.T) {
		e := qaEnv(t)
		id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "loop-impeccable")
		epoch := e.status(id).Epoch
		wantRefused(t, e.qaReport(id, epoch, "k1", present(head, nil, fuzzy("web", "pass"))),
			cli.CodeQAReportRefused, "check_missing", "visual web")
		mustOK(t, e.qaReport(id, epoch, "k2", present(head, nil, fuzzy("web", "pass"), visual("web", "pass"))))
	})
	t.Run("no design skill", func(t *testing.T) {
		e := qaEnv(t)
		id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
		wantRefused(t, e.qaReport(id, e.status(id).Epoch, "k1", present(head, nil, fuzzy("web", "pass"), visual("web", "pass"))),
			cli.CodeQAReportRefused, "check_unplanned", "visual web")
	})
}

// C20: with no verify skill at the head, a report with verify missing
// passes QA and posts neutral with a text that names the missing skill.
// That report refuses at a head with a verify skill, and a report with
// verify present refuses at a head with none.
func TestC20NoVerifySkillGivesNeutralAndOnlyThen(t *testing.T) {
	missing := func(head string) qa.Report {
		return qa.Report{Head: head, Verify: "missing", Checks: []qa.Check{}, Bugs: []string{}}
	}
	t.Run("no verify skill", func(t *testing.T) {
		e := qaEnv(t)
		id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), nil, "")
		epoch := e.status(id).Epoch
		wantRefused(t, e.qaReport(id, epoch, "k1", present(head, nil)), cli.CodeQAReportRefused, "verify_skill_missing", "verify-<app>")
		mustOK(t, e.qaReport(id, epoch, "k2", missing(head)))
		if it := e.status(id); it.Status != machine.ReadyToMerge {
			t.Fatalf("ticket = %s, want ready_to_merge", it.Status)
		}
		e.postQAChecks()
		e.wantCheckRun(head, "neutral", "no verify skill")
	})
	t.Run("a verify skill", func(t *testing.T) {
		e := qaEnv(t)
		id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
		wantRefused(t, e.qaReport(id, e.status(id).Epoch, "k1", missing(head)), cli.CodeQAReportRefused, "verify_skill_present", "web")
	})
}

// C21: a report whose web checks pass and whose api check is unstartable,
// with the bug the run filed, passes QA with neutral, not success. Its
// check run names api as unstartable and names the bug.
func TestC21AMixedReportWithAnUnstartableAppPassesWithNeutral(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("docs/x.md"), union(verifyWeb, verifyAPI), "")
	bug := e.qaBug(machine.SourceQA, qaRun)
	epoch := e.status(id).Epoch
	api := qa.Check{Kind: "fuzzy_uat", App: "api", Result: "unstartable", Findings: []qa.Finding{}}
	mustOK(t, e.qaReport(id, epoch, "k1", present(head, []string{bug}, fuzzy("web", "pass"), api)))
	if it := e.status(id); it.Status != machine.ReadyToMerge {
		t.Fatalf("ticket = %s, want ready_to_merge", it.Status)
	}
	e.postQAChecks()
	e.wantCheckRun(head, "neutral", "fuzzy_uat api: unstartable", bug)
}

// C22: a report must name each bug the run filed, and only those.
func TestC22TheReportNamesEachBugTheRunFiledAndOnlyThose(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	filed := e.qaBug(machine.SourceQA, qaRun)
	other := e.qaBug(machine.SourceQA, "r-other")
	operator := e.qaBug(machine.SourceOperator, "")
	epoch := e.status(id).Epoch
	for i, b := range []string{other, operator, "tk-404"} {
		wantRefused(t, e.qaReport(id, epoch, "k"+string(rune('a'+i)), present(head, []string{filed, b}, fuzzy("web", "pass"))),
			cli.CodeQAReportRefused, "bug_not_filed", b)
	}
	wantRefused(t, e.qaReport(id, epoch, "k1", present(head, nil, fuzzy("web", "pass"))),
		cli.CodeQAReportRefused, "bug_not_named", filed)
	mustOK(t, e.qaReport(id, epoch, "k2", present(head, []string{filed}, fuzzy("web", "pass"))))
}

// A fail finding that cites a file the call does not hold refuses. A
// report whose head is not the one --head names refuses as a bad report.
func TestTheVerbRefusesAFailFindingWithNoEvidenceAndAHeadItDoesNotName(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	epoch := e.status(id).Epoch
	failed := fuzzy("web", "fail")
	failed.Findings = []qa.Finding{{Summary: "save does nothing", Severity: "fail", Evidence: []string{"web/save.png"}}}
	wantRefused(t, e.qaReport(id, epoch, "k1", present(head, nil, failed)), cli.CodeQAReportRefused, "evidence_missing", "web/save.png")

	v, _ := cli.Surface().Verb("qa report")
	raw, _ := json.Marshal(present(shaB, nil, fuzzy("web", "pass")))
	res := e.m.Handlers()["qa report"](context.Background(), &rpc.Call{
		Inv: cli.Invocation{Verb: v, Args: []string{id}, Flags: map[string]string{"head": head, "file": "report.json"},
			Extra: map[string]string{cli.ExtraFile: base64.StdEncoding.EncodeToString(raw)}},
		Principal: rpc.Principal{Kind: cli.PrincipalRun, RunID: qaRun, Project: testProject, Item: id, Stage: machine.StageQA, Epoch: epoch},
	})
	wantRefused(t, res, cli.CodeQAReportRefused, "bad_report", shaB)
	if it := e.status(id); it.Status != machine.InQA {
		t.Fatalf("ticket = %s, want in_qa", it.Status)
	}
}

// C27: for each fixture of C3, the plan that loop qa plan computes in the
// checkout at a head equals the plan the report verb computes over the
// mirror's tree at that head, for the same ticket and design skill.
func TestC27LoopQAPlanAndTheServerAgree(t *testing.T) {
	cases := []struct {
		name, body, design string
		files              map[string]string
		drive              string
	}{
		{"one app meets the ticket", mayChange("apps/web/src/a.ts"), "", union(verifyWeb, verifyAPI), `["web"]`},
		{"no app meets the ticket", mayChange("docs/x.md"), "loop-impeccable", union(verifyWeb, verifyAPI), `["api","web"]`},
		{"a skill with no paths", mayChange("docs/x.md"), "",
			map[string]string{".claude/skills/verify-cli/SKILL.md": "---\nname: verify-cli\ndescription: drive the cli\n---\n"}, `["cli"]`},
		{"no verify skill", mayChange("apps/web/src/a.ts"), "", nil, `[]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := qaEnv(t)
			id, head := e.qaTicket(c.body, c.files, c.design)
			server, ref := e.svc.qaPlan(context.Background(), e.project, id, qaRun, head)
			if ref != nil {
				t.Fatalf("the server's plan: %+v", *ref)
			}
			// What loop qa plan reads in the run's checkout at the head.
			gt.Git(t, e.checkout, "checkout", "-q", head)
			apps, err := qa.Apps(os.DirFS(e.checkout))
			if err != nil {
				t.Fatal(err)
			}
			local, _ := json.Marshal(qa.MakePlan(apps, admission.MayChangeEntries(c.body), c.design))
			got, _ := json.Marshal(server)
			if string(got) != string(local) || !strings.Contains(string(got), `"drive":`+c.drive) {
				t.Fatalf("the server's plan %s, loop qa plan's %s; want equal with drive %s", got, local, c.drive)
			}
		})
	}
}
