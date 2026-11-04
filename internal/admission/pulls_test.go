package admission

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// fakePulls is a project's open pull requests.
type fakePulls struct {
	mu    sync.Mutex
	open  []PullHead
	err   error
	calls int
}

func (f *fakePulls) set(open ...PullHead) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.open = open
}

func (f *fakePulls) OpenPulls(context.Context, string) ([]PullHead, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return slices.Clone(f.open), f.err
}

// Ported from pr-dependency.test.ts (alpha): `## Depends on` names a pull
// request as `#n`, as a pull request URL, or as its head branch in a code
// span. Only that section counts.
func TestNamedPullsReadsOnlyDependsOn(t *testing.T) {
	body := "Fix it after #9.\n\n## Depends on\n\n- #12 and #12, then (#13)\n" +
		"- https://github.com/iantanwx/alpha/pull/14\n- `feat/ingest`\n- issue/#15 and abc#16\n\n" +
		"## Files\n\n- #17 `elsewhere/`\n"
	got := NamedPulls(body)
	if want := []int{12, 13, 14}; !slices.Equal(got.Numbers, want) {
		t.Fatalf("numbers = %v, want %v", got.Numbers, want)
	}
	if want := []string{"feat/ingest"}; !slices.Equal(got.Branches, want) {
		t.Fatalf("branches = %v, want %v", got.Branches, want)
	}
	if n := NamedPulls("## Depends ontology\n\n- #3\n"); len(n.Numbers) != 0 {
		t.Fatalf("another heading counted: %v", n.Numbers)
	}
}

// Admission holds a ticket while its `## Depends on` names an open pull
// request, by number or by head branch, and lets it go once the pull
// request merged or closed.
func TestDependsOnAnOpenPullRequestHoldsTheTicket(t *testing.T) {
	w := newWorld(t)
	byNumber := w.put(machine.KindTicket, machine.Todo, declaredBody+"\n## Depends on\n\n- #41\n")
	byBranch := w.put(machine.KindTicket, machine.Todo, declaredBody+"\n## Depends on\n\n- `feat/ingest` in iantanwx/other\n")
	w.pulls.set(PullHead{Number: 41, Branch: "feat/ledger"}, PullHead{Number: 42, Branch: "feat/ingest"})

	_, ref := w.admit(machine.StageImplementer, byNumber)
	wantRefusal(t, ref, cli.CodeDependencyOpen)
	if !strings.Contains(ref.Hint, "pull request #41 (`feat/ledger`), which is still open") {
		t.Fatalf("hint = %q", ref.Hint)
	}
	_, ref = w.admit(machine.StageImplementer, byBranch)
	wantRefusal(t, ref, cli.CodeDependencyOpen)
	if !strings.Contains(ref.Hint, "#42") {
		t.Fatalf("hint = %q", ref.Hint)
	}
	if c, ref := w.a.Peek(context.Background(), prj, machine.StageImplementer); ref != nil || c != nil {
		t.Fatalf("peek offered a held ticket: %+v %+v", c, ref)
	}
	if n := w.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("a held ticket reserved %d runs", n)
	}

	w.pulls.set(PullHead{Number: 42, Branch: "feat/ingest"}) // #41 merged
	w.mustAdmit(machine.StageImplementer, byNumber)
	_, ref = w.admit(machine.StageImplementer, byBranch)
	wantRefusal(t, ref, cli.CodeDependencyOpen)
}

// A pull request named outside `## Depends on`, or a number that names no
// open pull request, holds nothing. A body that names none reads no pull
// request at all.
func TestOnlyAnOpenPullInDependsOnHolds(t *testing.T) {
	w := newWorld(t)
	w.pulls.set(PullHead{Number: 7, Branch: "feat/x"})
	w.mustAdmit(machine.StageImplementer, w.put(machine.KindTicket, machine.Todo, declaredBody+"\nAfter #7 and `feat/x`.\n"))
	if w.pulls.calls != 0 {
		t.Fatalf("a body that names no pull request read the pull requests %d times", w.pulls.calls)
	}
	w.mustAdmit(machine.StageImplementer, w.put(machine.KindTicket, machine.Todo, "Fix the other thing.\n\n## Depends on\n\n- #8\n"))
}

// A failed read of the open pull requests refuses the ticket that needs
// it, as an unknown result is never taken as landed.
func TestAFailedPullReadRefuses(t *testing.T) {
	w := newWorld(t)
	w.pulls.err = errors.New("github: 502")
	_, ref := w.admit(machine.StageImplementer, w.put(machine.KindTicket, machine.Todo, declaredBody+"\n## Depends on\n\n- #8\n"))
	wantRefusal(t, ref, cli.CodeInternal)
	if n := w.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("a failed read reserved %d runs", n)
	}
}
