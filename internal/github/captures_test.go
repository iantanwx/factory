package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/captures"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/gitx/gittest"
	"github.com/iantanwx/loop/internal/machine"
)

// otherShot is a second PNG, so a test tells two captures apart.
var otherShot = append([]byte("\x89PNG\r\n\x1a\n"), []byte("another picture")...)

// citingTicket is a ticket in progress of epic tk-9 whose screens fact is
// screens. It holds no other design fact.
func (e *env) citingTicket(screens string) string {
	e.t.Helper()
	id := e.ticket(machine.InProgress, "")
	facts := map[string]string{machine.FactEpic: "tk-9"}
	if screens != "" {
		facts[machine.FactScreens] = screens
	}
	e.put(id, machine.InProgress, facts)
	return id
}

// capturesPush is `loop branch push --captures` of the checkout's HEAD
// from the implementer run of item, with files in the captures directory.
// A nil files sends no --captures.
func (e *env) capturesPush(item string, files map[string][]byte) cli.Result {
	e.t.Helper()
	head := gitT(e.t, e.checkout, "rev-parse", "HEAD")
	extra := map[string]string{cli.ExtraHead: head}
	flags := map[string]string{}
	if files != nil {
		dir := e.t.TempDir()
		for name, data := range files {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				e.t.Fatal(err)
			}
		}
		sha, err := cli.CaptureCommit(context.Background(), e.checkout, dir, head)
		if err != nil {
			e.t.Fatal(err)
		}
		flags["captures"] = dir
		extra[cli.ExtraCaptures] = sha
	}
	return e.runCall("branch push", item, machine.StageImplementer, flags, extra)
}

// remoteBlob reads path on a branch of the bare remote, or "" when the
// branch lacks it.
func (e *env) remoteBlob(branch, path string) string {
	cmd := gittest.Command("cat-file", "blob", "refs/heads/"+branch+":"+path)
	cmd.Dir = e.git.RemoteDir()
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// recordRefUpdates installs a hook in the bare remote that logs each ref
// it updates, in order, and returns the reader of that log.
func (e *env) recordRefUpdates() func() []string {
	e.t.Helper()
	log := filepath.Join(e.t.TempDir(), "updates")
	hook := filepath.Join(e.git.RemoteDir(), "hooks", "post-receive")
	writeFile(e.t, hook, "#!/bin/sh\nwhile read old new ref; do echo \"$ref\" >> "+log+"; done\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		e.t.Fatal(err)
	}
	return func() []string {
		raw, err := os.ReadFile(log)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			e.t.Fatal(err)
		}
		return strings.Fields(string(raw))
	}
}

// C10: a push with captures of a ticket that cites S1.1 and S1.2 puts
// <head>/S1.1.png and <head>/S1.2.png with the pushed bytes, and
// screens.json, on loop/<ticket>-captures. A push at a new head adds that
// head's directory and keeps the first. A push at the same head replaces
// that head's files.
func TestC10PushWithCapturesPutsEachCaptureAtTheHeadsPath(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	tk := e.citingTicket("S1.1,S1.2")
	branch := captures.Branch(tk)
	first := commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	res := e.capturesPush(tk, map[string][]byte{"S1.1.png": screenshot, "S1.2.png": otherShot})
	mustOK(t, res)
	if got, ok := res.Data.(PushResult); !ok || got.Captures == nil || got.Captures.Branch != branch ||
		got.Captures.Tip != e.remoteSHA(branch) || !slices.Equal(got.Captures.States, []string{"S1.1", "S1.2"}) {
		t.Fatalf("push result %+v, want the captures part of %s at its tip", res.Data, branch)
	}
	if got := e.remoteSHA(BranchFor(tk)); got != first {
		t.Fatalf("remote %s = %q, want %s", BranchFor(tk), got, first)
	}
	if e.remoteBlob(branch, first+"/S1.1.png") != string(screenshot) || e.remoteBlob(branch, first+"/S1.2.png") != string(otherShot) {
		t.Fatalf("%s does not hold the pushed bytes under %s/", branch, first)
	}
	var m captures.Manifest
	if err := json.Unmarshal([]byte(e.remoteBlob(branch, captures.ManifestFile)), &m); err != nil {
		t.Fatalf("screens.json: %v", err)
	}
	want := captures.Manifest{Ticket: tk, Epic: "tk-9", Record: "docs/design/tk-9/record.md", States: []string{"S1.1", "S1.2"}}
	if m.Ticket != want.Ticket || m.Epic != want.Epic || m.Record != want.Record || !slices.Equal(m.States, want.States) {
		t.Fatalf("screens.json %+v, want %+v", m, want)
	}

	second := commit(t, e.checkout, "two", map[string]string{"b.txt": "b\n"})
	mustOK(t, e.capturesPush(tk, map[string][]byte{"S1.1.png": otherShot, "S1.2.png": otherShot}))
	if e.remoteBlob(branch, second+"/S1.1.png") != string(otherShot) {
		t.Fatalf("%s holds no capture under the new head %s", branch, second)
	}
	if e.remoteBlob(branch, first+"/S1.1.png") != string(screenshot) {
		t.Fatalf("the push at a new head dropped the captures of %s", first)
	}

	mustOK(t, e.capturesPush(tk, map[string][]byte{"S1.1.png": screenshot, "S1.2.png": screenshot}))
	if e.remoteBlob(branch, second+"/S1.2.png") != string(screenshot) {
		t.Fatalf("a second push at %s kept the old capture of S1.2", second)
	}
	if e.remoteSHA(BranchFor(tk)) != second {
		t.Fatal("the ticket branch moved off the head")
	}
}

