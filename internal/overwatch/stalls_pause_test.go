package overwatch

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// pause pauses stage in project, or in every project when project is "".
func (w *stallsWorld) pause(t *testing.T, project, stage, reason string) {
	t.Helper()
	if _, err := admission.Pause(context.Background(), w.st, admission.StagePause{Project: project, Stage: stage, Reason: reason, By: "coordinator"}); err != nil {
		t.Fatal(err)
	}
}

func (w *stallsWorld) resume(t *testing.T, project, stage string) {
	t.Helper()
	if _, err := admission.Resume(context.Background(), w.st, project, stage, "coordinator"); err != nil {
		t.Fatal(err)
	}
}

// C25: an item that stalls by C10, at a stage that a pause holds in its
// project or in every project, is no stall. The part prints one note that
// names the pause and its reason, and reads code 0. After the resume, the
// same item stalls again. A pause of another stage, or of the stage in
// another project, holds no item of factorio's implementer. The implementer
// fires at 08:58 and 09:03, and the clock reads 09:05.
func TestC25AnItemAtAPausedStageIsNoStall(t *testing.T) {
	for _, project := range []string{"factorio", ""} {
		t.Run("pause of project "+project, func(t *testing.T) {
			w := newStallsWorld(t)
			w.item("factorio-t1", machine.KindTicket, machine.Todo, at(8, 57))
			w.pause(t, "alpha", config.StageImplementer, "alpha is red")
			w.pause(t, "", config.StageCodeReviewer, "reviews wait")
			key := "overwatch:stalls:factorio-t1@todo"
			if code := Sweep(context.Background(), w, stallsOnly(t)).Code; code != Work {
				t.Fatalf("the sweep's code under pauses of other scopes = %d, want 1", code)
			}

			w.pause(t, project, config.StageImplementer, "the merge queue is red")
			r := readStallsPart(t, w)
			if f, ok := findingOf(r, key); ok {
				t.Fatalf("an item at a paused stage is work: %q", f.Line)
			}
			mustContain(t, noteOf(t, r, "factorio-t1"), "implementer", "paused", "the merge queue is red")
			if code := Sweep(context.Background(), w, stallsOnly(t)).Code; code != Quiet {
				t.Fatalf("the sweep's code while paused = %d, want 0", code)
			}

			w.resume(t, project, config.StageImplementer)
			if _, ok := findingOf(readStallsPart(t, w), key); !ok {
				t.Fatalf("factorio-t1 is not work after the resume")
			}
			if code := Sweep(context.Background(), w, stallsOnly(t)).Code; code != Work {
				t.Fatalf("the sweep's code after the resume = %d, want 1", code)
			}
		})
	}
}
