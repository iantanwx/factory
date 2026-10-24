package github

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/brief"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// goLog is the log of a gates job whose test gate failed, as the jobs API
// returns it: a timestamp and colour codes on each line.
func goLog() string {
	lines := []string{
		"\ufeff2026-10-12T10:00:00.0000000Z ##[group]Run loop gates",
		"2026-10-12T10:00:01.0000000Z == format",
		"2026-10-12T10:00:02.0000000Z == test",
		"2026-10-12T10:00:03.0000000Z ok  \texample.com/x/other\t0.01s",
		"2026-10-12T10:00:04.0000000Z \x1b[31m--- FAIL: TestAdd (0.00s)\x1b[0m",
		"2026-10-12T10:00:04.1000000Z     add_test.go:9: got 3, want 4",
		"2026-10-12T10:00:04.2000000Z --- FAIL: TestSub (0.00s)",
		"2026-10-12T10:00:04.2500000Z     --- FAIL: TestSub/negative (0.00s)",
		"2026-10-12T10:00:04.3000000Z FAIL",
		"2026-10-12T10:00:04.4000000Z FAIL\texample.com/x/pkg\t0.02s",
		"2026-10-12T10:00:04.5000000Z --- FAIL: TestAdd (0.00s)",
		"2026-10-12T10:00:04.6000000Z FAIL\texample.com/x/pkg\t0.02s",
		"2026-10-12T10:00:05.0000000Z FAIL test",
		"2026-10-12T10:00:06.0000000Z ##[error]Process completed with exit code 1.",
	}
	return strings.Join(lines, "\n") + "\n"
}

// The parse names each failing Go test once with its package, and the
// gate that `loop gates` names in `FAIL <gate>`. The excerpt starts at the
// first failure, with no timestamp and no colour.
func TestParseLogNamesTheFailingTestsAndTheGate(t *testing.T) {
	got := ParseLog(goLog())
	if got.Gate != "test" {
		t.Fatalf("gate = %q, want test", got.Gate)
	}
	want := []brief.FailedTest{
		{Package: "example.com/x/pkg", Name: "TestAdd"},
		{Package: "example.com/x/pkg", Name: "TestSub"},
		{Package: "example.com/x/pkg", Name: "TestSub/negative"},
	}
	if !slices.Equal(got.Tests, want) {
		t.Fatalf("tests = %+v, want %+v", got.Tests, want)
	}
	wantExcerpt := []string{
		"--- FAIL: TestAdd (0.00s)",
		"    add_test.go:9: got 3, want 4",
		"--- FAIL: TestSub (0.00s)",
		"    --- FAIL: TestSub/negative (0.00s)",
		"FAIL",
		"FAIL\texample.com/x/pkg\t0.02s",
		"--- FAIL: TestAdd (0.00s)",
		"FAIL\texample.com/x/pkg\t0.02s",
		"FAIL test",
		"##[error]Process completed with exit code 1.",
	}
	if !slices.Equal(got.Excerpt, wantExcerpt) {
		t.Fatalf("excerpt = %q", got.Excerpt)
	}
}

