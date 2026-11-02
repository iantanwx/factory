package pipeline_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

const acceptWords = "Ian: the badge sits 4px lower on the phone, and that is fine."

// recordOf is a merged design record of epic with the states S1.1 and S1.2.
func recordOf(epic string) string {
	return "# Widgets: design record\n\n**Epic:** " + epic + "\n**PRD:** " + prdPath + "\n" +
		"**Prototype:** `loop/" + epic + "-prototype` at `0123456789abcdef0123456789abcdef01234567`\n\n" +
		"## Screens\n\n### S1. Widget list\n\n**Stories:** US-01\n\n" +
		"- **S1.1 Empty.** Picture `s1-1.png`, target `/widgets?empty`.\n" +
		"- **S1.2 Full.** Picture `s1-2.png`, target `/widgets`, pixel.\n"
}

// designedIn makes an epic in status whose design record merged to the base.
func (w *world) designedIn(status string) string {
	w.t.Helper()
	epic, res := w.m.Create(w.ctx, machine.CreateRequest{Project: testProject, Kind: machine.KindEpic, Title: "Widgets",
		Verb: machine.VerbCreatePRD, Actor: machine.Server("test")})
	ok(w.t, res)
	w.onBase(map[string]string{recordPath(epic): recordOf(epic)})
	w.put(epic, status, map[string]string{"prd_merged": "m1", "ui_stories": "US-01",
		"design_path": recordPath(epic), "design_merged": "d1"})
	return epic
}

func acceptFlags(state, request string) map[string]string {
	return map[string]string{"project": testProject, "state": state, "request": request}
}

type acceptance struct {
	State string `json:"state"`
	Words string `json:"words"`
	At    string `json:"at"`
}

func (w *world) acceptances(epic string) []acceptance {
	w.t.Helper()
	raw := w.show(epic).Fact("accepted_differences")
	if raw == "" {
		return nil
	}
	var out []acceptance
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		w.t.Fatalf("accepted_differences %q: %v", raw, err)
	}
	return out
}

// C15: the coordinator's acceptance of S1.2 on an epic in execution
// appends one entry with the state, the words exactly and the time. A
// second acceptance of S1.2 appends a second entry and keeps the first.
// The epic stays in execution.
func TestC15EpicAcceptDifferenceRecordsTheWordsAgainstTheState(t *testing.T) {
	w := newWorld(t)
	epic := w.designedIn("execution")
	before := time.Now().UTC().Add(-time.Second)
	out := ok(t, w.call(w.coordinator(), "", "epic accept-difference", acceptFlags("S1.2", acceptWords), epic))
	if out["status"] != "execution" {
		t.Fatalf("epic accept-difference answered %v", out)
	}
	second := "Ian: the shadow is softer than the picture, keep it."
	ok(t, w.call(w.coordinator(), "", "epic accept-difference", acceptFlags("S1.2", second), epic))
	if st := w.show(epic).Status; st != "execution" {
		t.Fatalf("after two acceptances the epic is %s", st)
	}
	got := w.acceptances(epic)
	if len(got) != 2 || got[0].State != "S1.2" || got[0].Words != acceptWords || got[1].State != "S1.2" || got[1].Words != second {
		t.Fatalf("accepted_differences holds %+v", got)
	}
	for _, a := range got {
		at, err := time.Parse(time.RFC3339, a.At)
		if err != nil || at.Before(before) || at.After(time.Now().Add(time.Second)) {
			t.Errorf("an acceptance holds the time %q, not the time of the write: %v", a.At, err)
		}
	}
}

