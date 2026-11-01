package github

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/qa"
	"github.com/iantanwx/loop/internal/store"
)

// qaOn is a project config that runs the QA stage.
const qaOn = "merge:\n  mechanism: direct\nstages:\n  qa: {}\n"

// eligibleAt counts the loop/eligible posts that the fake GitHub holds at
// sha.
func (e *env) eligibleAt(sha string) int {
	e.t.Helper()
	return len(slices.DeleteFunc(e.api.World.Posted(), func(p string) bool { return p != sha+" "+EligibleContext+" success" }))
}

// eligibility runs the reactor's eligibility step once.
func (e *env) eligibility() {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.svc.EnqueueQAChecks(ctx, e.project); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.svc.EnqueueEligibility(ctx, e.project); err != nil {
		e.t.Fatal(err)
	}
	cerr := e.svc.RunCheckRunEffects(ctx)
	if _, err := e.svc.EnqueueEligibility(ctx, e.project); err != nil {
		e.t.Fatal(err)
	}
	if err := errors.Join(cerr, e.svc.RunStatusEffects(ctx)); err != nil {
		e.t.Fatal(err)
	}
}

// wantQABeforeEligible enqueues and posts loop/eligible as the reactor
// does, and fails unless the one loop/qa check run of sha holds conclusion
// and each of texts before loop/eligible posts at sha.
func (e *env) wantQABeforeEligible(sha, conclusion string, texts ...string) {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.svc.EnqueueEligibility(ctx, e.project); err != nil {
		e.t.Fatal(err)
	}
	if err := e.svc.RunStatusEffects(ctx); err != nil {
		e.t.Fatal(err)
	}
	if e.eligibleAt(sha) != 0 {
		e.t.Fatalf("%s posted at %s before %s", EligibleContext, sha, QAContext)
	}
	if err := e.svc.RunCheckRunEffects(ctx); err != nil {
		e.t.Fatal(err)
	}
	e.wantCheckRun(sha, conclusion, texts...)
	e.eligibility()
	if e.eligibleAt(sha) != 1 {
		e.t.Fatalf("posted %v, want one %s at %s after %s", e.api.World.Posted(), EligibleContext, sha, QAContext)
	}
}

// qaKeys lists the natural keys of the checkrun effects of sha, by
// outbox id.
func (e *env) qaKeys(sha string) []string {
	e.t.Helper()
	var keys []string
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		rows, err := tx.Query(`SELECT natural_key FROM outbox WHERE effect_kind = ? AND natural_key LIKE ? ORDER BY id`,
			EffectCheckRun, testProject+":"+sha+":%")
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			keys = append(keys, k)
		}
		return rows.Err()
	}); err != nil {
		e.t.Fatal(err)
	}
	return keys
}

// TestC29EligibleFollowsANeutralQAOnAStageOffHead: a reviewed head in a
// project that does not run QA gets a neutral loop/qa from the effect with
// the key suffix off, and loop/eligible only after it posted.
func TestC29EligibleFollowsANeutralQAOnAStageOffHead(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	id := e.ticket(machine.ReadyToMerge, shaA)
	w.Add(&gt.PR{Head: BranchFor(id), HeadSHA: shaA})
	e.wantQABeforeEligible(shaA, "neutral", "QA stage is off")
	if got, want := e.qaKeys(shaA), []string{checkRunKey(testProject, shaA, "off")}; !slices.Equal(got, want) {
		t.Fatalf("checkrun keys = %q, want %q", got, want)
	}
	e.eligibility()
	if got := w.CheckRunCalls(); !slices.Equal(got, []string{"create 9001 " + shaA + " loop/qa neutral"}) || e.eligibleAt(shaA) != 1 {
		t.Fatalf("check run calls %q and %d eligible posts after another tick; want one of each", got, e.eligibleAt(shaA))
	}
}

// TestC29EligibleFollowsTheReportOnAQAHead: a head with a stored report
// gets no neutral loop/qa. loop/eligible waits for the report's check run.
func TestC29EligibleFollowsTheReportOnAQAHead(t *testing.T) {
	e := qaEnv(t)
	e.baseConfig(qaOn)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	mustOK(t, e.qaReport(id, e.status(id).Epoch, "k1", present(head, nil, fuzzy("web", "pass"))))
	e.wantQABeforeEligible(head, "success", "fuzzy_uat web: pass")
	if got, want := e.qaKeys(head), []string{checkRunKey(testProject, head, qaRun)}; !slices.Equal(got, want) {
		t.Fatalf("checkrun keys = %q, want %q", got, want)
	}
}

