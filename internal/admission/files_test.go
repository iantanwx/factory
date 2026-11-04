package admission

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// filesBody is a ticket body whose "May change" list holds entries.
func filesBody(entries ...string) string {
	var b strings.Builder
	b.WriteString("Do the thing.\n\n## Files\n\n**May change:**\n")
	for _, e := range entries {
		b.WriteString("- `" + e + "`\n")
	}
	return b.String()
}

// factorio-29yx: admission takes no ticket whose "May change" list meets
// the list of a ticket that a live implementer run holds. The second ticket
// waits while the first one's run is live, and is taken once that run
// settles. A ticket with no list, and a ticket whose list meets no list in
// flight, run beside the first.
func TestFilesInFlightRefusesATicketWhoseListMeetsALiveRun(t *testing.T) {
	w := newWorld(t)
	first := w.put(machine.KindTicket, machine.Todo, filesBody("internal/admission/**"))
	second := w.put(machine.KindTicket, machine.Todo, filesBody("docs/x.md", "internal/admission/policy.go"))
	ad := w.mustAdmit(machine.StageImplementer, first)
	_, ref := w.admit(machine.StageImplementer, second)
	wantRefusal(t, ref, cli.CodeCapFull)
	if ref.Rule != "files_in_flight" || ref.Item != second {
		t.Fatalf("refusal = %+v, want rule files_in_flight on %s", ref, second)
	}
	for _, want := range []string{first, ad.RunID, "`internal/admission/policy.go`", "`internal/admission/**`"} {
		if !strings.Contains(ref.Hint, want) {
			t.Fatalf("hint %q does not name %s", ref.Hint, want)
		}
	}
	w.mustAdmit(machine.StageImplementer, w.put(machine.KindTicket, machine.Todo, "Do the thing, with no Files section."))
	w.mustAdmit(machine.StageImplementer, w.put(machine.KindTicket, machine.Todo, filesBody("internal/overwatch/")))
	w.settle(ad, Settlement{Finish: "done"})
	w.mustAdmit(machine.StageImplementer, second)
}

// factorio-29yx: the implementer's work query passes a ticket whose list
// meets a ticket in flight, and offers the next ticket, so a free slot
// takes work that does not overlap.
func TestPeekOffersTheNextTicketThatMeetsNoTicketInFlight(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	overlapping := w.put(machine.KindTicket, machine.Todo, filesBody("pkg/x_test.go"))
	disjoint := w.put(machine.KindTicket, machine.Todo, filesBody("cmd/y/"))
	c, ref := w.a.Peek(ctx, prj, config.StageImplementer)
	if ref != nil || c == nil || c.Item != overlapping {
		t.Fatalf("peek with no ticket in flight = %+v %+v, want %s first", c, ref, overlapping)
	}
	w.mustAdmit(machine.StageImplementer, w.put(machine.KindTicket, machine.Todo, filesBody("pkg/x.go")))
	c, ref = w.a.Peek(ctx, prj, config.StageImplementer)
	if ref != nil || c == nil || c.Item != disjoint {
		t.Fatalf("peek = %+v %+v, want %s past %s, whose pkg/x_test.go meets pkg/x.go in flight", c, ref, disjoint, overlapping)
	}
}
