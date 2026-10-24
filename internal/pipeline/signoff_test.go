package pipeline_test

import (
	"net/http"
	"slices"
	"strconv"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// signoffConfig is a project config that names one sign-off reader.
const signoffConfig = "merge:\n  mechanism: direct\nsignoff:\n  readers: [octocat]\n"

// withReader lands a project config on main that names octocat as the
// project's sign-off reader.
func (w *world) withReader() {
	w.t.Helper()
	gt.Commit(w.t, w.checkout, "config", map[string]string{".loop/config.yaml": signoffConfig})
	gt.Git(w.t, w.checkout, "push", "-q", w.git.RemoteDir(), "HEAD:main")
}

// specOpen is a spec author's push and pr open, as run.
func (w *world) specOpen(epic, run string) map[string]any {
	w.t.Helper()
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: epic, Stage: machine.StageSpecAuthor, RunID: run}); res.Refusal != nil {
		w.t.Fatalf("spec author claim: %+v", *res.Refusal)
	}
	gt.Commit(w.t, w.checkout, "spec "+run, map[string]string{"docs/specs/widgets.md": "spec by " + run + "\n"})
	pr := w.run(run, epic, machine.StageSpecAuthor)
	head := gt.Git(w.t, w.checkout, "rev-parse", "HEAD")
	ok(w.t, w.call(pr, "", "branch push", headFlags(head, map[string]string{})))
	return ok(w.t, w.call(pr, "", "pr open", map[string]string{"title": "Widgets spec"}))
}

func signoffOf(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	s, _ := data["signoff"].(map[string]any)
	if s == nil {
		t.Fatalf("no signoff in %v", data)
	}
	return s
}

// The PRD and the spec are the two documents a person merges. The PR of
// each asks the project's sign-off reader for a review when it opens, so
// GitHub notifies that person, and a replay of the PRD submit asks
// nobody twice.
func TestSignoffDocumentPRRequestsTheProjectsReader(t *testing.T) {
	w := newWorld(t)
	w.withReader()
	w.commitPRD(prdText)
	data := ok(t, w.submit("k-submit"))
	epic := data["epic"].(string)
	prd := int(data["pr"].(map[string]any)["number"].(float64))
	if got := w.pr(prd).Reviewers; !slices.Equal(got, []string{"octocat"}) {
		t.Fatalf("the PRD PR requested %v, want [octocat]", got)
	}
	if got := signoffOf(t, data)["reviewers"]; len(got.([]any)) != 1 {
		t.Fatalf("prd submit reported %v", data["signoff"])
	}
	ok(t, w.submit("k-replay"))
	if got := w.pr(prd).Reviewers; len(got) != 1 {
		t.Fatalf("a replayed submit requested %v", got)
	}

	w.review(epic, "pass")
	w.merge(prd)
	w.tick()
	out := w.specOpen(epic, "run-spec-1")
	spec, _ := strconv.Atoi(w.show(epic).Fact(machine.FactDocPR))
	if spec == prd || spec == 0 {
		t.Fatalf("no spec PR: %d", spec)
	}
	if got := w.pr(spec).Reviewers; !slices.Equal(got, []string{"octocat"}) {
		t.Fatalf("the spec PR requested %v, want [octocat]", got)
	}
	if got := signoffOf(t, out)["reviewers"]; len(got.([]any)) != 1 {
		t.Fatalf("pr open reported %v", out["signoff"])
	}
}

// A ticket's PR is no document a person signs off, so its pr open asks
// nobody, even when the project names a reader.
func TestSignoffTicketPRRequestsNobody(t *testing.T) {
	w := newWorld(t)
	w.withReader()
	id, res := w.m.Create(w.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindTicket, Title: "t",
		Verb: "create", Actor: machine.Server("test")})
	if res.Refusal != nil {
		t.Fatalf("create: %+v", *res.Refusal)
	}
	if _, res := w.m.Claim(w.ctx, machine.ClaimRequest{Project: testProject, Item: id, Stage: machine.StageImplementer, RunID: "run-impl"}); res.Refusal != nil {
		t.Fatalf("claim: %+v", *res.Refusal)
	}
	gt.Commit(t, w.checkout, "code", map[string]string{"a.go": "package a\n"})
	pr := w.run("run-impl", id, machine.StageImplementer)
	head := gt.Git(t, w.checkout, "rev-parse", "HEAD")
	ok(t, w.call(pr, "", "branch push", headFlags(head, map[string]string{})))
	out := ok(t, w.call(pr, "", "pr open", map[string]string{"title": "Add a"}))
	n := int(out["pr"].(map[string]any)["number"].(float64))
	if got := w.pr(n).Reviewers; len(got) != 0 {
		t.Fatalf("a ticket PR requested %v", got)
	}
	if _, has := out["signoff"]; has {
		t.Fatalf("a ticket's pr open reported %v", out["signoff"])
	}
}

// GitHub can refuse the request, for example for a login with no access
// to the repository. The PR is open by then, so the submit still lands,
// and its answer names the reader to ask by hand.
func TestSignoffRequestRefusedStillOpensTheDocument(t *testing.T) {
	w := newWorld(t)
	w.withReader()
	w.api.World.FailNext("POST /repos/"+gt.Repo+"/pulls/1/requested_reviewers", http.StatusUnprocessableEntity)
	w.commitPRD(prdText)
	data := ok(t, w.submit("k-submit"))
	if got := w.show(data["epic"].(string)).Status; got != machine.PRDReview {
		t.Fatalf("epic is %s", got)
	}
	if msg, _ := signoffOf(t, data)["warning"].(string); msg == "" {
		t.Fatalf("no warning in %v", data["signoff"])
	}
}