// C16: the verb refuses an empty request, a state the record lacks, an
// epic with no merged design record, a run or a room, and any state but
// execution. After each refusal the epic's status and facts are as before.
func TestC16EpicAcceptDifferenceRefusalsChangeNothing(t *testing.T) {
	w := newWorld(t)
	type refusal struct {
		name, status, caller, state, request string
		noRecord                             bool
		code, rule                           string
	}
	cases := []refusal{
		{name: "no request", status: "execution", state: "S1.2", code: cli.CodeUsage, rule: "no_operator_request"},
		{name: "a blank request", status: "execution", state: "S1.2", request: "  ", code: cli.CodeUsage, rule: "no_operator_request"},
		{name: "an unknown state", status: "execution", state: "S9.9", request: acceptWords, code: cli.CodeShapeRefused, rule: "state_unknown"},
		{name: "no design record", status: "execution", state: "S1.2", request: acceptWords, noRecord: true,
			code: cli.CodeShapeRefused, rule: "no_design_record"},
		{name: "a run", status: "execution", caller: "run", state: "S1.2", request: acceptWords, code: cli.CodeCapabilityDenied},
		{name: "a room", status: "execution", caller: "room", state: "S1.2", request: acceptWords, code: cli.CodeCapabilityDenied},
		{name: "spec_signoff", status: "spec_signoff", state: "S1.2", request: acceptWords, code: cli.CodeTransitionNotAllowed},
	}
	for _, c := range cases {
		epic := w.designedIn(c.status)
		if c.noRecord {
			w.put(epic, c.status, map[string]string{"design_path": "", "design_merged": ""})
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
		flags := acceptFlags(c.state, c.request)
		if c.request == "" {
			delete(flags, "request")
		}
		before := w.show(epic)
		r := wantCode(t, w.call(pr, "", "epic accept-difference", flags, epic), c.code)
		if c.rule != "" && r.Rule != c.rule {
			t.Errorf("%s: rule %q, want %q", c.name, r.Rule, c.rule)
		}
		if c.rule == "state_unknown" && !strings.Contains(r.Hint, "S9.9") {
			t.Errorf("%s: the hint names no state: %q", c.name, r.Hint)
		}
		after := w.show(epic)
		if after.Status != before.Status || after.Seq != before.Seq || !sameFacts(before.Facts, after.Facts) {
			t.Fatalf("%s: the refusal changed the epic:\nbefore %+v\nafter  %+v", c.name, before, after)
		}
	}
}

func sameFacts(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// C17: an acceptance writes nothing to GitHub, moves no ref of the remote
// or the mirror, and leaves the record at the base byte for byte the same.
func TestC17EpicAcceptDifferenceChangesNoFile(t *testing.T) {
	w := newWorld(t)
	epic := w.designedIn("execution")
	mirror := w.svc.Mirror(w.projects[0]).Dir
	remoteRefs := gt.Git(t, w.git.RemoteDir(), "for-each-ref")
	mirrorRefs := gt.Git(t, mirror, "for-each-ref")
	calls := len(w.api.Calls())
	ok(t, w.call(w.coordinator(), "", "epic accept-difference", acceptFlags("S1.1", acceptWords), epic))
	if len(w.acceptances(epic)) != 1 {
		t.Fatalf("the acceptance recorded %+v", w.acceptances(epic))
	}
	if writes := slices.DeleteFunc(w.api.Calls()[calls:], func(c string) bool { return strings.HasPrefix(c, "GET ") }); len(writes) > 0 {
		t.Errorf("the acceptance wrote to GitHub: %v", writes)
	}
	if got := gt.Git(t, w.git.RemoteDir(), "for-each-ref"); got != remoteRefs {
		t.Errorf("the remote's refs moved:\nbefore %s\nafter  %s", remoteRefs, got)
	}
	if got := gt.Git(t, mirror, "for-each-ref"); got != mirrorRefs {
		t.Errorf("the mirror's refs moved:\nbefore %s\nafter  %s", mirrorRefs, got)
	}
	data, _, err := w.svc.ReadAtBase(w.ctx, testProject, recordPath(epic))
	if err != nil || string(data) != recordOf(epic) {
		t.Errorf("the record at the base is %q (%v), want it unchanged", data, err)
	}
}

// C27: an acceptance with --share records the share as given. A second
// acceptance of the same state keeps the first share, and an acceptance
// with no share records none.
func TestC27EpicAcceptDifferenceRecordsTheShare(t *testing.T) {
	w := newWorld(t)
	epic := w.designedIn("execution")
	for _, share := range []string{"1.20%", "0.80%", ""} {
		flags := acceptFlags("S1.2", acceptWords)
		if share != "" {
			flags["share"] = share
		}
		ok(t, w.call(w.coordinator(), "", "epic accept-difference", flags, epic))
	}
	var got []struct {
		State string  `json:"state"`
		Share *string `json:"share"`
	}
	if err := json.Unmarshal([]byte(w.show(epic).Fact("accepted_differences")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Share == nil || *got[0].Share != "1.20%" || got[1].Share == nil || *got[1].Share != "0.80%" || got[2].Share != nil {
		t.Fatalf("accepted_differences holds %+v, want the shares 1.20%%, 0.80%% and none", got)
	}
}

// C27: the verb refuses --share on a state with no pixel mark with rule
// share_unmarked, and a share that is not a percent from 0% to 100% with
// at most two decimals with rule bad_share. After each refusal the epic's
// facts are as before.
func TestC27EpicAcceptDifferenceRefusesABadShare(t *testing.T) {
	w := newWorld(t)
	cases := []struct{ state, share, rule string }{
		{"S1.1", "1.20%", "share_unmarked"},
		{"S1.2", "1.205%", "bad_share"},
		{"S1.2", "1.2", "bad_share"},
		{"S1.2", "-1%", "bad_share"},
		{"S1.2", "101%", "bad_share"},
	}
	for _, c := range cases {
		epic := w.designedIn("execution")
		ok(t, w.call(w.coordinator(), "", "epic accept-difference", acceptFlags("S1.2", acceptWords), epic))
		flags := acceptFlags(c.state, acceptWords)
		flags["share"] = c.share
		before := w.show(epic)
		r := wantCode(t, w.call(w.coordinator(), "", "epic accept-difference", flags, epic), cli.CodeShapeRefused)
		if r.Rule != c.rule {
			t.Errorf("--share %s on %s: rule %q, want %q", c.share, c.state, r.Rule, c.rule)
		}
		after := w.show(epic)
		if after.Status != before.Status || after.Seq != before.Seq || !sameFacts(before.Facts, after.Facts) {
			t.Fatalf("--share %s on %s: the refusal changed the epic:\nbefore %+v\nafter  %+v", c.share, c.state, before, after)
		}
	}
}
