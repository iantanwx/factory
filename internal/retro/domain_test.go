package retro

import (
	"fmt"
	"strings"
	"testing"
)

// C27: the change size is n/a exactly for a loop fault and a project ticket.
func TestParseAnswerHoldsQ3ToItsTargets(t *testing.T) {
	ok := map[string][]string{
		"loop_fault":      {"n/a"},
		"project_ticket":  {"n/a"},
		"pack_pr":         {"edit", "extend", "add", "restructure"},
		"project_policy":  {"edit", "extend", "add", "restructure"},
		"userspace_skill": {"edit", "extend", "add", "restructure"},
	}
	for target, changes := range ok {
		for _, change := range []string{"n/a", "edit", "extend", "add", "restructure"} {
			raw := fmt.Sprintf(`{"judge":"fable-5","target":%q,"evidence":"two_or_more","change":%q,"reason":"r"}`, target, change)
			a, err := ParseAnswer([]byte(raw))
			want := false
			for _, c := range changes {
				want = want || c == change
			}
			if want && (err != nil || string(a.Target) != target || string(a.Change) != change) {
				t.Errorf("%s %s: ParseAnswer = %+v, %v, want the answer", target, change, a, err)
			}
			if !want && err == nil {
				t.Errorf("%s %s: ParseAnswer accepted it", target, change)
			}
		}
	}
}

func TestParseAnswerRefusesAnUnknownValue(t *testing.T) {
	for _, raw := range []string{
		`{"judge":"fable-5","target":"pack","evidence":"two_or_more","change":"edit"}`,
		`{"judge":"fable-5","target":"pack_pr","evidence":"two","change":"edit"}`,
		`{"judge":"fable-5","target":"pack_pr","evidence":"two_or_more","change":"rewrite"}`,
		`{"judge":"","target":"pack_pr","evidence":"two_or_more","change":"edit"}`,
		`{"judge":"fable-5"`,
	} {
		if a, err := ParseAnswer([]byte(raw)); err == nil {
			t.Errorf("ParseAnswer(%s) = %+v, want an error", raw, a)
		}
	}
}

func TestParsePanelsReadsThePanelFile(t *testing.T) {
	raw := `{"panels": [{"learning": {"statement": "s", "cases": ["run:r-1", "round:factorio-x1y:2"], "extends": "L-4"},
	  "answers": [{"judge": "fable-5", "target": "project_policy", "evidence": "two_or_more", "change": "edit", "reason": "a"},
	              {"judge": "opus-5", "target": "loop_fault", "evidence": "one", "change": "n/a", "reason": "b"}],
	  "reading": {"probabilities": {"0": 0.01, "1": 0.04, "2": 0.35, "3": 0.6}, "score": 2.5, "confidence": 0.9}}]}`
	ps, err := ParsePanels([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("got %d panels, want 1", len(ps))
	}
	p := ps[0]
	if p.Learning.Statement != "s" || p.Learning.Extends != "L-4" || len(p.Learning.Cases) != 2 || p.Learning.Cases[1] != "round:factorio-x1y:2" {
		t.Errorf("learning = %+v", p.Learning)
	}
	want := []Answer{
		{Judge: "fable-5", Target: ProjectPolicy, Evidence: TwoOrMore, Change: Edit, Reason: "a"},
		{Judge: "opus-5", Target: LoopFault, Evidence: One, Change: NA, Reason: "b"},
	}
	if len(p.Answers) != 2 || p.Answers[0] != want[0] || p.Answers[1] != want[1] {
		t.Errorf("answers = %+v, want %+v", p.Answers, want)
	}
	if p.Reading == nil || p.Reading.Confidence != 0.9 || p.Reading.Probabilities["3"] != 0.6 {
		t.Errorf("reading = %+v", p.Reading)
	}

	empty, err := ParsePanels([]byte(`{"panels": []}`))
	if err != nil || len(empty) != 0 {
		t.Errorf("an empty panel file = %+v, %v, want no panel and no error", empty, err)
	}
}

func TestParsePanelsRefusesABadLearningOrAnswer(t *testing.T) {
	good := `{"judge": "fable-5", "target": "loop_fault", "evidence": "two_or_more", "change": "n/a"}`
	for name, tc := range map[string]struct{ learning, answer, want string }{
		"no case":      {`{"statement": "s", "cases": []}`, good, "no case"},
		"unknown case": {`{"statement": "s", "cases": ["pr:12"]}`, good, "pr:12"},
		"no statement": {`{"statement": " ", "cases": ["run:r-1"]}`, good, "statement"},
		"bad answer": {`{"statement": "s", "cases": ["run:r-1"]}`,
			`{"judge": "fable-5", "target": "loop_fault", "evidence": "two_or_more", "change": "edit"}`, "n/a"},
	} {
		raw := fmt.Sprintf(`{"panels": [{"learning": %s, "answers": [%s]}]}`, tc.learning, tc.answer)
		_, err := ParsePanels([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: ParsePanels error = %v, want one that names %q", name, err, tc.want)
		}
	}
}

func TestLearningStatusDerivesItsRoute(t *testing.T) {
	for _, tc := range []struct {
		s    LearningStatus
		want Route
	}{
		{StatusHeld, Hold{}},
		{StatusCounted, Hold{}},
		{StatusLanded, Land{Target: PackPR}},
		{StatusEscalated, Escalate{Reason: ReasonSize}},
		{StatusAnswered, Escalate{Reason: ReasonSize}},
	} {
		if got := tc.s.Route(PackPR, ReasonSize); got != tc.want {
			t.Errorf("%s.Route = %#v, want %#v", tc.s, got, tc.want)
		}
	}
}
