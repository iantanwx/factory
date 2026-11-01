package github

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/qa"
)

// screenshot is the start of a PNG file.
var screenshot = append([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"), bytes.Repeat([]byte{0x42}, 4096)...)

// noted is a passing report of the web app whose one note cites each of
// paths.
func noted(head string, paths ...string) qa.Report {
	c := fuzzy("web", "pass")
	c.Findings = []qa.Finding{{Summary: "the save toast shows at 390x844", Severity: "note", Evidence: paths}}
	return present(head, nil, c)
}

// evidenceCommit is the commit that the evidence ref of id holds on the
// fake GitHub, or "".
func (e *env) evidenceCommit(id string) string {
	e.t.Helper()
	return e.git.RefSHA("refs/loop/qa/" + id + "/" + qaRun)
}

// blobLink is the link the check run gives to path in commit.
func blobLink(commit, path string) string {
	return "https://github.com/" + testRepo + "/blob/" + commit + "/" + path
}

// storedEvidence is the evidence commit of qaRun's stored report, and
// whether a report is stored.
func (e *env) storedEvidence() (string, bool) {
	e.t.Helper()
	var commit string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT evidence_commit FROM qa_reports WHERE run_id = ?`, qaRun).Scan(&commit)
	})
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return commit, true
}

// Done means: a report with one screenshot moves the ticket to
// ready_to_merge, the fake GitHub holds the evidence commit with no parent
// at refs/loop/qa/<ticket>/<run id>, and one loop/qa check run that links
// each file. Each link resolves after the checkout is deleted.
func TestC24EachEvidenceFileOutlivesTheCheckoutAtTheLinkTheReportGives(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	files := map[string][]byte{"web/save-390.png": screenshot, "web/console.log": []byte("GET /strategies 200\n")}
	mustOK(t, e.qaReportWith(id, e.status(id).Epoch, "k1", noted(head, "web/save-390.png", "web/console.log"), files))
	if it := e.status(id); it.Status != machine.ReadyToMerge {
		t.Fatalf("ticket = %s, want ready_to_merge", it.Status)
	}

	commit := e.evidenceCommit(id)
	if commit == "" {
		t.Fatalf("the fake GitHub holds no refs/loop/qa/%s/%s", id, qaRun)
	}
	if parents := e.git.Parents(commit); len(parents) != 0 {
		t.Fatalf("the evidence commit %s has the parents %v, want none", commit, parents)
	}
	if got := strings.Join(e.git.Paths(commit), " "); got != "web/console.log web/save-390.png" {
		t.Fatalf("the evidence commit's tree holds %q, want exactly the two files", got)
	}
	if stored, _ := e.storedEvidence(); stored != commit {
		t.Fatalf("the stored report names the evidence commit %q, want %s", stored, commit)
	}

	e.postQAChecks()
	links := []string{blobLink(commit, "web/save-390.png"), blobLink(commit, "web/console.log")}
	e.wantCheckRun(head, "success", links...)

	if err := os.RemoveAll(e.checkout); err != nil {
		t.Fatal(err)
	}
	for _, link := range links {
		got, err := e.git.Serve(link)
		if err != nil {
			t.Fatalf("the fake GitHub serves no %s after the checkout's deletion: %v", link, err)
		}
		path := link[strings.LastIndex(link, commit)+len(commit)+1:]
		if !bytes.Equal(got, files[path]) {
			t.Fatalf("%s serves %d bytes, want the %d bytes of the file", link, len(got), len(files[path]))
		}
	}
}

// C24: the push lands before the verdict applies. A server that dies
// between the push and the row applies no row, and the report sent again
// applies it. A push that GitHub refuses applies no row and stores no
// report.
func TestC24ThePushLandsBeforeTheVerdictAndAFailedPushAppliesNoRow(t *testing.T) {
	files := map[string][]byte{"web/save-390.png": screenshot}
	t.Run("a kill point between the push and the row", func(t *testing.T) {
		e := qaEnv(t)
		id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
		epoch := e.status(id).Epoch
		e.svc.crash = func(p string) bool { return p == "evidence_pushed" }
		if res := e.qaReportWith(id, epoch, "k1", noted(head, "web/save-390.png"), files); res.Refusal == nil {
			t.Fatalf("the report at the kill point = %+v, want a refusal", res)
		}
		if e.evidenceCommit(id) == "" {
			t.Fatal("the push did not land before the kill point")
		}
		if it := e.status(id); it.Status != machine.InQA {
			t.Fatalf("ticket = %s after the kill point, want in_qa", it.Status)
		}
		if _, ok := e.storedEvidence(); ok {
			t.Fatal("a report is stored after the kill point")
		}
		e.svc.crash = nil
		mustOK(t, e.qaReportWith(id, epoch, "k1", noted(head, "web/save-390.png"), files))
		commit := e.evidenceCommit(id)
		if stored, _ := e.storedEvidence(); stored != commit {
			t.Fatalf("the stored report names %q, and the ref holds %s", stored, commit)
		}
		if it := e.status(id); it.Status != machine.ReadyToMerge {
			t.Fatalf("ticket = %s after the report sent again, want ready_to_merge", it.Status)
		}
	})
	t.Run("a push that GitHub refuses", func(t *testing.T) {
		e := qaEnv(t)
		id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
		e.git.RefusePushes(t, "refs/loop/qa/")
		res := e.qaReportWith(id, e.status(id).Epoch, "k1", noted(head, "web/save-390.png"), files)
		wantRefused(t, res, cli.CodeGitHubRefused, "evidence_push_refused", "refs/loop/qa/"+id+"/"+qaRun)
		if it := e.status(id); it.Status != machine.InQA {
			t.Fatalf("ticket = %s after the refused push, want in_qa", it.Status)
		}
		if _, ok := e.storedEvidence(); ok {
			t.Fatal("a report is stored after the refused push")
		}
		e.postQAChecks()
		if runs := e.api.World.CheckRunsAt(head); len(runs) != 0 {
			t.Fatalf("check runs after the refused push = %v, want none", runs)
		}
	})
}

// C25: a text evidence file that holds a secret refuses with
// SECRET_IN_OUTPUT through the verb. Nothing is pushed, and no row
// applies.
func TestC25TheVerbRefusesAnEvidenceFileThatHoldsASecret(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	files := map[string][]byte{"web/console.log": []byte("POST /login key=" + storedSecret + "\n")}
	res := e.qaReportWith(id, e.status(id).Epoch, "k1", noted(head, "web/console.log"), files)
	wantRefused(t, res, cli.CodeSecretInOutput, "secret_in_evidence", "web/console.log")
	if strings.Contains(res.Refusal.Hint, storedSecret) {
		t.Fatalf("the hint %q holds the secret", res.Refusal.Hint)
	}
	if e.evidenceCommit(id) != "" {
		t.Fatal("the evidence of a refused report was pushed")
	}
	if it := e.status(id); it.Status != machine.InQA {
		t.Fatalf("ticket = %s, want in_qa", it.Status)
	}
}

// C25: after an accepted report with a screenshot, no fact and no note of
// the ticket holds image data. The report's text links the screenshot.
func TestC25NoFactOfTheTicketHoldsImageDataAfterAReport(t *testing.T) {
	e := qaEnv(t)
	id, head := e.qaTicket(mayChange("apps/web/src/a.ts"), verifyWeb, "")
	files := map[string][]byte{"web/save-390.png": screenshot}
	mustOK(t, e.qaReportWith(id, e.status(id).Epoch, "k1", noted(head, "web/save-390.png"), files))
	it := e.status(id)
	if !strings.Contains(it.Fact(machine.FactReview), blobLink(e.evidenceCommit(id), "web/save-390.png")) {
		t.Fatalf("the review %q does not link the screenshot", it.Fact(machine.FactReview))
	}
	ctx := context.Background()
	issue, err := e.bx.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	events, err := e.bx.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	history, _ := json.Marshal(events)
	// The base64 of a PNG file starts with iVBORw0KGgo.
	marks := []string{"data:image/", base64.StdEncoding.EncodeToString(screenshot[:8])[:11], string(screenshot[:8])}
	texts := []string{issue.Description, string(history)}
	for _, v := range issue.Metadata {
		texts = append(texts, v)
	}
	for _, text := range texts {
		for _, m := range marks {
			if strings.Contains(text, m) {
				t.Fatalf("a fact or note of the ticket holds image data %q: %.200q", m, text)
			}
		}
	}
}
