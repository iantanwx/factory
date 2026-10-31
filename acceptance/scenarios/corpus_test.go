//go:build acceptance

package scenarios

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
)

// finding is one labelled finding of the corpus.
type finding struct {
	ID    string `json:"id"`
	Level string `json:"level"`
	Gate  string `json:"gate"`
	Route string `json:"route"`
	Text  string `json:"finding"`
}

type corpusCase struct {
	ID       string    `json:"id"`
	Stage    string    `json:"stage"`
	Route    string    `json:"route"`
	Findings []finding `json:"findings"`
}

func loadCorpus(t *testing.T) []corpusCase {
	t.Helper()
	raw, err := os.ReadFile("../corpus/escalations.json")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Cases []corpusCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c.Cases
}

// panelFor is the panel two judges who agree with the label return: both
// score the labelled level and gate, and Jev reads them as identical.
func panelFor(f finding) escalation.Panel {
	reason := "scored as the corpus labels it"
	return escalation.Panel{
		Finding: f.Text,
		Judges: []escalation.JudgeAnswer{
			{Judge: "judge-a", Level: f.Level, Choice: f.Gate, Reason: reason},
			{Judge: "judge-b", Level: f.Level, Choice: f.Gate, Reason: reason},
		},
		Reading: &escalation.Reading{Probabilities: map[string]float64{"0": 0, "1": 0.02, "2": 0.08, "3": 0.9},
			Score: 2.88, Confidence: 0.95},
	}
}

func escalationID(t *testing.T, r acceptance.Result) string {
	t.Helper()
	rec, _ := r.Data()["escalation"].(map[string]any)
	id, ok := rec["escalation"].(float64)
	if !ok {
		t.Fatalf("the raise named no escalation: %s", r)
	}
	return strconv.Itoa(int(id))
}

// routeAs starts a triage run, which the waiting escalation admits, and
// submits the panels for escalation id. It returns the route.
func routeAs(w *acceptance.World, id string, panels ...escalation.Panel) string {
	w.T.Helper()
	text, err := escalation.EncodePanels(panels)
	if err != nil {
		w.T.Fatal(err)
	}
	triage := w.Dispatch(escalation.TriageStage)
	r := w.Must(w.In(triage.Checkout, "escalation", "route", id, "--file", w.WriteFile("panel.json", text)))
	w.Must(w.In(triage.Checkout, "run", "finish", "--outcome", "done"))
	return r.Str("route")
}

// Every corpus finding routes to its labelled route. A run raises it at
// its labelled level and gate first: the rubric refuses a loop-labelled
// finding with ROUTED_TO_LOOP before any panel. Every finding then
// reaches `escalation route`, with the panel two agreeing judges return.
// A loop finding is raised again at level 4, so that it waits for the
// panel, and the panel sends it back to the loop with the item unmoved.
// A person finding moves the item to escalated with an inbox entry. Last,
// each case routes as one escalation with all of its panels, and goes
// where its label says.
func TestCorpusRoutesEveryFindingToItsLabel(t *testing.T) {
	cases := loadCorpus(t)
	var n, person int
	for _, c := range cases {
		n += len(c.Findings)
		for _, f := range c.Findings {
			if f.Route == "person" {
				person++
			}
		}
	}
	if len(cases) != 10 || n != 20 || person != 6 {
		t.Fatalf("the corpus holds %d cases, %d findings and %d person findings; want 10, 20 and 6", len(cases), n, person)
	}

	// The labels agree with the rubric: each case's route is DecideAll
	// over its findings' panels.
	for _, c := range cases {
		var ps []escalation.Panel
		for _, f := range c.Findings {
			ps = append(ps, panelFor(f))
			tr, err := escalation.ParseTriage(f.Level, f.Gate)
			if err != nil || string(tr.Route()) != f.Route {
				t.Errorf("%s: the rubric routes %s/%s to %s, and the label says %s (%v)", f.ID, f.Level, f.Gate, tr.Route(), f.Route, err)
			}
		}
		if got := escalation.DecideAll(ps, escalation.DefaultThresholds).Route; string(got) != c.Route {
			t.Errorf("%s: DecideAll routes to %s, and the label says %s", c.ID, got, c.Route)
		}
	}
	if t.Failed() {
		return
	}

	w := newWorld(t, acceptance.Options{})
	for _, c := range cases {
		item := createTicket(w, "Case "+c.ID, "The work behind escalation case "+c.ID+".")
		run := w.Dispatch(machine.StageImplementer)
		if run.Item != item {
			t.Fatalf("%s: the implementer took %s, want %s", c.ID, run.Item, item)
		}
		// resume returns an escalated item to todo with the operator's
		// answer, and starts the next run on it.
		resume := func() {
			w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "blocked"))
			w.Must(w.Coordinator("inbox", "answer", "escalation/"+acceptance.Project+"/"+item, "--body", "Carry on."))
			wantStatus(w, item, machine.Todo)
			run = w.Dispatch(machine.StageImplementer)
			if run.Item != item {
				t.Fatalf("%s: the implementer took %s, want %s", c.ID, run.Item, item)
			}
		}
		for _, f := range c.Findings {
			r := w.In(run.Checkout, "escalation", "raise", "--score", f.Level, "--choice", f.Gate, "--body", f.Text)
			if f.Route == "loop" {
				if r.Code != 3 || r.RefusalCode() != cli.CodeRoutedToLoop {
					t.Fatalf("%s: the raise at its label = %s; want %s", f.ID, r, cli.CodeRoutedToLoop)
				}
				r = w.Must(w.In(run.Checkout, "escalation", "raise", "--score", "preference", "--body", f.Text))
			} else {
				w.Must(r)
			}
			if got := routeAs(w, escalationID(t, r), panelFor(f)); got != f.Route {
				t.Fatalf("%s: routed to %s, labelled %s", f.ID, got, f.Route)
			}
			if f.Route == "loop" {
				wantStatus(w, item, machine.InProgress)
				continue
			}
			wantStatus(w, item, machine.Escalated)
			if w.Entry("escalation/"+acceptance.Project+"/"+item) == nil {
				t.Fatalf("%s: a person route put no inbox entry", f.ID)
			}
			resume()
		}

		// The case as one escalation with every panel.
		var ps []escalation.Panel
		for _, f := range c.Findings {
			ps = append(ps, panelFor(f))
		}
		r := w.Must(w.In(run.Checkout, "escalation", "raise", "--score", "preference", "--body", "Case "+c.ID+": every finding."))
		if got := routeAs(w, escalationID(t, r), ps...); got != c.Route {
			t.Fatalf("%s: the case routed to %s, labelled %s", c.ID, got, c.Route)
		}
		if c.Route == "person" {
			wantStatus(w, item, machine.Escalated)
			w.Must(w.In(run.Checkout, "run", "finish", "--outcome", "blocked"))
			w.Must(w.Coordinator("work", "cancel", item, "--reason", "The person drops case "+c.ID+".", "--project", acceptance.Project))
			continue
		}
		// A loop case keeps its run and its lease, as a run whose finding
		// came back does: it works on.
		wantStatus(w, item, machine.InProgress)
	}
}
