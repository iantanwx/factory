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

// A schedule may name its timezone with a TZ= prefix. Its fields then read
// the wall clock of that zone, as tickertown's nightly QA timer reads
// Asia/Singapore (nightly-qa.md, alpha).
func TestNextDueReadsTheScheduleTimezone(t *testing.T) {
	cases := []struct{ cron, now, want string }{
		// 02:55 in Singapore is 18:55 UTC on the day before.
		{"TZ=Asia/Singapore 55 2-6 * * *", "2026-10-24T12:00:00Z", "2026-10-24T18:55:00Z"},
		{"TZ=Asia/Singapore 55 2-6 * * *", "2026-10-24T18:55:00Z", "2026-10-24T19:55:00Z"},
		// Day fields read the local date: Monday in Singapore starts on
		// Sunday 16:00 UTC.
		{"TZ=Asia/Singapore 0 0 * * 1", "2026-10-24T00:00:00Z", "2026-10-25T16:00:00Z"},
		// West of UTC the local date trails the UTC date in the evening:
		// 02:00 UTC on 07-01 is 22:00 on 06-30 in New York, so that
		// evening's 23:00 is still ahead.
		{"TZ=America/New_York 0 23 * * *", "2026-07-01T02:00:00Z", "2026-07-01T03:00:00Z"},
		// The offset follows daylight saving: 03:00 in New York is 07:00
		// UTC in summer and 08:00 UTC in winter.
		{"TZ=America/New_York 0 3 * * *", "2026-07-01T00:00:00Z", "2026-07-01T07:00:00Z"},
		{"TZ=America/New_York 0 3 * * *", "2026-12-01T00:00:00Z", "2026-12-01T08:00:00Z"},
		// A wall time that spring-forward skips does not fire that day:
		// 02:30 on 2026-03-08 does not exist in New York.
		{"TZ=America/New_York 30 2 * * *", "2026-03-08T00:00:00Z", "2026-03-09T06:30:00Z"},
		// A wall time that fall-back repeats fires once that day.
		{"TZ=America/New_York 30 1 * * *", "2026-11-01T05:30:00Z", "2026-11-02T06:30:00Z"},
		// Its first instant: 02:30 CEST on 2026-10-25 in Berlin, not the
		// 02:30 CET an hour later.
		{"TZ=Europe/Berlin 30 2 * * *", "2026-10-25T00:00:00Z", "2026-10-25T00:30:00Z"},
		// A date the zone skipped never fires: Apia went from 12-29 to
		// 12-31 in 2011, so the next 12-30 noon is in 2012 (+14).
		{"TZ=Pacific/Apia 0 12 30 12 *", "2011-12-29T00:00:00Z", "2012-12-29T22:00:00Z"},
		// UTC names the default.
		{"TZ=UTC 0 0 * * *", "2026-12-31T23:59:59Z", "2027-01-01T00:00:00Z"},
	}
	for _, c := range cases {
		got, err := NextDue(c.cron, at(t, c.now))
		if err != nil {
			t.Fatalf("%s after %s: %v", c.cron, c.now, err)
		}
		if want := at(t, c.want); !got.Equal(want) {
			t.Errorf("%s after %s = %s, want %s", c.cron, c.now, got.UTC().Format(time.RFC3339), c.want)
		}
	}
}

// TZ= names an IANA zone, and only one: a zone this host cannot load, an
// empty name or the host's own Local zone is refused.
func TestParseCronAcceptsOnlyAnIANATimezone(t *testing.T) {
	if _, err := ParseCron("TZ=Asia/Singapore 0 0 * * *"); err != nil {
		t.Fatalf("ParseCron of a named zone = %v", err)
	}
	for _, bad := range []string{"TZ=Mars/Olympus 0 0 * * *", "TZ= 0 0 * * *", "TZ=Asia/Singapore", "TZ=Local 0 0 * * *"} {
		if _, err := ParseCron(bad); err == nil {
			t.Errorf("ParseCron(%q) accepted", bad)
		}
	}
}
