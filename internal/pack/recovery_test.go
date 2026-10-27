package pack_test

import (
	"strings"
	"testing"
)

// retryLimitBullet returns the bullet of coordinator section 3 that names
// `retry_limit`, as one joined line. The bullet runs from its "- " line to
// the next line that is not indented.
func retryLimitBullet(t *testing.T) string {
	t.Helper()
	var items []string
	for _, l := range strings.Split(coordinatorSection(t, "3"), "\n") {
		switch {
		case strings.HasPrefix(l, "- "):
			items = append(items, l)
		case len(items) > 0 && (l == "" || strings.HasPrefix(l, " ")):
			items[len(items)-1] += "\n" + l
		default:
			items = append(items, "")
		}
	}
	for _, it := range items {
		if strings.Contains(it, "`retry_limit`") {
			return flat(it)
		}
	}
	t.Fatal("coordinator section 3 has no bullet that names `retry_limit`")
	return ""
}

func assertSays(t *testing.T, contract, bullet string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(bullet, want) {
			t.Errorf("%s: the retry_limit bullet does not say %q: %s", contract, want, bullet)
		}
	}
}

// C1 of factorio-1okx: a first limit gets the coordinator's own edit, and
// the runbook no longer waits for the operator's words to edit.
func TestC1FirstRetryLimitGetsTheCoordinatorsOwnEdit(t *testing.T) {
	b := retryLimitBullet(t)
	assertSays(t, "C1", b, "`loop work edit <item>", "your own title and body", "answer the alert with what changed")
	if strings.Contains(read(t, coordinatorRunbook), "operator's text") {
		t.Error("C1: the coordinator runbook still says \"operator's text\"")
	}
}

// C2 of factorio-1okx: the recovery edit writes the record, a `## Recovery`
// section with the date, the alert entry, the stage and what changed.
func TestC2RecoveryEditWritesTheRecord(t *testing.T) {
	assertSays(t, "C2", retryLimitBullet(t),
		"The new body ends with a `## Recovery` section",
		"the date", "the alert entry", "the stage", "what changed")
}

// C3 of factorio-1okx: the coordinator reads the body before any edit, and a
// recovered item goes to the operator as a prepared question.
func TestC3SecondRetryLimitGetsNoUnattendedEdit(t *testing.T) {
	b := retryLimitBullet(t)
	assertSays(t, "C3", b,
		"When the body holds a `## Recovery` section",
		"`loop inbox prepare",
		"only with the operator's answer",
		"Never look for a way to reset the count.")
	show, edit := strings.Index(b, "`loop work show"), strings.Index(b, "`loop work edit")
	if show < 0 || edit < 0 || show > edit {
		t.Errorf("C3: the bullet must run `loop work show` before the first `loop work edit`: %s", b)
	}
}

// C4 of factorio-1okx: the record survives each later edit, the operator's
// edit and a SHAPE_REFUSED retry included.
func TestC4RecoveryRecordSurvivesEachLaterEdit(t *testing.T) {
	assertSays(t, "C4", retryLimitBullet(t),
		"Each later body edit of the item keeps the `## Recovery` section unchanged, an edit with the operator's answer included.",
		"On `SHAPE_REFUSED`, cut the task text, never the section")
}

// C5 of factorio-1okx: a loop fault spends no recovery.
func TestC5LoopFaultSpendsNoRecovery(t *testing.T) {
	assertSays(t, "C5", retryLimitBullet(t),
		"When the cause is a loop fault",
		"`loop fault report`",
		"Edit nothing")
}

// C6 of factorio-1okx: a ticket with no work closes, and spends no recovery.
func TestC6TicketWithNoWorkClosesWithoutRecovery(t *testing.T) {
	assertSays(t, "C6", retryLimitBullet(t),
		"leaves the ticket with no work",
		"`loop work supersede`",
		"writes no `## Recovery` section")
}
