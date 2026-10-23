package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// `pr open` twice on one head branch opens one PR.
func TestOpenPRTwiceOpensOnePR(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	ctx := context.Background()
	first, created, err := e.svc.OpenPR(ctx, e.project, BranchFor("tk-1"), "title", "body")
	if err != nil || !created {
		t.Fatalf("first open: %v created=%v", err, created)
	}
	second, created, err := e.svc.OpenPR(ctx, e.project, BranchFor("tk-1"), "title", "body")
	if err != nil || created || second.Number != first.Number {
		t.Fatalf("second open: %v created=%v #%d", err, created, second.Number)
	}
	if n := len(w.PRs()); n != 1 {
		t.Fatalf("%d PRs, want 1", n)
	}
	if pr := w.PRs()[0]; pr.Head != "loop/tk-1" || pr.Base != "main" {
		t.Fatalf("PR %+v", pr)
	}
}

// Ported from premerge-gate.test.ts (alpha): the files come from REST, a
// page of 100 at a time, so no list stops at 100.
func TestPRFilesReadEveryPage(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	w.SetFiles(250)
	pr := w.Add(&gt.PR{Head: "loop/tk-1", HeadSHA: shaA})
	files, err := e.svc.PRFiles(context.Background(), e.project, pr.Number)
	if err != nil || len(files) != 250 || files[249].Path != "f250.txt" {
		t.Fatalf("files: %d %v", len(files), err)
	}
	var pages []string
	for _, c := range e.api.Calls() {
		if strings.Contains(c, "/files?") {
			pages = append(pages, c)
		}
	}
	if len(pages) != 3 || !strings.HasSuffix(pages[0], "/pulls/1/files?per_page=100&page=1") {
		t.Fatalf("pages read: %v", pages)
	}
}

// A retried reply finds its marker and posts nothing twice.
func TestReplyIsIdempotentByMarker(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	pr := w.Add(&gt.PR{Head: "loop/tk-1", HeadSHA: shaA})
	ctx := context.Background()
	c, replayed, err := e.svc.Reply(ctx, e.project, pr.Number, 0, "done", "key1")
	if err != nil || replayed || c.AuthorClass != AuthorApp {
		t.Fatalf("first reply: %+v %v %v", c, replayed, err)
	}
	_, replayed, err = e.svc.Reply(ctx, e.project, pr.Number, 0, "done", "key1")
	if err != nil || !replayed {
		t.Fatalf("second reply: replayed=%v %v", replayed, err)
	}
	if n := len(w.Comments(pr.Number)); n != 1 {
		t.Fatalf("%d comments, want 1", n)
	}
	threads, err := e.svc.Threads(ctx, e.project, pr.Number)
	if err != nil || len(threads) != 1 || threads[0].AuthorClass != AuthorApp {
		t.Fatalf("threads: %+v %v", threads, err)
	}
}

// Ported from threadsFrom in scripts/sdlc/github-pull.ts:154-184 (alpha):
// `pr threads` names the review thread of each review comment, whether
// the thread is resolved or outdated, and whether it awaits us: open, and
// the last word is not the loop App's. An outdated thread still awaits
// us, because the comment was about something real. A conversation
// comment belongs to no thread.
func TestThreadsNameResolvedOutdatedAndAwaitingUs(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	pr := w.Add(&gt.PR{Head: "loop/tk-1", HeadSHA: shaA})
	ctx := context.Background()
	open := w.Review(pr.Number, "owner", false, 0, "rename this")
	answered := w.Review(pr.Number, "owner", false, 0, "add a test")
	w.Review(pr.Number, "loop[bot]", true, answered, "added")
	resolved := w.Review(pr.Number, "owner", false, 0, "typo")
	w.SetThread(resolved, true, false, false)
	stale := w.Review(pr.Number, "owner", false, 0, "old line")
	w.SetThread(stale, false, true, false)
	if _, _, err := e.svc.Reply(ctx, e.project, pr.Number, stale, "looking", "key1"); err != nil {
		t.Fatal(err)
	}
	w.Review(pr.Number, "owner", false, stale, "still wrong")
	if _, _, err := e.svc.Reply(ctx, e.project, pr.Number, 0, "summary", "key2"); err != nil {
		t.Fatal(err)
	}

	cs, err := e.svc.Threads(ctx, e.project, pr.Number)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cs)
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		Kind   string
		Body   string
		Thread *struct {
			ID         int64 `json:"id"`
			Resolved   bool  `json:"resolved"`
			Outdated   bool  `json:"outdated"`
			AwaitingUs bool  `json:"awaiting_us"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		line := r.Kind + " " + strings.SplitN(r.Body, "\n", 2)[0]
		if r.Thread != nil {
			line += fmt.Sprintf(" thread=%d resolved=%v outdated=%v awaiting_us=%v", r.Thread.ID, r.Thread.Resolved,
				r.Thread.Outdated, r.Thread.AwaitingUs)
		}
		got = append(got, line)
	}
	want := []string{
		fmt.Sprintf("review rename this thread=%d resolved=false outdated=false awaiting_us=true", open),
		fmt.Sprintf("review add a test thread=%d resolved=false outdated=false awaiting_us=false", answered),
		fmt.Sprintf("review added thread=%d resolved=false outdated=false awaiting_us=false", answered),
		fmt.Sprintf("review typo thread=%d resolved=true outdated=false awaiting_us=false", resolved),
		fmt.Sprintf("review old line thread=%d resolved=false outdated=true awaiting_us=true", stale),
		fmt.Sprintf("review looking thread=%d resolved=false outdated=true awaiting_us=true", stale),
		fmt.Sprintf("review still wrong thread=%d resolved=false outdated=true awaiting_us=true", stale),
		"conversation summary",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("threads:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A review comment whose thread GraphQL does not name has no known state,
// so the read fails rather than call the thread open.
func TestThreadsRefuseAThreadWithNoState(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	pr := w.Add(&gt.PR{Head: "loop/tk-1", HeadSHA: shaA})
	root := w.Review(pr.Number, "owner", false, 0, "rename this")
	w.SetThread(root, false, false, true)
	if cs, err := e.svc.Threads(context.Background(), e.project, pr.Number); err == nil {
		t.Fatalf("threads = %+v, want an error", cs)
	}
}

// Ported from land-queue.test.ts (alpha): no source of the GitHub actor
// rebases a branch, force-pushes one without a lease, or asks GitHub to
// update one. A conflict goes back to the implementer.
func TestNoSourceRebasesOrForcePushes(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	banned := []*regexp.Regexp{
		regexp.MustCompile(`"rebase"`), regexp.MustCompile(`update-branch`),
		regexp.MustCompile(`"--force"`), regexp.MustCompile(`"-f"`), regexp.MustCompile(`"\+" \+ .*refs/heads`),
	}
	read := 0
	for _, f := range entries {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Clean(f.Name()))
		if err != nil {
			t.Fatal(err)
		}
		read++
		for _, re := range banned {
			if re.Match(data) {
				t.Errorf("%s holds %s", f.Name(), re)
			}
		}
	}
	if read < 10 {
		t.Fatalf("the scan read %d sources", read)
	}
}
