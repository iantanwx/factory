// Package sched is the server's only scheduler (R24). One loop ticks
// every stage and native job of every project on its cron, dispatches stage
// runs through admission and the agent host, watches each live run, settles
// it, reclaims its lease to the stage-correct state, cleans its checkout up
// and drains on command.
package sched

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronLimits are the bounds of the minute, hour, day-of-month, month and
// day-of-week fields.
var cronLimits = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}

// Cron is one parsed five-field numeric cron. Ported from slothbot's
// scheduling/clock.ts (alpha): lists, ranges, steps and wildcards. Its
// fields read the wall clock of loc, UTC unless a TZ= prefix names a zone.
type Cron struct {
	fields [5]map[int]bool
	loc    *time.Location
	// restricted marks a day-of-month or day-of-week field that does not
	// start with "*". Cron reads two restricted day fields as either one.
	domRestricted, dowRestricted bool
}

// ParseCron parses a five-field cron. A step on a single value, such as
// 5/10, runs from that value to the field's maximum. An optional first
// word TZ=<IANA zone>, such as TZ=Asia/Singapore, sets the zone whose wall
// clock the fields read, as tickertown's timers do (nightly-qa.md, alpha).
func ParseCron(expr string) (*Cron, error) {
	parts := strings.Fields(expr)
	c := &Cron{loc: time.UTC}
	if len(parts) > 0 {
		if name, ok := strings.CutPrefix(parts[0], "TZ="); ok {
			// LoadLocation reads "" and "Local" as zones of this host.
			if name == "" || name == "Local" {
				return nil, fmt.Errorf("sched: cron %q must name an IANA timezone", expr)
			}
			loc, err := time.LoadLocation(name)
			if err != nil {
				return nil, fmt.Errorf("sched: cron %q: %w", expr, err)
			}
			c.loc, parts = loc, parts[1:]
		}
	}
	if len(parts) != len(cronLimits) {
		return nil, fmt.Errorf("sched: cron %q must have five fields", expr)
	}
	for i, part := range parts {
		set, err := cronField(part, cronLimits[i][0], cronLimits[i][1])
		if err != nil {
			return nil, fmt.Errorf("sched: cron %q field %d: %w", expr, i+1, err)
		}
		c.fields[i] = set
	}
	c.domRestricted = !strings.HasPrefix(parts[2], "*")
	c.dowRestricted = !strings.HasPrefix(parts[4], "*")
	return c, nil
}

func cronField(field string, lo, hi int) (map[int]bool, error) {
	set := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		rng, stepText, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepText)
			if err != nil || n < 1 || n > hi-lo+1 {
				return nil, fmt.Errorf("bad step %q", part)
			}
			step = n
		}
		start, end := lo, hi
		if rng != "*" {
			a, b, isRange := strings.Cut(rng, "-")
			x, err := strconv.Atoi(a)
			if err != nil {
				return nil, fmt.Errorf("bad value %q", part)
			}
			start, end = x, x
			if isRange {
				if end, err = strconv.Atoi(b); err != nil {
					return nil, fmt.Errorf("bad range %q", part)
				}
			} else if hasStep {
				end = hi
			}
		}
		if start < lo || end > hi || end < start {
			return nil, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for n := start; n <= end; n += step {
			set[n] = true
		}
	}
	return set, nil
}

func (c *Cron) dayMatches(d time.Time) bool {
	dom, dow := c.fields[2][d.Day()], c.fields[4][int(d.Weekday())]
	if c.domRestricted && c.dowRestricted {
		return dom || dow
	}
	return dom && dow
}

// errNoDate is a cron that no date holds, such as 0 0 31 2 *.
var errNoDate = errors.New("sched: cron has no possible date")

// Next returns the first deadline strictly after now. It searches dates,
// then hours and minutes, rather than minutes. A wall time that a daylight
// saving change skips does not fire that day, and one that it repeats fires
// once, at its first instant.
func (c *Cron) Next(now time.Time) (time.Time, error) {
	local := now.In(c.loc)
	// day holds the local calendar date only; UTC keeps its arithmetic
	// clear of daylight saving.
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	// The Gregorian calendar repeats after 400 years, leap days and weekdays
	// included.
	end := day.AddDate(400, 0, 0)
	for ; day.Before(end); day = day.AddDate(0, 0, 1) {
		if !c.fields[3][int(day.Month())] || !c.dayMatches(day) {
			continue
		}
		for h := 0; h <= 23; h++ {
			if !c.fields[1][h] {
				continue
			}
			for m := 0; m <= 59; m++ {
				if !c.fields[0][m] {
					continue
				}
				if t, ok := c.instant(day, h, m); ok && t.After(now) {
					return t.UTC(), nil
				}
			}
		}
	}
	return time.Time{}, errNoDate
}

// instant returns the first instant whose wall clock in loc reads the date
// of day at h:m, and false when no instant does. time.Date picks either
// instant of a repeated wall time, so this tries the offsets of both sides.
func (c *Cron) instant(day time.Time, h, m int) (time.Time, bool) {
	wall := day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	guess := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, c.loc)
	var first time.Time
	for _, side := range []time.Time{guess.Add(-24 * time.Hour), guess, guess.Add(24 * time.Hour)} {
		_, offset := side.Zone()
		t := wall.Add(-time.Duration(offset) * time.Second).In(c.loc)
		y, mo, d := t.Date()
		if y != day.Year() || mo != day.Month() || d != day.Day() || t.Hour() != h || t.Minute() != m {
			continue
		}
		if first.IsZero() || t.Before(first) {
			first = t
		}
	}
	return first, !first.IsZero()
}

// NextDue is the first deadline of expr strictly after now.
func NextDue(expr string, now time.Time) (time.Time, error) {
	c, err := ParseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	return c.Next(now)
}

// cronState is one stage's deadline for one project.
type cronState struct {
	expr string
	due  time.Time
}

// advance reports whether the deadline passed, and moves it past now. A
// deadline missed many times fires once.
func (c *cronState) advance(now time.Time) (bool, error) {
	if c.due.After(now) {
		return false, nil
	}
	next, err := NextDue(c.expr, now)
	if err != nil {
		return false, err
	}
	c.due = next
	return true, nil
}