// TestC29EligibleFollowsACarriedQAOnABaseMergedHead: the merge commit that
// base_merged made gets a neutral loop/qa with the key suffix carried,
// whose text links the report at the head QA passed, then loop/eligible.
func TestC29EligibleFollowsACarriedQAOnABaseMergedHead(t *testing.T) {
	e := qaEnv(t)
	e.baseConfig(qaOn)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	mustOK(t, e.qaReport(id, e.status(id).Epoch, "k1", present(head, nil, fuzzy("web", "pass"))))
	e.eligibility()
	e.eligibility()
	e.apply(id, machine.VerbEnqueue, map[string]string{"head": head})
	e.apply(id, machine.VerbBaseMerged, map[string]string{"from": head, "head": shaB})
	e.wantQABeforeEligible(shaB, "neutral", "https://github.com/"+testRepo+"/commit/"+head+"/checks")
	if got, want := e.qaKeys(shaB), []string{checkRunKey(testProject, shaB, "carried")}; !slices.Equal(got, want) {
		t.Fatalf("checkrun keys = %q, want %q", got, want)
	}
}

// TestC29NoEligibleWhileGitHubRefusesCheckRuns: an App that cannot post
// loop/qa posts no loop/eligible, even after the check run's effect fails.
func TestC29NoEligibleWhileGitHubRefusesCheckRuns(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	e.ticket(machine.ReadyToMerge, shaA)
	e.api.Route("POST /repos/"+testRepo+"/check-runs", func(w http.ResponseWriter, _ *http.Request) {
		gt.WriteJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by integration"})
	})
	for range 3 {
		if _, err := e.svc.EnqueueEligibility(context.Background(), e.project); err != nil {
			t.Fatal(err)
		}
		e.exec(`UPDATE outbox SET next_attempt_at = 0, attempts = ?`, maxEffectAttempts-1)
		_ = e.svc.RunCheckRunEffects(context.Background())
		_ = e.svc.RunStatusEffects(context.Background())
	}
	if n := e.eligibleAt(shaA); n != 0 {
		t.Fatalf("%d %s posts while GitHub refuses check runs", n, EligibleContext)
	}
	var effects int
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		return tx.QueryRow(`SELECT count(*) FROM outbox WHERE effect_kind = ?`, EffectStatus).Scan(&effects)
	}); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("%d %s effects enqueued while %s failed", effects, EligibleContext, QAContext)
	}
}

// offTicket makes a ticket whose review passed with the stage off at a
// head of its branch that holds files. It returns the ticket and the head.
func (e *env) offTicket(files map[string]string) (string, string) {
	e.t.Helper()
	ctx := context.Background()
	id, res := e.m.Create(ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindTicket, Title: "t",
		Description: mayChange("apps/web/src/a.ts"), Verb: "create", Actor: machine.Server("test")})
	mustOK(e.t, res)
	_, res = e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageImplementer, RunID: "run-1"})
	mustOK(e.t, res)
	head := commit(e.t, e.checkout, "work on "+id, union(files, map[string]string{id + ".txt": id + "\n"}))
	if _, err := e.push(id, head); err != nil {
		e.t.Fatal(err)
	}
	for _, verb := range []string{"pr_open", machine.VerbSimplifyOff, "review_pass"} {
		e.apply(id, verb, map[string]string{"head": head, "qa": "off"})
	}
	return id, head
}

