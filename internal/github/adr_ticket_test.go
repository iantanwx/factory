package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
)

// architectureEpic creates an epic that waits for an ADR.
func (e *env) architectureEpic(title string) string {
	e.t.Helper()
	id, res := e.m.Create(context.Background(), machine.CreateRequest{Project: testProject, Kind: machine.KindEpic,
		Title: title, Verb: machine.VerbCreateArchitecture, Actor: machine.Server("test")})
	mustOK(e.t, res)
	return id
}

// land commits files to the base branch, pushes them, and ticks once.
func (e *env) land(r *Reactor, files map[string]string) {
	e.t.Helper()
	gt.Commit(e.t, e.checkout, "land", files)
	gt.Git(e.t, e.checkout, "push", "-q", e.git.RemoteDir(), "HEAD:main")
	e.tick(r)
}

// adrText is an ADR whose Ticket line reads ticket and whose Context names
// each id in prose.
func adrText(ticket string, prose ...string) string {
	text := "# ADR 0007: Share the skill pack\n\n**Status:** Accepted\n**Date:** 2026-10-24\n**Ticket:** " + ticket +
		"\n\n## Context\n\n"
	for _, id := range prose {
		text += "Epic " + id + " reads the same pack later.\n"
	}
	return text
}

const adr0007 = "docs/adr/0007-shared-skill-pack.md"

// C-bug-1: an ADR joins only the epic its **Ticket:** line names. An
// architecture epic that the ADR names only in prose stays in
// architecture, with no adr fact.
func TestADRMovesOnlyItsTicketEpic(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	x, y := e.architectureEpic("Share the skill pack"), e.architectureEpic("Move the store to Postgres")
	r := e.reactor()
	e.land(r, map[string]string{adr0007: adrText(x, y)})
	if it := e.status(x); it.Status != machine.SpecNeeded || it.Fact(machine.FactADR) != adr0007 {
		t.Fatalf("the Ticket epic %s: %s %v", x, it.Status, it.Facts)
	}
	if it := e.status(y); it.Status != machine.Architecture || it.Fact(machine.FactADR) != "" {
		t.Fatalf("epic %s is named only in prose, and it moved to %s with adr=%q", y, it.Status, it.Fact(machine.FactADR))
	}
}

// C-bug-1: an ADR whose Ticket line reads none moves no epic, even one
// its prose names.
func TestADRWithTicketNoneMovesNoEpic(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	y := e.architectureEpic("Move the store to Postgres")
	r := e.reactor()
	e.land(r, map[string]string{adr0007: adrText("none", y)})
	if it := e.status(y); it.Status != machine.Architecture || it.Fact(machine.FactADR) != "" {
		t.Fatalf("the ADR's Ticket line reads none, and epic %s moved to %s with adr=%q", y, it.Status, it.Fact(machine.FactADR))
	}
}

// C-bug-1, the repair of F-10: an epic that an ADR moved to spec_needed,
// though the ADR's Ticket line names another epic, returns to
// architecture with no adr fact. The Ticket epic stays in spec_needed,
// and a second tick changes nothing.
func TestADRMisjoinedEpicReturnsToArchitecture(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	ctx := context.Background()
	x, y := e.architectureEpic("Share the skill pack"), e.architectureEpic("Move the store to Postgres")
	// The reactor before the fix moved both epics: replay its two writes.
	for _, id := range []string{x, y} {
		mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: id, Verb: machine.VerbADRLanded,
			ExpectedStatus: machine.Architecture, Params: map[string]string{"path": adr0007, "added": e.base}, Actor: machine.Server("test")}))
	}
	r := e.reactor()
	e.land(r, map[string]string{adr0007: adrText(x, y)})
	check := func() {
		t.Helper()
		if it := e.status(x); it.Status != machine.SpecNeeded || it.Fact(machine.FactADR) != adr0007 {
			t.Fatalf("the Ticket epic %s: %s %v", x, it.Status, it.Facts)
		}
		if it := e.status(y); it.Status != machine.Architecture || it.Fact(machine.FactADR) != "" {
			t.Fatalf("epic %s is not on the Ticket line, and it reads %s with adr=%q", y, it.Status, it.Fact(machine.FactADR))
		}
	}
	check()
	e.tick(r)
	check()
}

