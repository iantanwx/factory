package pipeline_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

func tickets(n int) map[string]any {
	ts := make([]ticket, n)
	for i := range ts {
		ts[i] = ticket{Key: fmt.Sprintf("t%d", i), Title: fmt.Sprintf("Slice %d", i), Stories: []string{"US-01"}}
	}
	return batch(ts...)
}

// Parity 1.9: a batch over 25 tickets is a runaway, not a large epic. The
// server refuses it before the first write, and the hint points at a
// split, not at merging slices. A batch of 25 lands.
func TestDecomposeRefusesARunawayBatch(t *testing.T) {
	w := newWorld(t)
	epic := w.decomposing()
	dec := w.run("run-dec", epic, machine.StageDecomposer)

	ref := wantCode(t, w.call(dec, "", "epic decompose", fileFlag(tickets(26))), cli.CodeUsage)
	if ref.Rule != "runaway_batch" {
		t.Fatalf("a batch of 26: rule %s", ref.Rule)
	}
	for _, want := range []string{"26 tickets", "25", "split", "never merge slices"} {
		if !strings.Contains(ref.Hint, want) {
			t.Fatalf("the hint %q does not say %q", ref.Hint, want)
		}
	}
	if n := len(w.items(machine.KindTicket)); n != 0 {
		t.Fatalf("a refused batch created %d tickets", n)
	}
	if got := w.show(epic).Status; got != machine.Decomposing {
		t.Fatalf("a refused batch moved the epic to %s", got)
	}

	ok(t, w.call(dec, "k-dec", "epic decompose", fileFlag(tickets(25))))
	if n := len(w.items(machine.KindTicket)); n != 25 {
		t.Fatalf("a batch of 25 made %d tickets", n)
	}
}
