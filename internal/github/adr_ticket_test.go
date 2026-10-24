package github

import (
	"context"
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
			ExpectedStatus: machine.Architecture, Params: map[string]string{"path": adr0007}, Actor: machine.Server("test")}))
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
		ExpectedStatus: machine.Architecture, Params: map[string]string{"path": adr0007}, Actor: machine.Server("test")}))
	res := e.m.Apply(ctx, machine.Request{Project: testProject, Item: y, Verb: machine.VerbADRMisjoined, Command: "work edit",
		ExpectedStatus: machine.SpecNeeded, Params: map[string]string{"path": adr0007}, Actor: machine.Server("test")})
	if res.Refusal == nil {
		t.Fatalf("a CLI command performed %s", machine.VerbADRMisjoined)
	}
	if it := e.status(y); it.Status != machine.SpecNeeded {
		t.Fatalf("a refused return moved %s to %s", y, it.Status)
	}
}
