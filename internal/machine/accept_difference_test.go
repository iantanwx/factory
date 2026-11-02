package machine

import (
	"encoding/json"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

const (
	acceptWords  = "Ian: the badge sits 4px lower, and that is fine."
	acceptRecord = "docs/design/ep/record.md"
)

// designedEpic makes an epic in status whose design record merged.
func (w *world) designedEpic(status string, facts map[string]string) string {
	w.t.Helper()
	ep := w.create(KindEpic, "create_prd", nil)
	all := map[string]string{"prd_merged": "m1", "ui_stories": uiLabels, "design_path": acceptRecord, "design_merged": "d1"}
	for k, v := range facts {
		all[k] = v
	}
	w.force(ep, status, all)
	return ep
}

func acceptParams(state, words, at string) map[string]string {
	return map[string]string{"state": state, "request": words, "at": at}
}

// C15: an acceptance appends one entry with the state, the words exactly
// and the time to accepted_differences. A second acceptance of the same
// state appends a second entry and keeps the first. The epic stays in
// execution.
func TestC15AcceptDifferenceAppendsTheOperatorsWords(t *testing.T) {
	w := newWorld(t)
	ep := w.designedEpic(Execution, nil)
	w.must(ep, "accept_difference", acceptParams("S1.2", acceptWords, "2026-11-02T12:00:00Z"))
	w.must(ep, "accept_difference", acceptParams("S1.2", "Ian: the shadow is softer, keep it.", "2026-11-02T13:00:00Z"))
	it := w.show(ep)
	if it.Status != Execution {
		t.Fatalf("after two acceptances the epic is %s, want execution", it.Status)
	}
	var got []map[string]string
	if err := json.Unmarshal([]byte(it.Fact("accepted_differences")), &got); err != nil {
		t.Fatalf("accepted_differences %q: %v", it.Fact("accepted_differences"), err)
	}
	want := []map[string]string{
		{"state": "S1.2", "words": acceptWords, "at": "2026-11-02T12:00:00Z"},
		{"state": "S1.2", "words": "Ian: the shadow is softer, keep it.", "at": "2026-11-02T13:00:00Z"},
	}
	if len(got) != len(want) {
		t.Fatalf("accepted_differences holds %d entries, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if len(got[i]) != len(want[i]) || got[i]["state"] != want[i]["state"] || got[i]["words"] != want[i]["words"] || got[i]["at"] != want[i]["at"] {
			t.Errorf("entry %d is %v, want %v", i, got[i], want[i])
		}
	}
}

// C16: the row refuses with no words, with no state, on an epic with no
// merged design record, and from every state but execution. Each refusal
// changes no status and no fact.
func TestC16AcceptDifferenceRefusalsChangeNothing(t *testing.T) {
	w := newWorld(t)
	type refusal struct {
		name, from string
		facts      map[string]string
		params     map[string]string
		code, rule string
	}
	at := "2026-11-02T12:00:00Z"
	cases := []refusal{
		{name: "no words", from: Execution, params: acceptParams("S1.2", "", at), code: cli.CodeUsage, rule: "missing_request"},
		{name: "no state", from: Execution, params: acceptParams("", acceptWords, at), code: cli.CodeUsage, rule: "missing_state"},
		{name: "no design record", from: Execution, facts: map[string]string{"design_merged": "", "design_path": ""},
			params: acceptParams("S1.2", acceptWords, at), code: cli.CodeShapeRefused, rule: "no_design_record"},
	}
	for _, from := range []string{SpecSignoff, Decomposing, "design_signoff", Escalated} {
		cases = append(cases, refusal{name: from, from: from, params: acceptParams("S1.2", acceptWords, at),
			code: cli.CodeTransitionNotAllowed, rule: "no_edge"})
	}
	for _, c := range cases {
		ep := w.designedEpic(c.from, c.facts)
		before := w.show(ep)
		r := wantCode(t, w.apply(ep, "accept_difference", c.params), c.code)
		if r.Rule != c.rule {
			t.Errorf("%s: rule %q, want %q", c.name, r.Rule, c.rule)
		}
		after := w.show(ep)
		if after.Status != c.from || after.Seq != before.Seq || after.Fact("accepted_differences") != "" {
			t.Fatalf("%s: the refusal changed the epic: %+v", c.name, after)
		}
	}
}
