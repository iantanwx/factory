package pipeline_test

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/machine"
)

func recordPath(epic string) string { return "docs/design/" + epic + "/record.md" }

// designed is an epic in decomposing whose design record merged. Its
// record holds S1.1 and S1.2, and US-01 is its one UI story.
func (w *world) designed() string {
	w.t.Helper()
	epic := w.decomposing()
	path := recordPath(epic)
	w.onBase(map[string]string{path: "# Widgets design\n\n**Epic:** " + epic + "\n**PRD:** `" + prdPath + "`\n" +
		"**Prototype:** `loop/" + epic + "-prototype` at `" + strings.Repeat("a", 40) + "`\n\n" +
		"### S1. Widget list\n\n**Stories:** US-01\n\n" +
		"- **S1.1 Empty.** Picture `s1-1.png`, target `/widgets?empty`.\n" +
		"- **S1.2 Full.** Picture `s1-2.png`, target `/widgets`.\n"})
	w.put(epic, machine.Decomposing, map[string]string{"design_merged": "d1", "design_path": path, "ui_stories": "US-01"})
	return epic
}

// screensBody is shapedBody with line in its ## Source section.
func screensBody(line string) string {
	return strings.Replace(shapedBody, "Work entry 1.\n\n", "Work entry 1.\n\n"+line+"\n\n", 1)
}

// uiBatch is a ticket of US-01 with body and a ticket of US-02 with no
// **Screens:** line.
func uiBatch(body string) map[string]any {
	return batch(
		ticket{Key: "list", Title: "List widgets", Body: body, Stories: []string{"US-01"}},
		ticket{Key: "api", Title: "Serve widgets", Stories: []string{"US-02"}},
	)
}

// ticketsByKey reads the tickets of a landed batch by their key.
func (w *world) ticketsByKey(epic string) map[string]machine.Item {
	w.t.Helper()
	out := map[string]machine.Item{}
	for _, tk := range w.items(machine.KindTicket) {
		out[strings.TrimPrefix(tk.Fact(machine.FactCreateKey), epic+"/")] = tk
	}
	return out
}

// C1: a ticket of a UI story with no **Screens:** line refuses the batch,
// and the hint names its key and no ticket of a UI: no story.
func TestC1DecomposeRefusesAUITicketWithNoScreensLine(t *testing.T) {
	w := newWorld(t)
	epic := w.designed()
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(uiBatch(shapedBody))), epic, "ticket_screens")
	hintHolds(t, ref, "ticket list", "**Screens:**")
	if strings.Contains(ref.Hint, "ticket api") {
		t.Fatalf("the hint names the ticket of a UI: no story: %q", ref.Hint)
	}
}

// C2: a none line with a reason passes with no screens fact, a ticket of
// a UI: no story needs no line, and an epic with no design_merged needs no
// line though it holds ui_stories. The same batch with no line on the UI
// ticket refuses first, so the pass is the none line's.
func TestC2DecomposePassesANoneLineAndANonUITicket(t *testing.T) {
	w := newWorld(t)
	epic := w.designed()
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(uiBatch(shapedBody))), epic, "ticket_screens")
	ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(uiBatch(screensBody("**Screens:** none. It adds the API route only.")))))
	tickets := w.ticketsByKey(epic)
	if len(tickets) != 2 {
		t.Fatalf("the batch made %d tickets", len(tickets))
	}
	for key, tk := range tickets {
		if got := tk.Fact("screens"); got != "" {
			t.Fatalf("ticket %s holds screens %q", key, got)
		}
	}

	w2 := newWorld(t)
	plain := w2.decomposing()
	w2.put(plain, machine.Decomposing, map[string]string{"ui_stories": "US-01"})
	ok(t, w2.call(w2.run("run-dec", plain, machine.StageDecomposer), "k-dec", "epic decompose", fileFlag(uiBatch(shapedBody))))
	if got := w2.show(plain).Status; got != machine.Execution {
		t.Fatalf("an epic with no design record stayed %s", got)
	}
}

// C3: a none line with no reason refuses the batch.
func TestC3DecomposeRefusesANoneLineWithNoReason(t *testing.T) {
	w := newWorld(t)
	epic := w.designed()
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	for _, line := range []string{"**Screens:** none", "**Screens:** none."} {
		ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(uiBatch(screensBody(line)))), epic, "ticket_screens")
		hintHolds(t, ref, "ticket list", "no reason")
	}
}

// C4: a state the record lacks, a citation in an epic with no design
// record, an id of another shape and a second line each refuse the batch,
// and the hint names the key and the state.
func TestC4DecomposeRefusesACitedStateTheRecordLacks(t *testing.T) {
	w := newWorld(t)
	epic := w.designed()
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	for line, says := range map[string]string{
		"**Screens:** S1.1, S9.9":                "S9.9",
		"**Screens:** S1":                        "S1",
		"**Screens:** S1.1\n\n**Screens:** S1.2": "**Screens:**",
	} {
		ref := w.refusedAll(w.call(dec, "", "epic decompose", fileFlag(uiBatch(screensBody(line)))), epic, "ticket_screens")
		hintHolds(t, ref, "ticket list", says)
	}

	w2 := newWorld(t)
	plain := w2.decomposing()
	ref := w2.refusedAll(w2.call(w2.run("run-dec", plain, machine.StageDecomposer), "", "epic decompose",
		fileFlag(uiBatch(screensBody("**Screens:** S1.1")))), plain, "ticket_screens")
	hintHolds(t, ref, "ticket list", "S1.1")
}

// C5: a passing ticket holds its cited ids in line order.
func TestC5DecomposeWritesTheCitedIdsInLineOrder(t *testing.T) {
	w := newWorld(t)
	epic := w.designed()
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(uiBatch(screensBody("**Screens:** S1.2, S1.1")))))
	tickets := w.ticketsByKey(epic)
	if got := tickets["list"].Fact("screens"); got != "S1.2,S1.1" {
		t.Fatalf("ticket list holds screens %q", got)
	}
	if got := tickets["api"].Fact("screens"); got != "" {
		t.Fatalf("ticket api holds screens %q", got)
	}
}
