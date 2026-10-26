package overwatch

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-1: when every ticket is done and a sign-off has waited on the
// operator longer than health.operator_reminder, the sweep holds work that
// names the entry and its age. A sign-off younger than the limit holds none.
func TestCBug1ASignoffOlderThanTheReminderLimitIsWork(t *testing.T) {
	w := newStallsWorld(t)
	cfg, err := config.ParseProjectConfig([]byte("merge:\n  mechanism: direct\nhealth:\n  operator_reminder: 12h\n"))
	if err != nil {
		t.Fatalf("the project config cannot name health.operator_reminder: %v", err)
	}
	cfg.Stages = w.configs["factorio"].Stages
	w.configs["factorio"] = cfg

	w.item("factorio-t1", machine.KindTicket, machine.Done, w.now.Add(-48*time.Hour))
	w.item("factorio-jiv", machine.KindEpic, machine.PRDSignoff, w.now.Add(-41*time.Hour))
	w.entered(t, "factorio-jiv", "prd_review", machine.PRDSignoff, w.now.Add(-41*time.Hour))
	w.item("factorio-wd0", machine.KindEpic, machine.PRDSignoff, w.now.Add(-5*time.Hour))
	w.entered(t, "factorio-wd0", "prd_review", machine.PRDSignoff, w.now.Add(-5*time.Hour))

	r := Sweep(context.Background(), w, Parts())
	var old, young []string
	for _, p := range r.Parts {
		for _, l := range p.Lines {
			if strings.Contains(l, "signoff/factorio/factorio-jiv") && p.Code == Work && !strings.Contains(l, "filed in") {
				old = append(old, p.Name+": "+l)
			}
			if strings.Contains(l, "signoff/factorio/factorio-wd0") && p.Code == Work && !strings.Contains(l, "filed in") {
				young = append(young, p.Name+": "+l)
			}
		}
	}
	if len(old) == 0 {
		var names []string
		for _, p := range r.Parts {
			names = append(names, p.Name)
		}
		t.Fatalf("no part holds work that names signoff/factorio/factorio-jiv, which waited 41h past a 12h limit; parts: %v", names)
	}
	if !slices.ContainsFunc(old, func(l string) bool { return strings.Contains(l, "41h") }) {
		t.Fatalf("the work line does not give the entry's age of 41h: %v", old)
	}
	if len(young) != 0 {
		t.Fatalf("a sign-off 5h old, under the 12h limit, holds work: %v", young)
	}
}
