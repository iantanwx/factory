package pipeline_test

import (
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

const skipWords = "Ian: skip the design, the page copies the settings screen."

func skipFlags(request string) map[string]string {
	return map[string]string{"project": testProject, "request": request}
}

// inDesign makes an epic in design whose PRD merged with a UI story.
func (w *world) inDesign(status string) string {
	w.t.Helper()
	epic, res := w.m.Create(w.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: "Widgets",
		Verb: machine.VerbCreatePRD, Actor: machine.Server("test")})
	ok(w.t, res)
	w.put(epic, status, map[string]string{"prd_merged": "m1", "ui_stories": "US-01"})
	return epic
}

// C16: the coordinator's skip of an epic in design, with no design pull
// request, moves it to spec_needed, and design_skipped holds the words.
func TestC16EpicSkipDesignMovesTheEpicAndKeepsTheWords(t *testing.T) {
	w := newWorld(t)
	epic := w.inDesign("design")
	// A closed design pull request does not hold the skip.
	w.api.World.Add(&gt.PR{Head: "loop/" + epic + "-design", Base: "main", State: "closed"})
	out := ok(t, w.call(w.coordinator(), "", "epic skip-design", skipFlags(skipWords), epic))
	if out["status"] != "spec_needed" {
		t.Fatalf("epic skip-design answered %v", out)
	}
	if it := w.show(epic); it.Status != "spec_needed" || it.Fact("design_skipped") != skipWords {
		t.Fatalf("after the skip: status %s, design_skipped %q", it.Status, it.Fact("design_skipped"))
	}
}

// C17: the skip refuses with no words, for a run or a room, from every
// state but design, and while the design branch heads an open pull
// request. Each refusal changes no status and no fact.
func TestC17EpicSkipDesignRefusalsChangeNothing(t *testing.T) {
	w := newWorld(t)
	type refusal struct {
		name, status, caller, request string
		openPR                        bool
		code, rule                    string
	}
	cases := []refusal{
		{name: "no request", status: "design", code: cli.CodeUsage, rule: "no_operator_request"},
		{name: "a blank request", status: "design", request: "  ", code: cli.CodeUsage, rule: "no_operator_request"},
		{name: "a run", status: "design", caller: "run", request: skipWords, code: cli.CodeCapabilityDenied},
		{name: "a room", status: "design", caller: "room", request: skipWords, code: cli.CodeCapabilityDenied},
		{name: "an open design pull request", status: "design", openPR: true, request: skipWords, code: cli.CodeShapeRefused, rule: "design_pr_open"},
	}
	for _, s := range []string{"design_review", "design_signoff", "spec_needed", "prd_signoff"} {
		cases = append(cases, refusal{name: s, status: s, request: skipWords, code: cli.CodeTransitionNotAllowed})
	}
	for _, c := range cases {
		epic := w.inDesign(c.status)
		if c.openPR {
			w.api.World.Add(&gt.PR{Head: "loop/" + epic + "-design", Base: "main", State: "open"})
		}
		var pr rpc.Principal
		switch c.caller {
		case "run":
			pr = w.run("run-x", epic, machine.StageImplementer)
		case "room":
			pr = w.brainstormRoom()
		default:
			pr = w.coordinator()
		}
		flags := skipFlags(c.request)
		if c.request == "" {
			delete(flags, "request")
		}
		before := w.show(epic)
		if r := wantCode(t, w.call(pr, "", "epic skip-design", flags, epic), c.code); c.rule != "" && r.Rule != c.rule {
			t.Errorf("%s: rule %q, want %q", c.name, r.Rule, c.rule)
		}
		after := w.show(epic)
		if after.Status != c.status || after.Seq != before.Seq || after.Fact("design_skipped") != "" || after.Fact("ui_stories") != "US-01" {
			t.Fatalf("%s: the refusal changed the epic: %+v", c.name, after)
		}
	}
}