// qaDue moves a ticket the stage-off review passed into in_qa, as the
// sweep does once the project runs QA, and claims it for qaRun.
func (e *env) qaDue(id string) {
	e.t.Helper()
	ctx := context.Background()
	e.apply(id, machine.VerbQADue, nil)
	_, res := e.m.Claim(ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageQA, RunID: qaRun})
	mustOK(e.t, res)
	if err := e.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO runs (run_id, project, stage, item, state, started_at) VALUES (?, ?, ?, ?, 'running', 0)`,
			qaRun, testProject, machine.StageQA, id)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
}

// TestC38AQAReportReplacesTheStageOffNeutral: a head that got the stage-off
// neutral and loop/eligible keeps its one loop/qa check run, which a QA
// report at the head then updates to the report's conclusion. loop/eligible
// posts no second time. A repeat of the off key refuses in RecordIntent,
// and the enqueue's INSERT OR IGNORE keeps one row.
func TestC38AQAReportReplacesTheStageOffNeutral(t *testing.T) {
	for _, c := range []struct {
		name, result, conclusion string
		want                     []string
	}{
		{"fail", "fail", "failure", []string{"create 9001 %s loop/qa neutral", "update 9001 failure"}},
		{"pass", "pass", "success", []string{"create 9001 %s loop/qa neutral", "update 9001 success"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := qaEnv(t)
			id, head := e.offTicket(verifyWeb)
			e.wantQABeforeEligible(head, "neutral")
			e.eligibility()
			off := checkRunKey(testProject, head, "off")
			if got := e.qaKeys(head); !slices.Equal(got, []string{off}) {
				t.Fatalf("checkrun keys after two enqueues = %q, want one %s", got, off)
			}
			err := e.st.Write(context.Background(), func(tx *store.Tx) error {
				_, err := tx.RecordIntent(store.Intent{Project: testProject, Item: id, Kind: "ticket", Verb: "review_pass"},
					[]store.OutboxRow{{EffectKind: EffectCheckRun, NaturalKey: off, Payload: []byte(`{}`)}}, nil)
				return err
			})
			if !errors.Is(err, store.ErrEffectExists) {
				t.Fatalf("a repeat of %s = %v, want ErrEffectExists", off, err)
			}

			e.baseConfig(qaOn)
			e.qaDue(id)
			check := fuzzy("web", c.result)
			files := map[string][]byte(nil)
			if c.result == "fail" {
				check.Findings = []qa.Finding{{Summary: "save does nothing", Severity: "fail", Evidence: []string{"web/save.png"}}}
				files = map[string][]byte{"web/save.png": screenshot}
			}
			mustOK(t, e.qaReportWith(id, e.status(id).Epoch, "k1", present(head, nil, check), files))
			e.eligibility()
			e.eligibility()
			want := slices.Clone(c.want)
			want[0] = strings.Replace(want[0], "%s", head, 1)
			if got := e.api.World.CheckRunCalls(); !slices.Equal(got, want) {
				t.Fatalf("check run calls = %q, want %q", got, want)
			}
			e.wantCheckRun(head, c.conclusion)
			if n := e.eligibleAt(head); n != 1 {
				t.Fatalf("%d %s posts at %s, want 1", n, EligibleContext, head)
			}
		})
	}
}

// TestC38OnlyTheReportPostsWhenTheOffNeutralIsDueAtTheSameTick: an off
// effect that never posted is done with no post once the report's effect
// of the same head is due.
func TestC38OnlyTheReportPostsWhenTheOffNeutralIsDueAtTheSameTick(t *testing.T) {
	e := qaEnv(t)
	id, head := e.offTicket(verifyWeb)
	if _, err := e.svc.EnqueueEligibility(context.Background(), e.project); err != nil {
		t.Fatal(err)
	}
	e.baseConfig(qaOn)
	e.qaDue(id)
	mustOK(t, e.qaReport(id, e.status(id).Epoch, "k1", present(head, nil, fuzzy("web", "pass"))))
	e.eligibility()
	e.eligibility()
	if got, want := e.api.World.CheckRunCalls(), []string{"create 9001 " + head + " loop/qa success"}; !slices.Equal(got, want) {
		t.Fatalf("check run calls = %q, want %q", got, want)
	}
	if n := e.eligibleAt(head); n != 1 {
		t.Fatalf("%d %s posts at %s, want 1", n, EligibleContext, head)
	}
	off, report := checkRunKey(testProject, head, "off"), checkRunKey(testProject, head, qaRun)
	if got := e.qaKeys(head); !slices.Equal(got, []string{off, report}) {
		t.Fatalf("checkrun keys = %q, want %q", got, []string{off, report})
	}
	var state, note string
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		return tx.QueryRow(`SELECT state, last_error FROM outbox WHERE natural_key = ?`, off).Scan(&state, &note)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "done" || !strings.Contains(note, "not posted") {
		t.Fatalf("the off effect = %s %q, want done with no post", state, note)
	}
}

// TestC38ASecondTurnOnKeepsTheReportsConclusion: once a report holds the
// head, a project that stops running QA and runs it again enqueues no new
// effect at the head.
func TestC38ASecondTurnOnKeepsTheReportsConclusion(t *testing.T) {
	e := qaEnv(t)
	id, head := e.offTicket(verifyWeb)
	e.wantQABeforeEligible(head, "neutral")
	e.baseConfig(qaOn)
	e.qaDue(id)
	mustOK(t, e.qaReport(id, e.status(id).Epoch, "k1", present(head, nil, fuzzy("web", "pass"))))
	e.eligibility()
	keys := e.qaKeys(head)
	e.baseConfig("merge:\n  mechanism: direct\nstages:\n  qa: {enabled: false}\n")
	e.eligibility()
	e.baseConfig(qaOn)
	e.eligibility()
	if got := e.qaKeys(head); !slices.Equal(got, keys) {
		t.Fatalf("checkrun keys after the second turn-on = %q, want %q", got, keys)
	}
	e.wantCheckRun(head, "success")
	if n := e.eligibleAt(head); n != 1 {
		t.Fatalf("%d %s posts at %s, want 1", n, EligibleContext, head)
	}
}

// TestC29NoNeutralQAOnAReportedHeadOnceTheStageTurnsOff: a head that holds
// a stored report gets no neutral loop/qa when the project then stops
// running QA, so the report's conclusion stays on its check run.
func TestC29NoNeutralQAOnAReportedHeadOnceTheStageTurnsOff(t *testing.T) {
	e := qaEnv(t)
	e.baseConfig(qaOn)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	mustOK(t, e.qaReport(id, e.status(id).Epoch, "k1", present(head, nil, fuzzy("web", "pass"))))
	e.wantQABeforeEligible(head, "success")
	e.baseConfig("merge:\n  mechanism: direct\nstages:\n  qa: {enabled: false}\n")
	e.eligibility()
	e.eligibility()
	if got, want := e.qaKeys(head), []string{checkRunKey(testProject, head, qaRun)}; !slices.Equal(got, want) {
		t.Fatalf("checkrun keys after the stage turned off = %q, want %q", got, want)
	}
	e.wantCheckRun(head, "success")
	if n := e.eligibleAt(head); n != 1 {
		t.Fatalf("%d %s posts at %s, want 1", n, EligibleContext, head)
	}
}
