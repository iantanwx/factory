package machine

import (
	"encoding/json"
	"testing"
)

// A reader of work show and work list tells a fixed bug from a bug whose
// fix has not started: each shaped bug answers closed, from BugClosed, and
// the status of the ticket it was shaped into.
func TestShapedBugAnswersClosedAndTicketStatus(t *testing.T) {
	w := newWorld(t)
	shape := func(run string) (string, string) {
		bug := w.create(KindBug, "file", nil)
		p := w.claimAs(bug, StageReproducer, run)
		res := w.call("work shape", p, []string{bug}, map[string]string{"title": "Fix it", "body": shapedBug, "priority": "2"})
		mustOK(t, res)
		return bug, res.Data.(ShapeResult).Ticket
	}
	fixed, done := shape("repro-1")
	w.force(done, Done, nil)
	open, _ := shape("repro-2")

	fields := func(v any) map[string]any {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	want := map[string][2]any{fixed: {true, Done}, open: {false, Todo}}

	for bug, w2 := range want {
		res := w.call("work show", coord(), []string{bug}, nil)
		mustOK(t, res)
		m := fields(res.Data)
		if m["status"] != Shaped || m["closed"] != w2[0] || m["ticket_status"] != w2[1] {
			t.Errorf("work show %s: status %v closed %v ticket_status %v, want shaped %v %v",
				bug, m["status"], m["closed"], m["ticket_status"], w2[0], w2[1])
		}
	}

	res := w.call("work list", coord(), nil, map[string]string{"kind": string(KindBug)})
	mustOK(t, res)
	seen := 0
	for _, it := range res.Data.([]Item) {
		w2, ok := want[it.ID]
		if !ok {
			continue
		}
		seen++
		m := fields(it)
		if m["closed"] != w2[0] || m["ticket_status"] != w2[1] {
			t.Errorf("work list %s: closed %v ticket_status %v, want %v %v", it.ID, m["closed"], m["ticket_status"], w2[0], w2[1])
		}
	}
	if seen != 2 {
		t.Fatalf("work list --kind bug answered %d of the 2 shaped bugs", seen)
	}
}

// Only a shaped bug answers closed and ticket_status: the ticket it became
// and a bug not yet shaped answer neither field, in work show or work list.
func TestOnlyShapedBugAnswersClosedAndTicketStatus(t *testing.T) {
	w := newWorld(t)
	bug := w.create(KindBug, "file", nil)
	p := w.claimAs(bug, StageReproducer, "repro-1")
	res := w.call("work shape", p, []string{bug}, map[string]string{"title": "Fix it", "body": shapedBug, "priority": "2"})
	mustOK(t, res)
	ticket := res.Data.(ShapeResult).Ticket
	reported := w.create(KindBug, "file", nil)
	answers := map[string]bool{bug: true, ticket: false, reported: false}

	check := func(verb string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		_, closed := m["closed"]
		_, status := m["ticket_status"]
		if want := answers[m["item"].(string)]; closed != want || status != want {
			t.Errorf("%s %s (%v): answers closed %v ticket_status %v, want both %v", verb, m["item"], m["status"], closed, status, want)
		}
	}
	for id := range answers {
		res := w.call("work show", coord(), []string{id}, nil)
		mustOK(t, res)
		check("work show", res.Data)
	}

	res = w.call("work list", coord(), nil, nil)
	mustOK(t, res)
	seen := 0
	for _, it := range res.Data.([]Item) {
		if _, ok := answers[it.ID]; ok {
			seen++
			check("work list", it)
		}
	}
	if seen != len(answers) {
		t.Fatalf("work list answered %d of the %d items", seen, len(answers))
	}
}
