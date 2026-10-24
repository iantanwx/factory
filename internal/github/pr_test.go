package github

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// OpenPulls reads every page of the open pull requests, and no closed one,
// for the `## Depends on` hold.
func TestOpenPullsReadEveryOpenPage(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	for i := range 120 {
		w.Add(&gt.PR{Head: "feat/b" + strconv.Itoa(i+1)})
	}
	w.Add(&gt.PR{Head: "feat/closed", State: "closed"})
	open, err := e.svc.OpenPulls(context.Background(), testProject)
	if err != nil || len(open) != 120 {
		t.Fatalf("open: %d %v", len(open), err)
	}
	if open[0].Number != 1 || open[0].Head.Ref != "feat/b1" || open[119].Head.Ref != "feat/b120" {
		t.Fatalf("open: %+v ... %+v", open[0], open[119])
	}
	var pages []string
	for _, c := range e.api.Calls() {
		if strings.Contains(c, "/pulls?") {
			pages = append(pages, c)
		}
	}
	if len(pages) != 2 || !strings.HasSuffix(pages[0], "/pulls?page=1&per_page=100&state=open") {
		t.Fatalf("pages read: %v", pages)
	}
}
