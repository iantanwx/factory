package escalation

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/machine"
)

func answer(judge, level, choice string) JudgeAnswer {
	return JudgeAnswer{Judge: judge, Level: level, Choice: choice, Reason: judge + " read the rule"}
}

func reading(p0, p1, p2, p3, confidence float64) *Reading {
	return &Reading{Probabilities: map[string]float64{"0": p0, "1": p1, "2": p2, "3": p3}, Score: 2, Confidence: confidence}
}

// The levels and the route of triage-route.ts (alpha): levels 1 to 3 go back
// to the loop, 4 and 5 go to a person, and outside overrides every level.
func TestRouteOfTheAgentsOwnTriage(t *testing.T) {
	for _, tc := range []struct {
		score, choice string
		want          Route
	}{
		{"1", Inside, RouteLoop}, {"measured", Inside, RouteLoop}, {"3", Inside, RouteLoop},
		{"4", Inside, RoutePerson}, {"owner-only", Inside, RoutePerson}, {"2", Outside, RoutePerson},
	} {
		tr, err := ParseTriage(tc.score, tc.choice)
		if err != nil {
			t.Fatal(err)
		}
		if got := tr.Route(); got != tc.want {
			t.Errorf("%s %s routes %s, want %s", tc.score, tc.choice, got, tc.want)
		}
	}
	if _, err := ParseTriage("6", ""); err == nil {
		t.Fatal("level 6 parsed")
	}
	if tr, _ := ParseTriage("derived", ""); tr.Choice != Inside {
		t.Fatalf("the default choice is %q, want the strict inside", tr.Choice)
	}
}

// The carve-outs of convergence.ts (alpha) run before any threshold.
func TestDecideAppliesEveryCarveOutBeforeAThreshold(t *testing.T) {
	th := DefaultThresholds
	agree := Panel{Finding: "f", Judges: []JudgeAnswer{answer("a", "derived", Inside), answer("b", "derived", Inside)},
		Reading: reading(0.3, 0.3, 0.2, 0.2, 0.1)}
	if d := Decide(agree, th); d.Route != RouteLoop {
		t.Fatalf("two judges that agree route to the loop, whatever Jev reads: %+v", d)
	}
	for name, p := range map[string]Panel{
		"level 4":       {Finding: "f", Judges: []JudgeAnswer{answer("a", "preference", Inside), answer("b", "preference", Inside)}, Reading: reading(0, 0, 0, 1, 1)},
		"outside":       {Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Outside), answer("b", "computed", Outside)}, Reading: reading(0, 0, 0, 1, 1)},
		"one judge":     {Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Inside)}, Reading: reading(0, 0, 0, 1, 1)},
		"same judge":    {Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Inside), answer("a", "computed", Inside)}, Reading: reading(0, 0, 0, 1, 1)},
		"no reading":    {Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Inside), answer("b", "measured", Inside)}},
		"low converge":  {Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Inside), answer("b", "measured", Inside)}, Reading: reading(0.5, 0.2, 0.2, 0.1, 0.9)},
		"low confident": {Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Inside), answer("b", "measured", Inside)}, Reading: reading(0, 0.05, 0.5, 0.45, 0.5)},
		"bad sum":       {Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Inside), answer("b", "measured", Inside)}, Reading: reading(0, 0, 0.5, 0.2, 0.9)},
	} {
		if d := Decide(p, th); d.Route != RoutePerson || len(d.Because) == 0 {
			t.Errorf("%s: %+v, want a person with a reason", name, d)
		}
	}
	pass := Panel{Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Inside), answer("b", "measured", Inside)},
		Reading: reading(0, 0.05, 0.5, 0.45, 0.9)}
	if d := Decide(pass, th); d.Route != RouteLoop || d.Convergence < 0.94 {
		t.Fatalf("a panel over both thresholds routes to the loop: %+v", d)
	}
}

func TestThresholdsComeFromTheRubricTable(t *testing.T) {
	rubric := "| Threshold | Value | What it gates |\n| --- | --- | --- |\n| `convergence` | 0.85 | x |\n| `confidence` | 0.80 | y |\n"
	th, err := ThresholdsIn(rubric)
	if err != nil || th.Convergence != 0.85 || th.Confidence != 0.80 {
		t.Fatalf("thresholds = %+v, %v", th, err)
	}
	if _, err := ThresholdsIn("| `convergence` | 0.85 | x |\n"); err == nil {
		t.Fatal("a rubric with no confidence row parsed")
	}
}

func TestPanelFileRoundTrips(t *testing.T) {
	p := Panel{Finding: "f", Judges: []JudgeAnswer{answer("a", "computed", Inside), answer("b", "measured", Inside)}, Reading: reading(0, 0, 0.5, 0.5, 0.9)}
	raw, err := EncodePanels([]Panel{p})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePanels(raw)
	if err != nil || len(got) != 1 || got[0].Judges[1].Level != "measured" || got[0].Reading.Confidence != 0.9 {
		t.Fatalf("parse = %+v, %v", got, err)
	}
	one, err := ParsePanels(`{"finding":"f","judges":[{"judge":"a","level":"9","choice":"inside","reason":"r"}],"reading":null}`)
	if err == nil {
		t.Fatalf("a panel with level 9 parsed: %+v", one)
	}
	if _, err := ParsePanels(`[]`); err == nil {
		t.Fatal("an empty panel file parsed")
	}
}

func TestRefusalNamesTheRoutesBack(t *testing.T) {
	tr, _ := ParseTriage("2", "")
	h := RefusalHint(tr, machine.KindTicket)
	if !strings.Contains(h, "level 2 measured routes to the loop") || !strings.Contains(h, "--choice outside") ||
		!strings.Contains(h, "loop work supersede") {
		t.Fatalf("hint = %q", h)
	}
	n := TriagedNote(tr, "the reason")
	if !strings.Contains(n, `level="measured"`) || !strings.Contains(n, "the reason") {
		t.Fatalf("note = %q", n)
	}
}

// C-bug-3: the hint names only the close verb of the item's kind:
// `work supersede` for a ticket, `work cancel` for an epic, and neither
// for a kind that has no such close.
func TestC3RefusalHintNamesTheCloseOfTheItemsKind(t *testing.T) {
	tr, _ := ParseTriage("2", "")
	for _, tc := range []struct {
		kind      machine.Kind
		want, not []string
	}{
		{machine.KindTicket, []string{"`loop work supersede`"}, []string{"work cancel"}},
		{machine.KindEpic, []string{"`loop work cancel`"}, []string{"work supersede"}},
		{machine.KindBug, nil, []string{"work supersede", "work cancel"}},
	} {
		h := RefusalHint(tr, tc.kind)
		for _, w := range tc.want {
			if !strings.Contains(h, w) {
				t.Errorf("%s hint names no %s: %q", tc.kind, w, h)
			}
		}
		for _, n := range tc.not {
			if strings.Contains(h, n) {
				t.Errorf("%s hint names %s: %q", tc.kind, n, h)
			}
		}
	}
}
