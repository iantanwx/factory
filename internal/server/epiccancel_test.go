package server

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// C-bug-3: a run that holds an epic and raises a finding the rubric routes
// to the loop reads a hint that names `work cancel`, the close an epic
// has, and never `work supersede`, which takes only a ticket.
func TestC3RoutedToLoopHintOfAnEpicNamesWorkCancel(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	if err := w.bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	is, err := w.bx.Create(ctx, beadsx.CreateSpec{Title: "epic", Type: "epic"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.SpecNeeded}); err != nil {
		t.Fatal(err)
	}
	run := w.runToken("run-1", is.ID, machine.StagePlanner)
	env, code := w.as(run, "escalation", "raise", "--score", "2", "--body", "ADR 0013 gave the work to factorio-yzrt")
	hint, _ := env["hint"].(string)
	if code != 3 || env["code"] != cli.CodeRoutedToLoop || !strings.Contains(hint, "`loop work cancel`") ||
		strings.Contains(hint, "work supersede") {
		t.Fatalf("raise = %d %v", code, env)
	}
}