// The excerpt holds at most 40 lines from the first failure, and a line
// longer than 300 characters is cut.
func TestParseLogCapsTheExcerpt(t *testing.T) {
	lines := []string{"setup", "--- FAIL: TestLong (0.00s)", strings.Repeat("x", 400)}
	for i := range 60 {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	lines = append(lines, "FAIL\texample.com/x/long\t0.01s")
	got := ParseLog(strings.Join(lines, "\n"))
	if len(got.Excerpt) != 40 || got.Excerpt[0] != "--- FAIL: TestLong (0.00s)" || got.Excerpt[39] != "line 37" {
		t.Fatalf("excerpt has %d lines: first %q, last %q", len(got.Excerpt), got.Excerpt[0], got.Excerpt[len(got.Excerpt)-1])
	}
	if got.Excerpt[1] != strings.Repeat("x", 300)+"…" {
		t.Fatalf("the long line is %d bytes", len(got.Excerpt[1]))
	}
	if !slices.Equal(got.Tests, []brief.FailedTest{{Package: "example.com/x/long", Name: "TestLong"}}) {
		t.Fatalf("tests = %+v", got.Tests)
	}
}

// A log that names no failing test is a lint or another gate. The excerpt
// is then its last 40 lines. A package that failed before any test ran
// is named on its own, and a vitest FAIL line names its file and test.
func TestParseLogWithNoFailingTestQuotesTheTail(t *testing.T) {
	var lines []string
	for i := range 50 {
		lines = append(lines, fmt.Sprintf("lint %d", i))
	}
	lines = append(lines, "FAIL lint")
	got := ParseLog(strings.Join(lines, "\n") + "\n\n")
	if got.Gate != "lint" || len(got.Tests) != 0 || len(got.Excerpt) != 40 || got.Excerpt[0] != "lint 11" || got.Excerpt[39] != "FAIL lint" {
		t.Fatalf("gate %q, tests %+v, excerpt %q", got.Gate, got.Tests, got.Excerpt)
	}
	build := ParseLog("# example.com/x/pkg\npkg/a.go:3:1: syntax error\nFAIL\texample.com/x/pkg [build failed]\n" +
		" FAIL  src/a.test.ts > adds two\n FAIL  src/b.test.ts\nFAIL test\n")
	want := []brief.FailedTest{
		{Package: "example.com/x/pkg", Name: "(the package failed before any test ran)"},
		{Package: "src/a.test.ts", Name: "adds two"},
		{Package: "src/b.test.ts", Name: "(the file failed before any test ran)"},
	}
	if !slices.Equal(build.Tests, want) || build.Gate != "test" {
		t.Fatalf("tests = %+v, gate %q", build.Tests, build.Gate)
	}
}

// RedBuild reads the failed jobs of the item's open pull request at its
// head and parses each log. A log that does not read is named unreadable,
// and the read goes on.
func TestRedBuildReadsEachFailedJobOfTheItemPR(t *testing.T) {
	e := newEnv(t)
	head := strings.Repeat("d", 40)
	e.api.World.Add(&gt.PR{Head: BranchFor("tk-1"), HeadSHA: head, Base: "main", State: "open"})
	runs := "/repos/" + gt.Repo + "/actions/runs"
	e.api.Route("GET "+runs, func(w http.ResponseWriter, r *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"workflow_runs": []any{
			map[string]any{"id": 11, "name": "ci", "head_sha": head, "status": "completed", "conclusion": "failure"},
			map[string]any{"id": 12, "name": "docs", "head_sha": head, "status": "completed", "conclusion": "success"},
		}})
	})
	e.api.Route("GET "+runs+"/11/jobs", func(w http.ResponseWriter, r *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"jobs": []any{
			map[string]any{"id": 101, "run_id": 11, "name": "gates", "head_sha": head, "conclusion": "failure", "html_url": "https://example.invalid/job/101"},
			map[string]any{"id": 102, "run_id": 11, "name": "acceptance", "head_sha": head, "conclusion": "failure", "html_url": "https://example.invalid/job/102"},
			map[string]any{"id": 103, "run_id": 11, "name": "build", "head_sha": head, "conclusion": "success", "html_url": "https://example.invalid/job/103"},
		}})
	})
	jobs := "/repos/" + gt.Repo + "/actions/jobs"
	e.api.Route("GET "+jobs+"/101/logs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(goLog()))
	})
	e.api.Route("GET "+jobs+"/102/logs", func(w http.ResponseWriter, r *http.Request) {
		gt.WriteJSON(w, 404, map[string]string{"message": "Not Found"})
	})
	got, err := e.svc.RedBuild(context.Background(), testProject, "tk-1")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.PR != 1 || got.Head != head || len(got.Checks) != 2 {
		t.Fatalf("red build = %+v", got)
	}
	gates, acc := got.Checks[0], got.Checks[1]
	if gates.Name != "gates" || gates.URL != "https://example.invalid/job/101" || gates.Gate != "test" ||
		len(gates.Tests) != 3 || gates.Excerpt[0] != "--- FAIL: TestAdd (0.00s)" || gates.Unreadable != "" {
		t.Fatalf("gates = %+v", gates)
	}
	if acc.Name != "acceptance" || acc.Unreadable == "" || len(acc.Excerpt) != 0 {
		t.Fatalf("acceptance = %+v", acc)
	}
	if none, err := e.svc.RedBuild(context.Background(), testProject, "tk-2"); err != nil || none != nil {
		t.Fatalf("an item with no pull request = %+v, %v", none, err)
	}
}