// The return is the server's alone: no CLI verb performs it.
func TestADRMisjoinedIsServerOnly(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	ctx := context.Background()
	y := e.architectureEpic("Move the store to Postgres")
	mustOK(t, e.m.Apply(ctx, machine.Request{Project: testProject, Item: y, Verb: machine.VerbADRLanded,
		ExpectedStatus: machine.Architecture, Params: map[string]string{"path": adr0007, "added": e.base}, Actor: machine.Server("test")}))
	res := e.m.Apply(ctx, machine.Request{Project: testProject, Item: y, Verb: machine.VerbADRMisjoined, Command: "work edit",
		ExpectedStatus: machine.SpecNeeded, Params: map[string]string{"path": adr0007}, Actor: machine.Server("test")})
	if res.Refusal == nil {
		t.Fatalf("a CLI command performed %s", machine.VerbADRMisjoined)
	}
	if it := e.status(y); it.Status != machine.SpecNeeded {
		t.Fatalf("a refused return moved %s to %s", y, it.Status)
	}
}

// C3: the reactor passes the commit that added the ADR, not the tick's
// head. Commit X adds the ADR, and a later first-parent commit Y edits
// another file. One tick lands the epic with adr_merged equal to X.
func TestC3ADRMergedIsTheAddingCommitNotTheHead(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	ep := e.architectureEpic("Share the skill pack")
	r := e.reactor()
	x := commit(t, e.checkout, "adr", map[string]string{adr0007: adrText(ep)})
	e.land(r, map[string]string{"README.md": "later\n"})
	y := gitT(t, e.checkout, "rev-parse", "HEAD")
	if it := e.status(ep); it.Status != machine.SpecNeeded || it.Fact(machine.FactADRMerged) != x {
		t.Fatalf("epic %s: %s with adr_merged=%q, want %s (the head is %s)", ep, it.Status, it.Fact(machine.FactADRMerged), x, y)
	}
}

// C3: an ADR that a merge commit brought in records that merge commit,
// not the commit on its second parent.
func TestC3ADRMergedIsTheMergeCommitOfItsBranch(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	ep := e.architectureEpic("Share the skill pack")
	r := e.reactor()
	gitT(t, e.checkout, "checkout", "-q", "-b", "adr")
	side := commit(t, e.checkout, "adr", map[string]string{adr0007: adrText(ep)})
	gitT(t, e.checkout, "checkout", "-q", "work")
	commit(t, e.checkout, "other", map[string]string{"x.txt": "x\n"})
	gitT(t, e.checkout, "merge", "-q", "--no-ff", "-m", "Merge pull request #9", "adr")
	merge := gitT(t, e.checkout, "rev-parse", "HEAD")
	e.land(r, map[string]string{"README.md": "later\n"})
	if it := e.status(ep); it.Status != machine.SpecNeeded || it.Fact(machine.FactADRMerged) != merge {
		t.Fatalf("epic %s: %s with adr_merged=%q, want the merge %s (its second parent is %s)", ep, it.Status,
			it.Fact(machine.FactADRMerged), merge, side)
	}
}

// C3: when AddedBy fails, the tick returns an error that names the epic
// and the path, and the epic stays in architecture.
func TestC3FailedAddedByLandsNoEpic(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	ep := e.architectureEpic("Share the skill pack")
	r := e.reactor()
	// A git whose log fails, and whose every other command runs.
	bin := filepath.Join(t.TempDir(), "git")
	writeFile(t, bin, "#!/bin/sh\nif [ \"$1\" = log ]; then echo 'log fails' >&2; exit 1; fi\nexec git \"$@\"\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	e.svc.git.Binary = bin
	commit(t, e.checkout, "adr", map[string]string{adr0007: adrText(ep)})
	gitT(t, e.checkout, "push", "-q", e.git.RemoteDir(), "HEAD:main")
	err := r.Tick(context.Background())
	if err == nil || !strings.Contains(err.Error(), ep) || !strings.Contains(err.Error(), adr0007) {
		t.Fatalf("the tick answered %v, want an error that names %s and %s", err, ep, adr0007)
	}
	if it := e.status(ep); it.Status != machine.Architecture || it.Fact(machine.FactADR) != "" || it.Fact(machine.FactADRMerged) != "" {
		t.Fatalf("epic %s landed with no adding commit: %s %v", ep, it.Status, it.Facts)
	}
}