// C5: the push reads the ticket's screens fact and nothing else. A ticket
// whose only design fact is screens = S1.2,S1.1 pushes, and the manifest
// lists the ids in that order.
func TestC5PushReadsOnlyTheScreensFact(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	tk := e.citingTicket("S1.2,S1.1")
	commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	mustOK(t, e.capturesPush(tk, map[string][]byte{"S1.1.png": screenshot, "S1.2.png": screenshot}))
	var m captures.Manifest
	if err := json.Unmarshal([]byte(e.remoteBlob(captures.Branch(tk), captures.ManifestFile)), &m); err != nil {
		t.Fatalf("screens.json: %v", err)
	}
	if !slices.Equal(m.States, []string{"S1.2", "S1.1"}) {
		t.Fatalf("the manifest lists %v, want [S1.2 S1.1]", m.States)
	}
}

// C11: for a ticket that cites S1.1 and S1.2, a push with no --captures or
// with S1.1.png alone gets captures_missing naming S1.2, a set with
// S3.1.png gets capture_unknown, and text bytes named S1.1.png get
// capture_not_png. After each refusal neither branch moved on the remote.
// A push with --captures of a ticket with no screens fact gets
// captures_not_cited, and of an epic captures_ticket_only.
func TestC11PushWithAnIncompleteOrWrongSetPushesNothing(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	tk := e.citingTicket("S1.1,S1.2")
	first := commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	mustOK(t, e.capturesPush(tk, map[string][]byte{"S1.1.png": screenshot, "S1.2.png": screenshot}))
	tip := e.remoteSHA(captures.Branch(tk))
	commit(t, e.checkout, "two", map[string]string{"b.txt": "b\n"})

	cases := []struct {
		name  string
		files map[string][]byte
		rule  string
		names []string
	}{
		{"no captures", nil, "captures_missing", []string{"S1.1", "S1.2"}},
		{"one state alone", map[string][]byte{"S1.1.png": screenshot}, "captures_missing", []string{"S1.2"}},
		{"an uncited state", map[string][]byte{"S1.1.png": screenshot, "S1.2.png": screenshot, "S3.1.png": screenshot},
			"capture_unknown", []string{"S3.1.png"}},
		{"text bytes", map[string][]byte{"S1.1.png": []byte("not a picture\n"), "S1.2.png": screenshot},
			"capture_not_png", []string{"S1.1.png"}},
	}
	for _, c := range cases {
		res := e.capturesPush(tk, c.files)
		wantRule(t, res, cli.CodeShapeRefused, c.rule)
		for _, n := range c.names {
			if !strings.Contains(res.Refusal.Hint, n) {
				t.Errorf("%s: hint %q names no %s", c.name, res.Refusal.Hint, n)
			}
		}
		if got := e.remoteSHA(BranchFor(tk)); got != first {
			t.Errorf("%s: the ticket branch moved to %s", c.name, got)
		}
		if got := e.remoteSHA(captures.Branch(tk)); got != tip {
			t.Errorf("%s: the capture branch moved to %s", c.name, got)
		}
	}

	plain := e.citingTicket("")
	wantRule(t, e.capturesPush(plain, map[string][]byte{"S1.1.png": screenshot}), cli.CodeShapeRefused, "captures_not_cited")
	ep := e.designEpic(machine.Design)
	gitT(t, e.checkout, "checkout", "-q", "-b", "record", e.base)
	commit(t, e.checkout, "record", map[string]string{"docs/design/" + ep + "/record.md": "# Record\n"})
	head := gitT(t, e.checkout, "rev-parse", "HEAD")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "S1.1.png"), string(screenshot))
	sha, err := cli.CaptureCommit(context.Background(), e.checkout, dir, head)
	if err != nil {
		t.Fatal(err)
	}
	call := e.runCall("branch push", ep, machine.StagePlanner, map[string]string{"captures": dir},
		map[string]string{cli.ExtraHead: head, cli.ExtraCaptures: sha})
	wantRule(t, call, cli.CodeUsage, "captures_ticket_only")
	for _, b := range []string{captures.Branch(plain), BranchFor(plain), captures.Branch(ep), DesignBranchFor(ep)} {
		if got := e.remoteSHA(b); got != "" {
			t.Errorf("remote %s = %q after a refusal; want no branch", b, got)
		}
	}
}

