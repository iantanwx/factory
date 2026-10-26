package overwatch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/inbox"
)

// remindersPart reads `loop inbox list --awaits person` against each
// project's health.operator_reminder (C-bug-1). An entry that waited on the
// operator longer than its project's limit is work. Its key counts the
// whole limits it waited, so a record that quotes the key files it until
// the next limit passes, and the finding rings once per limit. A project
// with no limit reports its entries and holds no work.
func remindersPart() Part {
	return Part{Name: "reminders", Command: "loop inbox list --awaits person", Read: readReminders}
}

func readReminders(ctx context.Context, w World) (Reading, error) {
	entries, err := w.Inbox(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the inbox: %w", err)
	}
	var projects []string
	byProject := map[string][]inbox.Entry{}
	for _, e := range entries {
		if e.Awaits != inbox.AwaitsPerson {
			continue
		}
		p := entryProject(e)
		if _, ok := byProject[p]; !ok {
			projects = append(projects, p)
		}
		byProject[p] = append(byProject[p], e)
	}
	var r Reading
	for _, p := range projects {
		var limit time.Duration
		if p != "" {
			if limit, err = reminderLimit(ctx, w, p); err != nil {
				r.Notes = append(r.Notes, fmt.Sprintf("UNREAD: %s: read the project config: %v", p, err))
				continue
			}
		}
		r.add(classifyReminders(p, limit, byProject[p], w.Now()))
	}
	return r, nil
}

// reminderLimit is the project's health.operator_reminder, zero when its
// config names none or the project has no config yet.
func reminderLimit(ctx context.Context, w World, project string) (time.Duration, error) {
	snap, err := w.ProjectConfig(ctx, project)
	switch {
	case errors.Is(err, config.ErrConfigMissing):
		return 0, nil
	case err != nil:
		return 0, err
	case snap == nil || snap.Config == nil:
		return 0, nil
	}
	return snap.Config.Health.OperatorReminder, nil
}

// entryProject is the project of an entry. A prepared question names none,
// so it takes the project of the entry it is about.
func entryProject(e inbox.Entry) string {
	if e.Project != "" || e.Kind != inbox.KindQuestion {
		return e.Project
	}
	if parts := strings.Split(e.Link, "/"); len(parts) == 3 {
		return parts[1]
	}
	return ""
}

// classifyReminders is the reminders part's rule for the entries of one
// project that await a person. An entry's age is from its At, when it
// entered the inbox. An entry older than limit is one finding. The other
// entries count in one note, which names none of them, since each waits
// on the operator and on no one else.
func classifyReminders(project string, limit time.Duration, entries []inbox.Entry, now time.Time) Reading {
	var r Reading
	quiet := 0
	for _, e := range entries {
		age := now.Sub(time.UnixMilli(e.At))
		if limit == 0 || e.At == 0 || age <= limit {
			quiet++
			continue
		}
		key := Key("reminders", e.ID, strconv.Itoa(int(age/limit)))
		r.Findings = append(r.Findings, Finding{Key: key, Line: fmt.Sprintf(
			"%s has waited on the operator for %s, past the reminder limit of %s of project %s: remind the operator (%s)",
			e.ID, short(age), short(limit), project, key)})
	}
	switch {
	case quiet == 0:
	case project == "":
		r.Notes = append(r.Notes, fmt.Sprintf("%d entries that await a person name no project, so no health.operator_reminder applies", quiet))
	case limit == 0:
		r.Notes = append(r.Notes, fmt.Sprintf("%s: %d entries await a person, and the project config names no health.operator_reminder, so none is work",
			project, quiet))
	default:
		r.Notes = append(r.Notes, fmt.Sprintf("%s: %d other entries await a person, none past the reminder limit of %s", project, quiet, short(limit)))
	}
	return r
}

// short prints d to the minute, with no zero unit at its end: 41h, 1h30m,
// 45m.
func short(d time.Duration) string {
	s := d.Truncate(time.Minute).String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
