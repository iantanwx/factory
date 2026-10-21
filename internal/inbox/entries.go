package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/sched"
)

// DeferKind is the schedule row kind of `inbox defer --until`. Its stage
// column holds the entry id and its due_at the time the entry returns.
const DeferKind = "inbox_defer"

// Alert kinds that wait on a person: only the operator stores a secret or
// edits the repository's tracked files. Every other alert asks the
// coordinator to look (KTD9).
var personAlerts = map[string]bool{sched.AlertMissingSecret: true, sched.AlertConfigConflict: true}

// Entry ids. The id names its kind and its source, so an answer finds the
// source again without a lookup table.
func itemEntry(kind, project, item string) string { return kind + "/" + project + "/" + item }

func rowEntry(kind string, id int64) string { return kind + "/" + strconv.FormatInt(id, 10) }

// parseEntry splits an entry id.
func parseEntry(id string) (kind, project, item string, row int64, ok bool) {
	parts := strings.Split(id, "/")
	switch {
	case len(parts) == 3 && (parts[0] == KindEscalation || parts[0] == KindDecision || parts[0] == KindSignoff):
		return parts[0], parts[1], parts[2], 0, parts[1] != "" && parts[2] != ""
	case len(parts) == 2 && (parts[0] == KindQuestion || parts[0] == KindAlert || parts[0] == KindPanel):
		n, err := strconv.ParseInt(parts[1], 10, 64)
		return parts[0], "", "", n, err == nil && n > 0
	case len(parts) == 2 && parts[0] == KindRoom:
		return parts[0], "", parts[1], 0, parts[1] != ""
	}
	return "", "", "", 0, false
}

// itemSource is one beads state an entry kind reads.
type itemSource struct {
	kind   string
	filter machine.ListFilter
}

var itemSources = []itemSource{
	{KindEscalation, machine.ListFilter{Status: machine.Escalated}},
	{KindDecision, machine.ListFilter{Kind: machine.KindDecision, Status: machine.NeedsHuman}},
	{KindSignoff, machine.ListFilter{Kind: machine.KindEpic, Status: machine.PRDSignoff}},
	{KindSignoff, machine.ListFilter{Kind: machine.KindEpic, Status: machine.SpecSignoff}},
}

