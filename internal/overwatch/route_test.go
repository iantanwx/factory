package overwatch

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/coord"
)

// homes is the topology of the C15 and C16 fixtures: the loop project
// loop, and alpha and beta, each with a coordinator of its own. gamma has
// none.
var homes = coord.Topology{Loop: "loop", Homes: map[string]bool{"loop": true, "alpha": true, "beta": true}}

// homeSession is the session of each project of homes.
var homeSession = map[string]agenthost.RunID{
	"loop":  {Host: "orca", Terminal: "t-loop", Incarnation: "i1"},
	"alpha": {Host: "orca", Terminal: "t-alpha", Incarnation: "i1"},
	"beta":  {Host: "orca", Terminal: "t-beta", Incarnation: "i1"},
}

func homeSessions(project string) (agenthost.RunID, bool) {
	id, ok := homeSession[project]
	return id, ok
}

// stallsOfAlpha and rulesOfBeta are the findings of the ticket's fixture.
var (
	stallsOfAlpha = Finding{Key: "overwatch:stalls:alpha-x1y@in_progress", Line: "alpha-x1y stalls (overwatch:stalls:alpha-x1y@in_progress)", Project: "alpha"}
	rulesOfBeta   = Finding{Key: "overwatch:rules:beta:L-4", Line: "learning L-4 of beta is not compared (overwatch:rules:beta:L-4)", Project: "beta"}
)

// splitParts are the parts of the ticket's fixture: a stalls finding of
// alpha, a rules finding of beta, a note, and a part whose read failed.
func splitParts() []Part {
	return []Part{
		readingPart("stalls", Reading{Findings: []Finding{stallsOfAlpha}}),
		readingPart("rules", Reading{Findings: []Finding{rulesOfBeta}}),
		readingPart("decisions", Reading{Notes: []string{"beta-jam waits on a release"}}),
		readingPart("ci", Reading{Notes: []string{"UNREAD: beta: read the runs: GitHub is down"}}),
	}
}

// C15: a finding of a project with a coordinator routes to that project.
// A rules finding, a finding with no project, and a finding of a project
// with no coordinator route to the loop project.
func TestC15RouteNamesEachFindingsCoordinator(t *testing.T) {
	for _, c := range []struct {
		part string
		f    Finding
		want string
	}{
		{"stalls", Finding{Project: "alpha"}, "alpha"},
		{"pulls", Finding{Project: "beta"}, "beta"},
		{"rules", Finding{Project: "beta"}, "loop"},
		{"server", Finding{}, "loop"},
		{"stalls", Finding{Project: "gamma"}, "loop"},
	} {
		if got := Route(c.part, c.f, homes); got != c.want {
			t.Errorf("Route(%s, %+v) = %q, want %q", c.part, c.f, got, c.want)
		}
	}
}

// C15: Split gives alpha its stalls finding only, and the loop project the
// rules finding of beta, the notes and the UNREAD line. Beta, with no
// finding of its own, gets no report.
func TestC15SplitGivesEachProjectItsOwnReport(t *testing.T) {
	rs := ReadAll(context.Background(), newFakeWorld(t), splitParts())
	split := Split(rs, homes)
	if got := slices.Sorted(maps.Keys(split)); !slices.Equal(got, []string{"alpha", "loop"}) {
		t.Fatalf("Split's destinations = %q", got)
	}
	want := map[string]map[string]PartReport{
		"alpha": {
			"stalls":    {Name: "stalls", Code: Work, Command: "loop stalls", Lines: []string{stallsOfAlpha.Line}},
			"rules":     {Name: "rules", Code: Quiet, Command: "loop rules", Lines: []string{}},
			"decisions": {Name: "decisions", Code: Quiet, Command: "loop decisions", Lines: []string{}},
			"ci":        {Name: "ci", Code: Quiet, Command: "loop ci", Lines: []string{}},
		},
		"loop": {
			"stalls":    {Name: "stalls", Code: Quiet, Command: "loop stalls", Lines: []string{}},
			"rules":     {Name: "rules", Code: Work, Command: "loop rules", Lines: []string{rulesOfBeta.Line}},
			"decisions": {Name: "decisions", Code: Quiet, Command: "loop decisions", Lines: []string{"beta-jam waits on a release"}},
			"ci":        {Name: "ci", Code: Unread, Command: "loop ci", Lines: []string{"UNREAD: beta: read the runs: GitHub is down"}},
		},
	}
	codes := map[string]Code{"alpha": Work, "loop": Unread}
	for dest, parts := range want {
		r := split[dest]
		if r.Code != codes[dest] || len(r.Parts) != len(parts) {
			t.Fatalf("the report of %s = %+v", dest, r)
		}
		for _, p := range r.Parts {
			if !slices.Equal(p.Lines, parts[p.Name].Lines) || p.Code != parts[p.Name].Code {
				t.Errorf("the %s part of %s = %+v, want %+v", p.Name, dest, p, parts[p.Name])
			}
		}
	}
}

