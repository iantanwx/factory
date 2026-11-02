package github

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/captures"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

// roomCapturesPush is `loop branch push` of the checkout's HEAD from the
// review room of item, with --captures of files when files is not nil.
func (e *env) roomCapturesPush(item string, files map[string][]byte) cli.Result {
	e.t.Helper()
	head := gitT(e.t, e.checkout, "rev-parse", "HEAD")
	extra := map[string]string{cli.ExtraHead: head}
	flags := map[string]string{}
	if files != nil {
		dir := e.t.TempDir()
		for name, data := range files {
			writeFile(e.t, dir+"/"+name, string(data))
		}
		sha, err := cli.CaptureCommit(context.Background(), e.checkout, dir, head)
		if err != nil {
			e.t.Fatal(err)
		}
		flags["captures"] = dir
		extra[cli.ExtraCaptures] = sha
	}
	v, _ := cli.Surface().Verb("branch push")
	call := &rpc.Call{Inv: cli.Invocation{Verb: v, Flags: flags, Extra: extra, RequestID: "req-room-push-" + head},
		Principal: rpc.Principal{Kind: cli.PrincipalRoom, RunID: "room-1", Project: testProject, Item: item,
			Stage: "review-room", Checkout: e.checkout}}
	return e.svc.Handlers()["branch push"](context.Background(), call)
}

// C28: a push from the review room of a ticket that cites S1.1 with no
// --captures gets captures_missing naming S1.1, and neither branch moves.
// The same push with S1.1.png moves the capture branch, then the ticket
// branch.
func TestC28ReviewRoomPushCarriesTheCapturesOfACitingTicket(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	tk := e.citingTicket("S1.1")
	updates := e.recordRefUpdates()
	head := commit(t, e.checkout, "the operator's edit", map[string]string{"a.txt": "a\n"})

	res := e.roomCapturesPush(tk, nil)
	wantRule(t, res, cli.CodeShapeRefused, captures.RuleMissing)
	if !strings.Contains(res.Refusal.Hint, "S1.1") {
		t.Fatalf("hint %q names no S1.1", res.Refusal.Hint)
	}
	for _, b := range []string{BranchFor(tk), captures.Branch(tk)} {
		if got := e.remoteSHA(b); got != "" {
			t.Fatalf("remote %s = %q after the refusal; want no branch", b, got)
		}
	}

	mustOK(t, e.roomCapturesPush(tk, map[string][]byte{"S1.1.png": screenshot}))
	if got := e.remoteBlob(captures.Branch(tk), head+"/S1.1.png"); got != string(screenshot) {
		t.Fatalf("%s holds %q under %s/, want the pushed capture", captures.Branch(tk), got, head)
	}
	if got := e.remoteSHA(BranchFor(tk)); got != head {
		t.Fatalf("remote %s = %q, want %s", BranchFor(tk), got, head)
	}
	want := []string{"refs/heads/" + captures.Branch(tk), "refs/heads/" + BranchFor(tk)}
	if got := updates(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the remote took %v, want %v in that order", got, want)
	}
}
