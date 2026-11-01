package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/store"
)

// TestC28ScopeChecksAsksForChecksWrite: the token that posts loop/qa
// holds checks write and nothing more.
func TestC28ScopeChecksAsksForChecksWrite(t *testing.T) {
	want := map[string]string{"checks": "write", "metadata": "read"}
	if got := Permissions(ScopeChecks); !maps.Equal(got, want) {
		t.Fatalf("Permissions(ScopeChecks) = %v, want %v", got, want)
	}
}

// killAfter stands for a server that dies after GitHub answered one
// call: the call lands, and the caller never reads the answer.
type killAfter struct {
	key  string
	done bool
}

func (k *killAfter) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil || k.done || r.Method+" "+r.URL.Path != k.key {
		return resp, err
	}
	k.done = true
	_ = resp.Body.Close()
	return nil, errors.New("killed after the call")
}

func qaPost(sha, conclusion string) CheckRunPayload {
	return CheckRunPayload{Project: testProject, Item: "tk-1", SHA: sha, Name: QAContext, Conclusion: conclusion,
		Title: "QA " + conclusion, Summary: "s", Text: "t"}
}

// TestC26PostCheckRunKeepsOneCheckRunAfterAKillBetweenTheCreateAndTheRow:
// a server that dies after GitHub created the head's check run, and before
// qa_check_runs stored its id, finds that check run by its name and the
// loop App on the retry, and updates it. A later post updates the stored
// check run with no list. A check run of the same name from another App is
// never the loop's.
func TestC26PostCheckRunKeepsOneCheckRunAfterAKillBetweenTheCreateAndTheRow(t *testing.T) {
	e := newEnv(t)
	w := e.api.World
	ctx := context.Background()
	dying, err := New(Config{
		Apps: OneApp(testAppID, func(context.Context) ([]byte, error) { return gt.KeyPEM(), nil }),
		API:  e.api.URL(), HTTP: &http.Client{Transport: &killAfter{key: "POST /repos/" + testRepo + "/check-runs"}},
		Store: e.st, Redactor: e.red, Projects: staticProjects{e.project}, MirrorRoot: e.root,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dying.PostCheckRun(ctx, e.project, qaPost(shaA, "neutral")); err == nil {
		t.Fatal("the killed post reports success")
	}
	if got, want := w.CheckRunCalls(), []string{"create 9001 " + shaA + " loop/qa neutral"}; !slices.Equal(got, want) {
		t.Fatalf("calls after the kill = %q, want %q", got, want)
	}

	e.restart()
	if err := e.svc.PostCheckRun(ctx, e.project, qaPost(shaA, "failure")); err != nil {
		t.Fatal(err)
	}
	lists := func() int {
		return len(slices.DeleteFunc(e.api.Calls(), func(c string) bool {
			return !strings.HasPrefix(c, "GET /repos/"+testRepo+"/commits/"+shaA+"/check-runs")
		}))
	}
	listed := lists()
	if err := e.svc.PostCheckRun(ctx, e.project, qaPost(shaA, "success")); err != nil {
		t.Fatal(err)
	}
	want := []string{"create 9001 " + shaA + " loop/qa neutral", "update 9001 failure", "update 9001 success"}
	if got := w.CheckRunCalls(); !slices.Equal(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if runs := w.CheckRunsAt(shaA); len(runs) != 1 || runs[0]["conclusion"] != "success" {
		t.Fatalf("check runs at the head = %v, want one with success", runs)
	}
	if lists() != listed {
		t.Fatal("the post after the stored row listed the check runs again")
	}

	// Another App's loop/qa at shaB is not the loop's.
	w.SetRuns(shaB, gt.CheckRun(QAContext, shaB, "completed", "success", time.Now()))
	if err := e.svc.PostCheckRun(ctx, e.project, qaPost(shaB, "failure")); err != nil {
		t.Fatal(err)
	}
	if got := w.CheckRunCalls()[len(want):]; !slices.Equal(got, []string{"create 9002 " + shaB + " loop/qa failure"}) {
		t.Fatalf("calls at shaB = %q", got)
	}
}

// checkRunEffect records one applied checkrun effect, as a QA report or a
// stage-off head records it, and returns its outbox id.
func (e *env) checkRunEffect(sha, post, conclusion string) int64 {
	e.t.Helper()
	payload, _ := json.Marshal(qaPost(sha, conclusion))
	var id int64
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		in, err := tx.RecordIntent(store.Intent{Project: testProject, Item: "tk-1", Kind: "ticket", Verb: "qa_pass"},
			[]store.OutboxRow{{EffectKind: EffectCheckRun, NaturalKey: checkRunKey(testProject, sha, post), Payload: payload}}, nil)
		if err != nil {
			return err
		}
		if err := tx.MarkApplied(in.ID, nil); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT id FROM outbox WHERE intent_id = ?`, in.ID).Scan(&id)
	}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) outboxRow(id int64) (state, note string) {
	e.t.Helper()
	if err := e.st.Write(context.Background(), func(tx *storeTx) error {
		return tx.QueryRow(`SELECT state, last_error FROM outbox WHERE id = ?`, id).Scan(&state, &note)
	}); err != nil {
		e.t.Fatal(err)
	}
	return state, note
}

// TestC26RunCheckRunEffectsPostsTheNewestEffectOfEachHead: of the due
// effects of one head, only the newest posts. Each older one is done with
// no post, and a note names the newer one, even while it waits on a
// retry. Another head posts its own.
func TestC26RunCheckRunEffectsPostsTheNewestEffectOfEachHead(t *testing.T) {
	e := newEnv(t)
	w := e.api.World
	ctx := context.Background()
	off := e.checkRunEffect(shaA, "off", "neutral")
	retry := e.checkRunEffect(shaA, "carried", "neutral")
	e.exec(`UPDATE outbox SET attempts = 1, next_attempt_at = ? WHERE id = ?`, time.Now().Add(time.Hour).UnixMilli(), retry)
	report := e.checkRunEffect(shaA, "run-1", "failure")
	other := e.checkRunEffect(shaB, "off", "neutral")
	if err := e.svc.RunCheckRunEffects(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"create 9001 " + shaA + " loop/qa failure", "create 9002 " + shaB + " loop/qa neutral"}
	if got := w.CheckRunCalls(); !slices.Equal(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	for _, id := range []int64{off, retry} {
		state, note := e.outboxRow(id)
		if state != "done" || !strings.Contains(note, fmt.Sprint(report)) {
			t.Errorf("older effect %d = %s %q, want done with a note that names the newer one", id, state, note)
		}
	}
	for _, id := range []int64{report, other} {
		if state, _ := e.outboxRow(id); state != "done" {
			t.Errorf("posted effect %d = %s, want done", id, state)
		}
	}

	// A later effect of shaA updates the one check run of the head.
	e.checkRunEffect(shaA, "run-2", "success")
	if err := e.svc.RunCheckRunEffects(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.CheckRunCalls(); !slices.Equal(got, append(want, "update 9001 success")) {
		t.Fatalf("calls = %q", got)
	}
}