// sessionRing is one ring of the job to one session.
type sessionRing struct {
	id    agenthost.RunID
	ok    bool
	dests []string
	text  string
}

// C15, the ticket's done-when: a sweep with a stalls finding of alpha and a
// rules finding of beta rings alpha's session with stalls, and the loop
// project's session with rules. Beta's session gets no ring.
func TestC15AFindingRingsItsProjectsCoordinator(t *testing.T) {
	var rings []sessionRing
	j := &Job{World: newFakeWorld(t), Parts: splitParts()[:2], Topology: func() coord.Topology { return homes }, Sessions: homeSessions,
		Ring: func(_ context.Context, id agenthost.RunID, ok bool, dests []string, text string) error {
			rings = append(rings, sessionRing{id, ok, dests, text})
			return nil
		}}
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []sessionRing{
		{homeSession["alpha"], true, []string{"alpha"}, "loop overwatch: work in stalls. Run `loop overwatch sweep`, then follow section 1 of your runbook."},
		{homeSession["loop"], true, []string{"loop"}, "loop overwatch: work in rules. Run `loop overwatch sweep`, then follow section 1 of your runbook."},
	}
	if !slices.EqualFunc(rings, want, func(a, b sessionRing) bool {
		return a.id == b.id && a.ok == b.ok && slices.Equal(a.dests, b.dests) && a.text == b.text
	}) {
		t.Fatalf("rings\n got %+v\nwant %+v", rings, want)
	}
	if r, _ := j.Last(); r.Code != Work || len(r.Parts) != 2 || r.Parts[0].Code != Work || r.Parts[1].Code != Work {
		t.Fatalf("the tick's report holds every finding: %+v", r)
	}
}

// C15: one session that serves two destinations gets one ring that names
// the parts of both. A destination with no session rings nobody.
func TestC15OneSessionGetsOneRingForEachProjectItServes(t *testing.T) {
	one := agenthost.RunID{Host: "orca", Terminal: "t-old", Incarnation: "i1"}
	var rings []sessionRing
	j := &Job{World: newFakeWorld(t), Parts: splitParts()[:2], Topology: func() coord.Topology { return homes },
		Sessions: func(project string) (agenthost.RunID, bool) { return one, project != "loop" },
		Ring: func(_ context.Context, id agenthost.RunID, ok bool, dests []string, text string) error {
			rings = append(rings, sessionRing{id, ok, dests, text})
			return nil
		}}
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rings) != 2 || rings[0].id != one || !rings[0].ok || !slices.Equal(rings[0].dests, []string{"alpha"}) ||
		rings[1].ok || !slices.Equal(rings[1].dests, []string{"loop"}) {
		t.Fatalf("rings = %+v", rings)
	}
	j.Sessions = func(string) (agenthost.RunID, bool) { return one, true }
	rings = nil
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rings) != 1 || !slices.Equal(rings[0].dests, []string{"alpha", "loop"}) ||
		rings[0].text != "loop overwatch: work in stalls, rules. Run `loop overwatch sweep`, then follow section 1 of your runbook." {
		t.Fatalf("rings = %+v", rings)
	}
}

// C16: the sweep that alpha's coordinator reads lists alpha's finding and
// no finding of beta. The loop project's coordinator reads beta's rules
// finding, the notes and the failed read. The host reads every line.
func TestC16TheSweepShowsTheCallerItsOwnFindings(t *testing.T) {
	rs := ReadAll(context.Background(), newFakeWorld(t), splitParts())
	lines := func(r Report) []string {
		var out []string
		for _, p := range r.Parts {
			out = append(out, p.Lines...)
		}
		return out
	}
	if got := lines(rs.Served(homes, homeSessions, "alpha").Report()); !slices.Equal(got, []string{stallsOfAlpha.Line}) {
		t.Fatalf("alpha's sweep = %q", got)
	}
	if got := lines(rs.Served(homes, homeSessions, "beta").Report()); len(got) != 0 {
		t.Fatalf("beta's sweep = %q", got)
	}
	loop := []string{"beta-jam waits on a release", "UNREAD: beta: read the runs: GitHub is down", rulesOfBeta.Line}
	if got := lines(rs.Served(homes, homeSessions, "loop").Report()); !slices.Equal(got, loop) {
		t.Fatalf("the loop project's sweep = %q", got)
	}
	all := []string{stallsOfAlpha.Line, "beta-jam waits on a release", "UNREAD: beta: read the runs: GitHub is down", rulesOfBeta.Line}
	if got := lines(rs.Report()); !slices.Equal(got, all) {
		t.Fatalf("the host's sweep = %q", got)
	}
	if r := rs.Report(); !r.At.Equal(newFakeWorld(t).now) || r.Code != Unread {
		t.Fatalf("the host's report = %+v", r)
	}
}