// C12: the capture branch push completes before the ticket branch push
// starts. A crash between the two leaves the captures on the remote and
// the head off it, and the same push run again pushes the head.
func TestC12RemoteHoldsTheCapturesBeforeTheHead(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	updates := e.recordRefUpdates()
	tk := e.citingTicket("S1.1")
	head := commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	set := map[string][]byte{"S1.1.png": screenshot}

	e.svc.crash = func(p string) bool { return p == "captures_pushed" }
	if res := e.capturesPush(tk, set); res.Refusal == nil {
		t.Fatalf("the push passed the kill point: %+v", res)
	}
	if e.remoteBlob(captures.Branch(tk), head+"/S1.1.png") != string(screenshot) {
		t.Fatal("after the crash the remote holds no capture of the head")
	}
	if got := e.remoteSHA(BranchFor(tk)); got != "" {
		t.Fatalf("after the crash the remote holds the head at %s", got)
	}

	e.svc.crash = nil
	mustOK(t, e.capturesPush(tk, set))
	if got := e.remoteSHA(BranchFor(tk)); got != head {
		t.Fatalf("the push run again left %s at %q, want %s", BranchFor(tk), got, head)
	}
	want := []string{"refs/heads/" + captures.Branch(tk), "refs/heads/" + BranchFor(tk)}
	if got := updates(); !slices.Equal(got, want) {
		t.Fatalf("the remote updated %v, want %v", got, want)
	}

	next := commit(t, e.checkout, "two", map[string]string{"b.txt": "b\n"})
	mustOK(t, e.capturesPush(tk, set))
	if got := updates(); !slices.Equal(got[2:], want) {
		t.Fatalf("the push of %s updated %v, want %v", next, got[2:], want)
	}
}

