package github

import (
	"context"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// checkPages answers the check runs of shaA a page of 100 at a time: full
// green pages up to full, then one red run on the page after them.
func checkPages(e *env, full int) {
	at := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	e.api.Route("GET /repos/"+testRepo+"/commits/"+shaA+"/check-runs", func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		runs := []map[string]any{}
		switch {
		case page <= full:
			for i := range 100 {
				runs = append(runs, gt.CheckRun("ci-"+strconv.Itoa((page-1)*100+i), shaA, "completed", "success", at))
			}
		case page == full+1:
			runs = append(runs, gt.CheckRun("late", shaA, "completed", "failure", at))
		}
		writeJSON(w, 200, map[string]any{"total_count": full*100 + 1, "check_runs": runs})
	})
}

func checkPagesRead(e *env) int {
	n := 0
	for _, c := range e.api.Calls() {
		if strings.Contains(c, "/check-runs?") {
			n++
		}
	}
	return n
}

// Ported from MAX_PAGES in scripts/sdlc/graphql-pages.ts (alpha): a read
// that meets the page cap with another page still to come fails, and
// never answers from the pages it did read. A red run past the cap must
// not leave the head green.
func TestChecksFailClosedAtThePageCap(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	checkPages(e, 10)
	rep, err := e.svc.Checks(context.Background(), e.project, shaA)
	if err == nil {
		t.Fatalf("checks past the page cap answered %s with %d runs and no error", rep.State, len(rep.Checks))
	}
	if !strings.Contains(err.Error(), "more than 1000 check runs") {
		t.Fatalf("err = %v", err)
	}
	if n := checkPagesRead(e); n != 10 {
		t.Fatalf("read %d pages, want 10", n)
	}
}

// Under the cap every page is read, and the last short page ends the read.
func TestChecksReadEveryPageUnderTheCap(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	checkPages(e, 2)
	rep, err := e.svc.Checks(context.Background(), e.project, shaA)
	if err != nil {
		t.Fatal(err)
	}
	if rep.State != CIRed || len(rep.Checks) != 201 {
		t.Fatalf("state %s with %d checks; want red with 201", rep.State, len(rep.Checks))
	}
	if n := checkPagesRead(e); n != 3 {
		t.Fatalf("read %d pages, want 3", n)
	}
}

// C-bug-1: a failure in the newest attempt of a rerun is red as before.
// Only the check the rerun has not reached yet waits as pending.
func TestARerunsOwnFailureIsRedAndItsEarlierAttemptPending(t *testing.T) {
	const head = "c8bca9c13f95573d8fad8cab34a06be57f127a1b"
	at := func(m int) *time.Time { t := time.Date(2026, 11, 1, 9, m, 0, 0, time.UTC); return &t }
	job := func(id string) string {
		return "https://github.com/" + testRepo + "/actions/runs/41292919362/job/" + id
	}
	runs := []CheckRun{
		{ID: 1, Name: "acceptance", HeadSHA: head, Status: "completed", Conclusion: "failure", StartedAt: at(10), HTMLURL: job("1")},
		{ID: 2, Name: "gates", HeadSHA: head, Status: "completed", Conclusion: "failure", StartedAt: at(10), HTMLURL: job("2")},
		{ID: 3, Name: "verify", HeadSHA: head, Status: "completed", Conclusion: "failure", StartedAt: at(11), HTMLURL: job("3")},
		// Attempt 2: gates failed again, and acceptance still runs.
		{ID: 4, Name: "gates", HeadSHA: head, Status: "completed", Conclusion: "failure", StartedAt: at(37), HTMLURL: job("4")},
		{ID: 5, Name: "acceptance", HeadSHA: head, Status: "in_progress", StartedAt: at(37), HTMLURL: job("5")},
	}

	rep := Classify(head, runs, nil)

	got := map[string]string{}
	for _, c := range rep.Checks {
		got[c.Name] = c.State
	}
	want := map[string]string{"acceptance": CIPending, "gates": CIRed, "verify": CIPending}
	if rep.State != CIRed || !maps.Equal(got, want) {
		t.Fatalf("head %s with checks %v; want red with %v", rep.State, got, want)
	}
}

// C-bug-1: two jobs of one workflow run may share a name. With no rerun,
// the only attempt is the newest one, so its failure is red as before.
func TestAFailureWithNoRerunIsRedWhenTwoJobsShareAName(t *testing.T) {
	const head = "c8bca9c13f95573d8fad8cab34a06be57f127a1b"
	at := func(m, s int) *time.Time { t := time.Date(2026, 11, 1, 9, m, s, 0, time.UTC); return &t }
	job := func(id string) string { return "https://github.com/" + testRepo + "/actions/runs/7/job/" + id }
	for name, runs := range map[string][]CheckRun{
		"no times": {
			{ID: 1, Name: "test", HeadSHA: head, Status: "completed", Conclusion: "success", HTMLURL: job("1")},
			{ID: 2, Name: "lint", HeadSHA: head, Status: "completed", Conclusion: "failure", HTMLURL: job("2")},
			{ID: 3, Name: "test", HeadSHA: head, Status: "completed", Conclusion: "success", HTMLURL: job("3")},
		},
		"jobs overlap": {
			{ID: 1, Name: "test", HeadSHA: head, Status: "completed", Conclusion: "success", StartedAt: at(10, 0), CompletedAt: at(14, 0), HTMLURL: job("1")},
			{ID: 2, Name: "test", HeadSHA: head, Status: "completed", Conclusion: "success", StartedAt: at(10, 2), CompletedAt: at(15, 0), HTMLURL: job("2")},
			{ID: 3, Name: "lint", HeadSHA: head, Status: "completed", Conclusion: "failure", StartedAt: at(10, 1), CompletedAt: at(12, 0), HTMLURL: job("3")},
		},
	} {
		rep := Classify(head, runs, nil)

		got := map[string]string{}
		for _, c := range rep.Checks {
			got[c.Name] = c.State
		}
		want := map[string]string{"lint": CIRed, "test": CIGreen}
		if rep.State != CIRed || !maps.Equal(got, want) {
			t.Errorf("%s: head %s with checks %v; want red with %v", name, rep.State, got, want)
		}
	}
}