// Entries builds the inbox now: every entry, oldest first. A deferred
// entry is absent until its time. A project whose beads cannot be read
// shows as one coordinator entry, and the rest of the inbox still reads.
func (in *Inbox) Entries(ctx context.Context) ([]Entry, error) {
	now := in.cfg.Now().UnixMilli()
	var items []machine.Item
	var kinds []string
	var out []Entry
	if in.cfg.Machine != nil && in.cfg.Projects != nil {
		projects, err := in.cfg.Projects()
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			for _, src := range itemSources {
				got, ref := in.cfg.Machine.List(ctx, p.ID, src.filter)
				if ref != nil {
					// At stays zero, so a beads outage rings no doorbell on
					// every poll. `inbox list` still shows it.
					out = append(out, Entry{ID: "unread/" + p.ID, Kind: KindAlert, Awaits: AwaitsCoordinator, Project: p.ID,
						Title: "the inbox cannot read project " + p.ID, Text: ref.Code + ": " + ref.Hint})
					break
				}
				for _, it := range got {
					items, kinds = append(items, it), append(kinds, src.kind)
				}
			}
		}
	}
	defers := map[string]int64{}
	err := in.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if defers, err = readDefers(ctx, tx); err != nil {
			return err
		}
		for i, it := range items {
			e, err := in.itemEntry(ctx, tx, kinds[i], it)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		for _, read := range []func(context.Context, *sql.Tx) ([]Entry, error){in.panels, in.questions, in.rooms, in.alerts} {
			es, err := read(ctx, tx)
			if err != nil {
				return err
			}
			out = append(out, es...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	shown := out[:0]
	for _, e := range out {
		if due, ok := defers[e.ID]; ok {
			if due > now {
				continue
			}
			e.At = max(e.At, due)
		}
		e.Cursor = strconv.FormatInt(e.At, 10)
		shown = append(shown, e)
	}
	sort.SliceStable(shown, func(i, j int) bool {
		if shown[i].At != shown[j].At {
			return shown[i].At < shown[j].At
		}
		return shown[i].ID < shown[j].ID
	})
	return shown, nil
}

func readDefers(ctx context.Context, tx *sql.Tx) (map[string]int64, error) {
	rs, err := tx.QueryContext(ctx, `SELECT stage, due_at FROM schedule WHERE kind = ? AND state = 'active'`, DeferKind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	out := map[string]int64{}
	for rs.Next() {
		var id string
		var due sql.NullInt64
		if err := rs.Scan(&id, &due); err != nil {
			return nil, err
		}
		out[id] = due.Int64
	}
	return out, rs.Err()
}

// enteredAt is when the item last entered its status.
func enteredAt(ctx context.Context, tx *sql.Tx, it machine.Item) (int64, string, error) {
	var at sql.NullInt64
	var says sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT at, says FROM transitions WHERE project = ? AND item = ? AND to_status = ?
		ORDER BY seq DESC LIMIT 1`, it.Project, it.ID, it.Status).Scan(&at, &says)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	return at.Int64, says.String, err
}

func (in *Inbox) itemEntry(ctx context.Context, tx *sql.Tx, kind string, it machine.Item) (Entry, error) {
	at, says, err := enteredAt(ctx, tx, it)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{ID: itemEntry(kind, it.Project, it.ID), Kind: kind, Awaits: AwaitsPerson, Project: it.Project, Item: it.ID,
		Status: it.Status, Title: it.Title, Link: in.cfg.Link(it.Project, it.ID), At: at}
	var text []string
	switch kind {
	case KindEscalation:
		e.Next = "loop inbox answer " + e.ID + " --body <the operator's answer> [--amend]"
		rec, err := escalation.Latest(ctx, tx, it.Project, it.ID, escalation.StatePerson)
		if err != nil {
			return Entry{}, err
		}
		if rec != nil {
			text = append(text, fmt.Sprintf("Escalated at level %d %s, choice %s, and the fixer's panel routed it to a person. The finding:",
				rec.Level, rec.LevelName, rec.Choice), AgentText(rec.RunID, rec.Finding))
		} else {
			text = append(text, "Escalated: "+orDefault(says, "the item reached escalated"))
		}
		if r := it.Fact(machine.FactResume); r != "" {
			text = append(text, "An answer returns it to "+r+".")
		}
	case KindDecision:
		e.Next = "loop inbox answer " + e.ID + " --body <the operator's answer>"
		text = append(text, "The decision's trigger fired. The trigger, as filed:", AgentText(it.Fact(FactFiledBy), it.Fact(FactTrigger)))
	case KindSignoff:
		e.Next = "the operator merges the document's pull request; the merge is the sign-off"
		text = append(text, fmt.Sprintf("The %s passed review and waits for the operator's sign-off.", docOf(it.Status)))
	}
	if n := it.Fact(machine.FactNote); n != "" && kind != KindSignoff {
		text = append(text, "The item's last note:", AgentText(it.Fact(machine.FactOwner), n))
	}
	e.Text = strings.Join(text, "\n\n")
	return e, nil
}

// Decision facts that `decision file` writes (internal/decision).
const (
	FactTrigger = "trigger"
	FactBy      = "by"
	FactFiledBy = "filed_by"
)

func docOf(status string) string {
	if status == machine.SpecSignoff {
		return "spec"
	}
	return "PRD"
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func (in *Inbox) panels(ctx context.Context, tx *sql.Tx) ([]Entry, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id, project, item, run_id, level, choice, finding, created_at FROM escalations
		WHERE state = ? ORDER BY id`, escalation.StateAwaitingPanel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []Entry
	for rs.Next() {
		var id, at int64
		var level int
		var project, item, run, choice, finding string
		if err := rs.Scan(&id, &project, &item, &run, &level, &choice, &finding, &at); err != nil {
			return nil, err
		}
		name := ""
		if level >= 1 && level <= len(escalation.Levels) {
			name = escalation.Levels[level-1].Name
		}
		out = append(out, Entry{ID: rowEntry(KindPanel, id), Kind: KindPanel, Awaits: AwaitsCoordinator, Project: project, Item: item,
			Title: fmt.Sprintf("escalation %d on %s waits for the fixer's panel", id, item),
			Text: fmt.Sprintf("Raised at level %d %s, choice %s. The fixer's next run judges it, and no stage takes the item until then. The finding:",
				level, name, choice) + "\n\n" + AgentText(run, finding),
			Link: in.cfg.Link(project, item), Next: "the fixer runs `loop escalation judge` and `loop escalation route`", At: at})
	}
	return out, rs.Err()
}

func (in *Inbox) questions(ctx context.Context, tx *sql.Tx) ([]Entry, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id, entry, body, run_id, created_at FROM questions WHERE answered_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []Entry
	for rs.Next() {
		var id, at int64
		var about, body, run string
		if err := rs.Scan(&id, &about, &body, &run, &at); err != nil {
			return nil, err
		}
		e := Entry{ID: rowEntry(KindQuestion, id), Kind: KindQuestion, Awaits: AwaitsPerson, Title: "a question the coordinator prepared",
			Text: AgentText(run, body), Next: "loop inbox answer " + rowEntry(KindQuestion, id) + " --body <the operator's answer>", At: at}
		if about != "" {
			e.Title, e.Link = "a question about "+about, about
		}
		out = append(out, e)
	}
	return out, rs.Err()
}

// Room states (internal/room).
const (
	roomOpening     = "opening"
	roomLive        = "live"
	roomIdle        = "idle"
	roomInterrupted = "interrupted"
	roomApplied     = "applied"
)

func (in *Inbox) rooms(ctx context.Context, tx *sql.Tx) ([]Entry, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id, project, item, kind, state, reason, opened_by, updated_at, opened_at FROM rooms
		WHERE closed_at IS NULL ORDER BY opened_at, id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []Entry
	for rs.Next() {
		var id, project, item, kind, state, reason, by string
		var updated, opened int64
		if err := rs.Scan(&id, &project, &item, &kind, &state, &reason, &by, &updated, &opened); err != nil {
			return nil, err
		}
		e := Entry{ID: KindRoom + "/" + id, Kind: KindRoom, Project: project, Item: item, Status: state,
			Title: fmt.Sprintf("%s room %s is %s", kind, id, state), Link: "loop room list", At: max(updated, opened)}
		switch state {
		case roomLive:
			e.Awaits, e.Next = AwaitsPerson, "the operator talks to the room agent in the room's session"
		case roomIdle:
			e.Awaits, e.Next = AwaitsPerson, "the room waits for the operator; it never closes itself. `loop room open` returns its secrets, and `loop room close` ends it"
		case roomInterrupted:
			e.Awaits, e.Next = AwaitsCoordinator, "the room's session ended without a close; `loop room open` reopens it in the same checkout"
		case roomApplied:
			e.Awaits, e.Next = AwaitsCoordinator, "the room applied its decision; `loop room close "+id+"` ends it"
		default:
			e.Awaits, e.Next = AwaitsCoordinator, "the room starts once the running lease on its item finishes"
		}
		e.Text = "Why it opened:\n\n" + AgentText(by, reason)
		out = append(out, e)
	}
	return out, rs.Err()
}

func (in *Inbox) alerts(ctx context.Context, tx *sql.Tx) ([]Entry, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id, project, kind, subject, body, count, last_at FROM alerts WHERE state = 'open' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []Entry
	for rs.Next() {
		var id, at int64
		var count int
		var project, kind, subject, body string
		if err := rs.Scan(&id, &project, &kind, &subject, &body, &count, &at); err != nil {
			return nil, err
		}
		e := Entry{ID: rowEntry(KindAlert, id), Kind: KindAlert, Awaits: AwaitsCoordinator, Project: project, Status: kind,
			Title: subject, Text: body, Next: "loop inbox answer " + rowEntry(KindAlert, id) + " --body <what was done> closes it", At: at}
		if count > 1 {
			e.Text = fmt.Sprintf("%s\n\nRaised %d times.", body, count)
		}
		if personAlerts[kind] {
			e.Awaits = AwaitsPerson
		}
		out = append(out, e)
	}
	return out, rs.Err()
}
