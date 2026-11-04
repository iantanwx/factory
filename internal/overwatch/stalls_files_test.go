package overwatch

import (
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// factorio-29yx: admission refuses an implementer ticket whose "May change"
// list meets the list of a ticket that a live implementer run holds (rule
// files_in_flight). Such a fire does not count against the waiting ticket.
// factorio-t1 entered todo at 08:50. Implementer run r-y1y1y1y1y1y1 on
// factorio-y started at 08:40, and runs still, or ended at 08:55. The fires
// at 08:53, 08:58 and 09:03 started no run on factorio-t1. The clock reads
// 09:05, or 09:00 for the run that ended: then only the fire at 08:58
// counts.
func TestC10AFireThatFilesInFlightRefusedIsNoStall(t *testing.T) {
	const mine = "## Files\n\n**May change:**\n- `internal/admission/policy.go`\n"
	cases := []struct {
		name   string
		theirs string // the body of factorio-y
		ended  time.Time
		now    time.Time
		work   bool
		note   []string
	}{
		{"a live run whose list meets", "## Files\n\n**May change:**\n- `internal/admission/**`\n", time.Time{}, at(9, 5), false,
			[]string{"files_in_flight", "r-y1y1y1y1y1y1", "factorio-y", "`internal/admission/policy.go`", "`internal/admission/**`",
				"at the fire of " + stamp(at(9, 3))}},
		{"a run that ended between the fires", "## Files\n\n**May change:**\n- `internal/admission/**`\n", at(8, 55), at(9, 0), false,
			[]string{"only one fire", "files_in_flight", "at the fire of " + stamp(at(8, 53))}},
		{"a live run whose list does not meet", "## Files\n\n**May change:**\n- `internal/overwatch/`\n", time.Time{}, at(9, 5), true, nil},
		{"a live run whose ticket has no list", "Do the other thing.", time.Time{}, at(9, 5), true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStallsWorld(t)
			w.now = c.now
			w.add("factorio", machine.Item{ID: "factorio-t1", Kind: machine.KindTicket, Status: machine.Todo, Updated: at(8, 50)}, mine)
			w.entered(t, "factorio-t1", machine.Backlog, machine.Todo, at(8, 50))
			w.add("factorio", machine.Item{ID: "factorio-y", Kind: machine.KindTicket, Status: machine.InProgress, Updated: at(8, 40)}, c.theirs)
			w.run(t, "r-y1y1y1y1y1y1", config.StageImplementer, "factorio-y", at(8, 40), c.ended)
			r := readStallsPart(t, w)
			if _, ok := findingOf(r, "overwatch:stalls:factorio-t1@todo"); ok != c.work {
				t.Fatalf("work = %v, want %v: %+v", ok, c.work, r)
			}
			if !c.work {
				mustContain(t, noteOf(t, r, "factorio-t1"), c.note...)
			}
		})
	}
}
