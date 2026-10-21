package admission

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// During a drain admission refuses every run with DRAINING and peeks
// nothing. The refusal writes no run row.
func TestDrainingRefusesAdmission(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	w.a.SetDraining(true)
	if !w.a.Draining() {
		t.Fatal("Draining = false")
	}
	_, ref := w.admit(machine.StageImplementer, id)
	wantRefusal(t, ref, cli.CodeDraining)
	if c, ref := w.a.Peek(context.Background(), prj, machine.StageImplementer); c != nil || ref != nil {
		t.Fatalf("Peek during a drain = %+v, %+v", c, ref)
	}
	if n := w.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("a refused admission wrote %d run rows", n)
	}
	w.a.SetDraining(false)
	w.mustAdmit(machine.StageImplementer, id)
}
