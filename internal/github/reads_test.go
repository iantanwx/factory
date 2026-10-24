package github

import (
	"context"
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
