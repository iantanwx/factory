package sched

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
)

// C6: two failed runs at the same inputs write one attempts signal, scoped
// to the stage, with each failed run as a case. One failed run writes none.
func TestC6SecondFailedAttemptWritesASignal(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	var want []retro.CaseRef
	for i := range 2 {
		st := w.dispatch(machine.StageImplementer)
		want = append(want, retro.CaseRef("run:"+st.RunID))
		if _, ref := w.s.Finish(ctx, st.RunID, "failed"); ref != nil {
			t.Fatal(ref)
		}
		got := w.signals(id)
		if i == 0 && len(got) != 0 {
			t.Fatalf("one failed run wrote %+v", got)
		}
	}
	got := w.signals(id)
	if len(got) != 1 || got[0].Kind != retro.SignalAttempts || got[0].Scope != machine.StageImplementer || !reflect.DeepEqual(got[0].Cases, want) {
		t.Fatalf("signals = %+v, want one attempts signal scoped %s with cases %v", got, machine.StageImplementer, want)
	}
}

func (w *world) signals(item string) []retro.Signal {
	w.t.Helper()
	on, err := retro.OnItem(context.Background(), w.st, prj, item)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []retro.Signal
	for _, raw := range on.Signals {
		var s retro.Signal
		if err := json.Unmarshal(raw, &s); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}
