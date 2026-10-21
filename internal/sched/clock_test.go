package sched

import (
	"strings"
	"testing"
	"time"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The cases of slothbot's clock.test.ts (alpha), including day of month OR
// day of week.
func TestNextDueMatchesSlothbot(t *testing.T) {
	cases := []struct{ cron, now, want string }{
		{"5,15 8-10/2 2 * 1", "2026-02-09T08:15:00Z", "2026-02-09T10:05:00Z"},
		{"5,15 8-10/2 2 * 1", "2026-04-01T23:59:00Z", "2026-04-02T08:05:00Z"},
		{"5,15 8-10/2 2 * 1", "2026-04-03T08:05:00Z", "2026-04-06T08:05:00Z"},
		{"*/15 * * * *", "2026-04-03T08:30:00Z", "2026-04-03T08:45:00Z"},
		{"*/15 * * * *", "2026-04-03T08:31:00Z", "2026-04-03T08:45:00Z"},
		{"15,5 10,8 * * *", "2026-04-03T07:59:59Z", "2026-04-03T08:05:00Z"},
		{"3,23,43 * * * *", "2026-10-12T09:43:00Z", "2026-10-12T10:03:00Z"},
		{"0 0 * * *", "2026-12-31T23:59:59Z", "2027-01-01T00:00:00Z"},
		{"0 0 29 2 *", "2026-01-01T00:00:00Z", "2028-02-29T00:00:00Z"},
	}
	for _, c := range cases {
		got, err := NextDue(c.cron, at(t, c.now))
		if err != nil {
			t.Fatalf("%s after %s: %v", c.cron, c.now, err)
		}
		if want := at(t, c.want); !got.Equal(want) {
			t.Errorf("%s after %s = %s, want %s", c.cron, c.now, got.Format(time.RFC3339), c.want)
		}
	}
}

func TestNextDueRefusesImpossibleAndMalformed(t *testing.T) {
	if _, err := NextDue("0 0 31 2 *", at(t, "2026-01-01T00:00:00Z")); err == nil || !strings.Contains(err.Error(), "no possible") {
		t.Fatalf("an impossible cron = %v", err)
	}
	for _, bad := range []string{"", "* * * *", "61 * * * *", "*/0 * * * *", "a * * * *", "5-1 * * * *", "* * * * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("ParseCron(%q) accepted", bad)
		}
	}
}

// A deadline the server missed while it was down fires once, and the next
// deadline lies after now.
func TestMissedDeadlineCatchesUpOnce(t *testing.T) {
	c := &cronState{expr: "*/5 * * * *", due: at(t, "2026-10-21T10:00:00Z")}
	now := at(t, "2026-10-21T11:02:00Z") // twelve deadlines passed
	fire, err := c.advance(now)
	if err != nil || !fire {
		t.Fatalf("advance = %v, %v; want one fire", fire, err)
	}
	if want := at(t, "2026-10-21T11:05:00Z"); !c.due.Equal(want) {
		t.Fatalf("next due = %s, want %s", c.due, want)
	}
	if fire, _ := c.advance(now); fire {
		t.Fatal("a second advance at the same time fired again")
	}
}
