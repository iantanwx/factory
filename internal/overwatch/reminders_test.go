package overwatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
)

// remindersWorld is the stalls world with one PRD sign-off of factorio,
// factorio-jiv, that entered the inbox age ago, and the project's
// health.operator_reminder at limit. A zero limit names none.
func remindersWorld(t *testing.T, limit, age time.Duration) *stallsWorld {
	t.Helper()
	w := newStallsWorld(t)
	w.configs["factorio"].Health = config.HealthConfig{OperatorReminder: limit}
	w.item("factorio-jiv", machine.KindEpic, machine.PRDSignoff, w.now.Add(-age))
	w.entered(t, "factorio-jiv", "prd_review", machine.PRDSignoff, w.now.Add(-age))
	return w
}

// sweepReminders sweeps the reminders part alone.
func sweepReminders(t *testing.T, w World) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{remindersPart()})
	if len(r.Parts) != 1 || r.Parts[0].Name != "reminders" {
		t.Fatalf("the sweep of the reminders part = %+v", r.Parts)
	}
	return r.Parts[0]
}

// C-bug-1: a project that names no health.operator_reminder gets no work
// from the part, however long an entry waits. The part only reports.
func TestCBug1NoOperatorReminderHoldsNoWork(t *testing.T) {
	w := remindersWorld(t, 0, 400*time.Hour)
	p := sweepReminders(t, w)
	if p.Code != Quiet {
		t.Fatalf("a project with no health.operator_reminder: code %d, lines %q, want code 0", p.Code, p.Lines)
	}
	want := "factorio: 1 entries await a person, and the project config names no health.operator_reminder, so none is work"
	if len(p.Lines) != 1 || p.Lines[0] != want {
		t.Fatalf("lines = %q, want [%q]", p.Lines, want)
	}
}

// C-bug-1: the finding rings the coordinator once per limit while the
// entry waits, and not on each 10-minute tick. The coordinator files each
// ring's key in a prepared question about the sign-off, as its runbook
// says, and that question waits on the operator too. A sign-off that
// passed a 12h limit at 13h, swept every 10 minutes until it is 49h old,
// rings at 13h, 24h, 36h and 48h, and at no other tick.
func TestCBug1AReminderRingsOncePerLimit(t *testing.T) {
	w := remindersWorld(t, 12*time.Hour, 13*time.Hour)
	entered := w.now.Add(-13 * time.Hour)
	d := &doorbell{}
	j := &Job{World: w, Ring: d.ring, Parts: []Part{remindersPart()}}
	var rangAt []string
	for ; w.now.Sub(entered) <= 49*time.Hour; w.now = w.now.Add(10 * time.Minute) {
		rings := len(d.texts)
		if err := j.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		if len(d.texts) == rings {
			continue
		}
		rangAt = append(rangAt, short(w.now.Sub(entered)))
		last, _ := j.Last()
		var keys []string
		for _, l := range last.Parts[0].Lines {
			if !strings.Contains(l, "(filed in ") {
				keys = append(keys, Tokens(l)...)
			}
		}
		filed := false
		for _, k := range keys {
			if strings.HasPrefix(k, "overwatch:reminders:") {
				w.exec(t, `INSERT INTO questions (entry, body, run_id, created_at) VALUES ('signoff/factorio/factorio-jiv', ?, '', ?)`,
					"The sign-off waits on you. "+k, w.now.UnixMilli())
				filed = true
			}
		}
		if !filed {
			t.Fatalf("a ring at %s whose report names no reminders key: %q", short(w.now.Sub(entered)), last.Parts[0].Lines)
		}
	}
	if got := strings.Join(rangAt, " "); got != "13h 24h 36h 48h" {
		t.Fatalf("the reminder rang at ages %q, want once per 12h limit: 13h 24h 36h 48h", got)
	}
}

// C-bug-1: a prepared question names no project, so it takes the limit of
// the project of the entry it is about.
func TestCBug1AQuestionTakesTheLimitOfItsEntrysProject(t *testing.T) {
	w := remindersWorld(t, 12*time.Hour, time.Hour)
	w.exec(t, `INSERT INTO questions (entry, body, run_id, created_at) VALUES ('signoff/factorio/factorio-jiv', 'Sign it off?', '', ?)`,
		w.now.Add(-20*time.Hour).UnixMilli())
	p := sweepReminders(t, w)
	if p.Code != Work || len(p.Lines) == 0 || !strings.HasPrefix(p.Lines[0], "question/1 has waited on the operator for 20h") {
		t.Fatalf("a question 20h old about a factorio entry: code %d, lines %q, want work that names question/1", p.Code, p.Lines)
	}
}
