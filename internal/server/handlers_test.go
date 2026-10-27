package server

import (
	"fmt"
	"slices"
	"testing"
)

// exemption is one server verb that may have no handler yet, and the epic
// that owns it. The epic's work deletes the exemption with the handler.
type exemption struct {
	verb, epic string
}

// handlerExemptions lists every server verb with no handler, in path order.
var handlerExemptions = []exemption{
	{"stage pause", "factorio-wd0"},
	{"stage resume", "factorio-wd0"},
	{"work block", "factorio-9wh"},
}

// inventoryFaults compares the unhandled server verbs with the exemptions.
// It names each unhandled verb with no exemption, each exempt verb with a
// handler, and each exemption that names no epic.
func inventoryFaults(unhandled []string, exempt []exemption) []string {
	var faults []string
	for _, v := range unhandled {
		if !slices.ContainsFunc(exempt, func(e exemption) bool { return e.verb == v }) {
			faults = append(faults, fmt.Sprintf("%s has no handler and no exemption", v))
		}
	}
	for _, e := range exempt {
		if !slices.Contains(unhandled, e.verb) {
			faults = append(faults, fmt.Sprintf("%s has a handler: delete its exemption", e.verb))
		}
		if e.epic == "" {
			faults = append(faults, fmt.Sprintf("the exemption of %s names no epic", e.verb))
		}
	}
	return faults
}

// C1: each server verb of the server with beads, the scheduler and the
// GitHub service, its merge interface and its pipeline, has a handler, or
// an exemption that names its epic.
func TestEachServerVerbHasAHandlerOrANamedExemption(t *testing.T) {
	w, gh, _ := newDocWorld(t)
	if err := (&Merge{GitHub: gh}).Setup(w.srv); err != nil {
		t.Fatal(err)
	}
	got := w.srv.RPC.Unhandled()
	if faults := inventoryFaults(got, handlerExemptions); len(faults) > 0 {
		t.Fatalf("handler inventory: %q", faults)
	}
	want := make([]string, len(handlerExemptions))
	for i, e := range handlerExemptions {
		want[i] = e.verb
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Unhandled() = %q, want the exemptions %q", got, want)
	}
}

// C1: the inventory fails in each of its three cases, and only in those.
func TestInventoryFaultsNameEachBrokenExemption(t *testing.T) {
	exempt := []exemption{{"run show", "factorio-wd0"}}
	for _, tc := range []struct {
		name      string
		unhandled []string
		exempt    []exemption
		want      []string
	}{
		{"each unhandled verb is exempt", []string{"run show"}, exempt, nil},
		{"a new server verb has no handler and no exemption", []string{"run show", "work teleport"}, exempt,
			[]string{"work teleport has no handler and no exemption"}},
		{"an exempt verb has a handler", nil, exempt,
			[]string{"run show has a handler: delete its exemption"}},
		{"an exemption names no epic", []string{"run show"}, []exemption{{"run show", ""}},
			[]string{"the exemption of run show names no epic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inventoryFaults(tc.unhandled, tc.exempt); !slices.Equal(got, tc.want) {
				t.Fatalf("faults = %q, want %q", got, tc.want)
			}
		})
	}
}