// C13: no capture reaches the ticket's branch or the base. After a citing
// ticket's push and its merge, no object of loop/<ticket> or of main is a
// blob of the capture set, and `pr open` opens its pull request from
// loop/<ticket>.
func TestC13NoCaptureReachesTheBaseOrTheTicketBranch(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	tk := e.citingTicket("S1.1")
	commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	mustOK(t, e.capturesPush(tk, map[string][]byte{"S1.1.png": screenshot}))
	mustOK(t, e.runCall("pr open", tk, machine.StageImplementer, map[string]string{"title": "t", "body": "b"}, nil))
	for _, pr := range w.PRs() {
		if pr.Head != BranchFor(tk) {
			t.Errorf("a pull request opened from %s", pr.Head)
		}
	}

	gitT(t, e.checkout, "checkout", "-q", "main")
	gitT(t, e.checkout, "merge", "-q", "--no-ff", "-m", "merge "+tk, "work")
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), "main")
	blob := strings.TrimSpace(gitStdin(t, e.checkout, screenshot, "hash-object", "--stdin"))
	for _, b := range []string{BranchFor(tk), "main"} {
		cmd := gittest.Command("rev-list", "--objects", "refs/heads/"+b)
		cmd.Dir = e.git.RemoteDir()
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), blob) {
			t.Errorf("%s holds the capture blob %s", b, blob)
		}
	}
	if !strings.Contains(e.remoteBlob(captures.Branch(tk), gitT(t, e.checkout, "rev-parse", "work")+"/S1.1.png"), "PNG") {
		t.Fatal("the capture branch lost the capture")
	}
}

// gitStdin runs git in dir with stdin.
func gitStdin(t *testing.T, dir string, stdin []byte, args ...string) string {
	t.Helper()
	cmd := gittest.Command(args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(stdin))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// C14: the loop never deletes a capture branch. A mirror push of no
// commit refuses, where git would delete the branch, and the capture
// branch stays on the remote. No source file under internal/ pushes with
// --delete or --mirror, or sends a DELETE to GitHub's git refs.
func TestC14LoopNeverDeletesACaptureBranch(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	tk := e.citingTicket("S1.1")
	commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	mustOK(t, e.capturesPush(tk, map[string][]byte{"S1.1.png": screenshot}))
	branch := captures.Branch(tk)
	tip := e.remoteSHA(branch)
	tok, err := e.svc.Token(context.Background(), e.project, ScopePush)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Mirror(e.project).Push(context.Background(), tok, "", branch, tip); err == nil {
		t.Error("a push of no commit answered no error")
	}
	if got := e.remoteSHA(branch); got != tip {
		t.Fatalf("remote %s = %q, want %s", branch, got, tip)
	}

	err = filepath.WalkDir("..", func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, "githubtest") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			deletes := strings.Contains(line, `"--delete"`) || strings.Contains(line, `"--mirror"`) ||
				strings.Contains(line, "MethodDelete") && strings.Contains(line, "git/refs")
			if deletes {
				t.Errorf("%s deletes a remote ref: %s", path, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// `branch sync` of a ticket that cites states names its capture branch,
// the tip and a fetch command that brings the tip into the checkout. A
// ticket that cites nothing gets no captures part.
func TestBranchSyncNamesTheCaptureBranchOfACitingTicket(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	tk := e.citingTicket("S1.1")
	head := commit(t, e.checkout, "one", map[string]string{"a.txt": "a\n"})
	mustOK(t, e.capturesPush(tk, map[string][]byte{"S1.1.png": screenshot}))

	res := e.runCall("branch sync", tk, machine.StageCodeReviewer, nil, nil)
	mustOK(t, res)
	got, ok := res.Data.(SyncResult)
	if !ok || got.Captures == nil {
		t.Fatalf("branch sync data %+v holds no captures part", res.Data)
	}
	tip := e.remoteSHA(captures.Branch(tk))
	if got.Captures.Branch != captures.Branch(tk) || got.Captures.SHA != tip || len(got.Captures.Fetch) == 0 {
		t.Fatalf("captures part %+v, want %s at %s with a fetch", *got.Captures, captures.Branch(tk), tip)
	}
	fresh := t.TempDir()
	gitT(t, fresh, "init", "-q")
	gitT(t, fresh, got.Captures.Fetch[1:]...)
	if shown := gitT(t, fresh, "show", "origin/"+captures.Branch(tk)+":"+head+"/S1.1.png"); !strings.Contains(shown, "PNG") {
		t.Fatalf("the fetched capture branch holds %q", shown)
	}

	plain := e.citingTicket("")
	res = e.runCall("branch sync", plain, machine.StageCodeReviewer, nil, nil)
	mustOK(t, res)
	if got, _ := res.Data.(SyncResult); got.Captures != nil {
		t.Fatalf("a ticket that cites nothing got a captures part %+v", *got.Captures)
	}
}
