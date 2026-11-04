package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/sched"
)

// DeferKind is the schedule row kind of `inbox defer --until`. Its stage
// column holds the entry id, its due_at the time the entry returns, and
// its payload {"reason": "..."}. A row written before the reason holds {}.
const DeferKind = "inbox_defer"

// Alert kinds that wait on a person: only the operator stores a secret or
// edits the repository's tracked files. Every other alert asks the
// coordinator to look (KTD9).
var personAlerts = map[string]bool{sched.AlertMissingSecret: true, sched.AlertConfigConflict: true, sched.AlertCheckoutKept: true}

// Entry ids. The id names its kind and its source, so an answer finds the
// source again without a lookup table.
func itemEntry(kind, project, item string) string { return kind + "/" + project + "/" + item }

func rowEntry(kind string, id int64) string { return kind + "/" + strconv.FormatInt(id, 10) }

// pauseEntry is `pause/<project>/<stage>`, or `pause/<stage>` for a pause
// of every project.
func pauseEntry(project, stage string) string {
	if project == "" {
		return KindPause + "/" + stage
	}
	return itemEntry(KindPause, project, stage)
}

// parseEntry splits an entry id. A pause entry names its stage as item.
func parseEntry(id string) (kind, project, item string, row int64, ok bool) {
	parts := strings.Split(id, "/")
	switch {
	case len(parts) == 3 && (parts[0] == KindEscalation || parts[0] == KindHold || parts[0] == KindSignoff || parts[0] == KindAsk ||
		parts[0] == KindArchitecture || parts[0] == KindDesign || parts[0] == KindPause):
		return parts[0], parts[1], parts[2], 0, parts[1] != "" && parts[2] != ""
	case len(parts) == 2 && (parts[0] == KindQuestion || parts[0] == KindAlert || parts[0] == KindPanel || parts[0] == KindNotice ||
		parts[0] == KindLearning):
		n, err := strconv.ParseInt(parts[1], 10, 64)
		return parts[0], "", "", n, err == nil && n > 0
	case len(parts) == 2 && (parts[0] == KindRoom || parts[0] == KindPause):
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
	{KindHold, machine.ListFilter{Kind: machine.KindDecision, Status: machine.NeedsHuman}},
	{KindSignoff, machine.ListFilter{Kind: machine.KindEpic, Status: machine.PRDSignoff}},
	{KindSignoff, machine.ListFilter{Kind: machine.KindEpic, Status: machine.SpecSignoff}},
	{KindSignoff, machine.ListFilter{Kind: machine.KindEpic, Status: machine.DesignSignoff}},
	{KindAsk, machine.ListFilter{Kind: machine.KindBug, Status: machine.Asked}},
	// An epic here makes an entry only with no open child, and, read in
	// the store, no open room on it.
	{KindArchitecture, machine.ListFilter{Kind: machine.KindEpic, Status: machine.Architecture}},
	// An epic here makes an entry only with no open room on it.
	{KindDesign, machine.ListFilter{Kind: machine.KindEpic, Status: machine.Design}},
}

// sourced is one item an entry kind read, with its link.
type sourced struct {
	kind string
	item machine.Item
	link string
}

// projectItems reads the items of every item source of one project. An
// architecture epic with an open child is left out, so a ticket filed
// under it ends its entry. A reader with no machine reads no open
// children, so it makes no architecture entry.
func (in *Inbox) projectItems(ctx context.Context, project string) ([]sourced, *cli.Refusal) {
	var out []sourced
	for _, src := range itemSources {
		if src.kind == KindArchitecture && in.cfg.Machine == nil {
			continue
		}
		got, ref := in.cfg.List(ctx, project, src.filter)
		if ref != nil {
			return nil, ref
		}
		for _, it := range got {
			if src.kind == KindArchitecture {
				open, ref := in.cfg.Machine.OpenChildren(ctx, project, it.ID)
				if ref != nil {
					return nil, ref
				}
				if len(open) > 0 {
					continue
				}
			}
			out = append(out, sourced{kind: src.kind, item: it, link: in.cfg.ItemLink(it)})
		}
	}
	return out, nil
}

// Entries builds the inbox now: every entry, oldest first. A deferred
// entry is absent until its time. A project whose beads cannot be read
// shows as one coordinator entry, and the rest of the inbox still reads.
func (in *Inbox) Entries(ctx context.Context) ([]Entry, error) {
	now := in.cfg.Now().UnixMilli()
	var items []sourced
	var out []Entry
	if in.cfg.List != nil && in.cfg.Projects != nil {
		projects, err := in.cfg.Projects()
		if err != nil {
			return nil, err
		}
		for _, p := range projects {
			got, ref := in.projectItems(ctx, p.ID)
			if ref != nil {
				// At stays zero, so a beads outage rings no doorbell on
				// every poll. `inbox list` still shows it.
				out = append(out, Entry{ID: "unread/" + p.ID, Kind: KindAlert, Awaits: AwaitsCoordinator, Project: p.ID,
					Title: "the inbox cannot read project " + p.ID, Text: ref.Code + ": " + ref.Hint})
				continue
			}
			items = append(items, got...)
		}
	}
	defers := map[string]deferRow{}
	err := in.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if defers, err = readDefers(ctx, tx); err != nil {
			return err
		}
		for _, it := range items {
			// A PRD reject cap escalates its epic and waits for the triage
			// panel. Its panel entry stands for it until the panel routes it.
			if it.kind == KindEscalation {
				rec, err := escalation.Latest(ctx, tx, it.item.Project, it.item.ID, escalation.StateAwaitingPanel)
				if err != nil {
					return err
				}
				if rec != nil {
					continue
				}
			}
			if it.kind == KindArchitecture || it.kind == KindDesign {
				roomed, err := openRoom(ctx, tx, it.item)
				if err != nil {
					return err
				}
				if roomed {
					continue
				}
			}
			e, err := in.itemEntry(ctx, tx, it.kind, it.item)
			if err != nil {
				return err
			}
			e.Link = it.link
			out = append(out, e)
		}
		for _, read := range []func(context.Context, *sql.Tx) ([]Entry, error){in.panels, in.questions, in.rooms, in.alerts, in.faults, in.pauses, in.notices, in.learnings} {
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
	// A panel's link may read beads, so it is set after the transaction.
	for i := range out {
		if out[i].Kind == KindPanel {
			out[i].Link = in.cfg.Link(out[i].Project, out[i].Item)
		}
	}
	shown := out[:0]
	for _, e := range out {
		if d, ok := defers[e.ID]; ok {
			if d.due > now {
				continue
			}
			e.At = max(e.At, d.due)
			e.DeferReason = d.reason
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

// deferRow is one active defer: when its entry returns, and why.
type deferRow struct {
	due    int64
	reason string
}

func readDefers(ctx context.Context, tx *sql.Tx) (map[string]deferRow, error) {
	rs, err := tx.QueryContext(ctx, `SELECT stage, due_at, coalesce(json_extract(payload, '$.reason'), '') FROM schedule
		WHERE kind = ? AND state = 'active'`, DeferKind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	out := map[string]deferRow{}
	for rs.Next() {
		var id, reason string
		var due sql.NullInt64
		if err := rs.Scan(&id, &due, &reason); err != nil {
			return nil, err
		}
		out[id] = deferRow{due: due.Int64, reason: reason}
	}
	return out, rs.Err()
}

// enteredAt is when the item last entered its status, what that move
// says, and who made it: the run, or the actor when no run made it. A note or
// an edit writes facts only, so it is no entry into the status.
func enteredAt(ctx context.Context, tx *sql.Tx, it machine.Item) (at int64, says, by string, err error) {
	var a sql.NullInt64
	var s, b sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT at, says, coalesce(nullif(run_id, ''), actor) FROM transitions
		WHERE project = ? AND item = ? AND to_status = ? AND verb NOT IN (?, ?) ORDER BY seq DESC LIMIT 1`,
		it.Project, it.ID, it.Status, machine.VerbNote, "edit").Scan(&a, &s, &b)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", "", nil
	}
	return a.Int64, s.String, b.String, err
}

// openRoom reports whether a room that is not closed serves the item.
func openRoom(ctx context.Context, tx *sql.Tx, it machine.Item) (bool, error) {
	var open bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM rooms WHERE project = ? AND item = ? AND closed_at IS NULL)`,
		it.Project, it.ID).Scan(&open)
	return open, err
}

func (in *Inbox) itemEntry(ctx context.Context, tx *sql.Tx, kind string, it machine.Item) (Entry, error) {
	at, says, movedBy, err := enteredAt(ctx, tx, it)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{ID: itemEntry(kind, it.Project, it.ID), Kind: kind, Awaits: AwaitsPerson, Project: it.Project, Item: it.ID,
		Status: it.Status, Title: it.Title, At: at}
	var text []string
	switch kind {
	case KindEscalation:
		e.Next = "loop inbox answer " + e.ID + " --body <the operator's answer> [--amend]"
		qs, err := openQuestions(ctx, tx, e.ID)
		if err != nil {
			return Entry{}, err
		}
		if len(qs) > 0 {
			e.Next = "first answer each open question about it: " + questionCommands(qs) + "; then " + e.Next
		}
		rec, err := escalation.Latest(ctx, tx, it.Project, it.ID, escalation.StatePerson)
		if err != nil {
			return Entry{}, err
		}
		if rec != nil {
			text = append(text, fmt.Sprintf("Escalated at level %d %s, choice %s, and the triage panel routed it to a person. The finding:",
				rec.Level, rec.LevelName, rec.Choice), AgentText(rec.RunID, rec.Finding))
			// The move that escalated a PRD reject cap names the room that
			// answers it.
			if says != "" {
				text = append(text, says)
			}
		} else {
			text = append(text, "Escalated: "+orDefault(says, "the item reached escalated"))
		}
		if r := it.Fact(machine.FactResume); r != "" {
			text = append(text, "An answer returns it to "+r+".")
		}
	case KindHold:
		e.Next = "loop inbox answer " + e.ID + " --body <the operator's answer>"
		text = append(text, "The decision's trigger fired. The trigger, as filed:", AgentText(it.Fact(FactFiledBy), it.Fact(FactTrigger)))
		if why := it.Fact(machine.FactWhy); why != "" {
			text = append(text, "Why it fired:", AgentText(movedBy, why))
		}
		// decision-timers closed the defer row when it fired, so the
		// reason comes from the fire (spec Decision 19).
		e.DeferReason = it.Fact(machine.FactDeferReason)
	case KindAsk:
		e.Next = "loop inbox answer " + e.ID + " --body <the reporter's answer, in the reporter's own words>"
		text = append(text, fmt.Sprintf("The reproducer cannot reproduce this bug report without a fact only its reporter holds. "+
			"Ask %d of %d. Relay the questions to the reporter. The answer returns the report to the reproducer. The questions:",
			askCount(it), machine.AskLimit), AgentText(it.Fact(machine.FactAskedBy), it.Fact(machine.AskFact(machine.FactQuestion, askCount(it)))))
	case KindSignoff:
		e.Head = it.Fact(machine.FactReviewedHead)
		e.Next = "loop inbox answer " + e.ID + ` --body "<the operator's approval, in the operator's own words>"; the server merges the document at head ` + orDefault(e.Head, "<none pinned>")
		at := ""
		if e.Head != "" {
			at = " at head " + e.Head
		}
		text = append(text, fmt.Sprintf("The %s passed review%s and waits for the operator's approval. On the approval the server merges that head.",
			docOf(it.Status), at))
	case KindArchitecture:
		e.Awaits, e.Next = AwaitsCoordinator, archActs(it)
		text = append(text, "The epic is in architecture, and no open ticket under it writes its ADR, and no room serves it.")
	case KindDesign:
		e.Awaits, e.Next = AwaitsCoordinator, designAct(it.Project, it.ID)
		text = append(text, "The epic is in design, and no room serves it. Ask the operator whether to open its design room, "+
			"or to skip the design stage with loop epic skip-design.")
	}
	if n := it.Fact(machine.FactNote); n != "" && kind != KindSignoff {
		text = append(text, "The item's last note:", AgentText(it.Fact(machine.FactOwner), n))
	}
	e.Text = strings.Join(text, "\n\n")
	return e, nil
}

// archActs names the two acts that end an architecture entry: a ticket
// filed under the epic, or an architect room opened on it. The ticket
// under a PRD epic that the operator sent to the ADR stage cites a story
// of its PRD.
func archActs(epic machine.Item) string {
	story := ""
	if epic.Fact(machine.FactPRDPath) != "" {
		story = " --story <a story label of its PRD>"
	}
	return fmt.Sprintf("loop work create --project %s --epic %s%s --request <the operator's words> --title <the ADR ticket>, "+
		"or loop room open --kind architect --project %s --item %s --reason <why> --score <level>", epic.Project, epic.ID, story, epic.Project, epic.ID)
}

// designAct names the act that ends a design entry: a design room opened
// on the epic.
func designAct(project, epic string) string {
	return fmt.Sprintf("loop room open --kind design --item %s --project %s --reason <why> --score <level>", epic, project)
}

// askCount is the number of the ask an asked report holds.
func askCount(it machine.Item) int {
	n, _ := strconv.Atoi(it.Fact(machine.FactAsks))
	return n
}

// Decision facts that `decision file` writes (internal/decision).
const (
	FactTrigger  = "trigger"
	FactBy       = "by"
	FactWaitsFor = "waits_for"
	FactFiledBy  = "filed_by"
)

func docOf(status string) string {
	switch status {
	case machine.SpecSignoff:
		return "spec"
	case machine.DesignSignoff:
		return "design record"
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
			Title: fmt.Sprintf("escalation %d on %s waits for the triage panel", id, item),
			Text: fmt.Sprintf("Raised at level %d %s, choice %s. The next triage run judges it, and no stage takes the item until then. The finding:",
				level, name, choice) + "\n\n" + AgentText(run, finding),
			Next: "a triage run calls `loop escalation judge` and `loop escalation route`", At: at})
	}
	return out, rs.Err()
}

// openQuestions returns the entry ids of the open questions about entry,
// oldest first.
func openQuestions(ctx context.Context, tx *sql.Tx, entry string) ([]string, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id FROM questions WHERE entry = ? AND answered_at IS NULL ORDER BY id`, entry)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []string
	for rs.Next() {
		var id int64
		if err := rs.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, rowEntry(KindQuestion, id))
	}
	return out, rs.Err()
}

func questionCommands(qs []string) string {
	cmds := make([]string, len(qs))
	for i, q := range qs {
		cmds[i] = "`loop inbox answer " + q + " --body <the operator's answer>`"
	}
	return strings.Join(cmds, ", ")
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
		case room.Live:
			e.Awaits, e.Next = AwaitsPerson, "the operator talks to the room agent in the room's session"
		case room.Idle:
			e.Awaits, e.Next = AwaitsPerson, "the room waits for the operator; it never closes itself. `loop room open` returns its secrets, and `loop room close` ends it"
		case room.Interrupted:
			e.Awaits, e.Next = AwaitsCoordinator, "the room's session ended without a close; `loop room open` reopens it in the same checkout"
		case room.Applied:
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
	rs, err := tx.QueryContext(ctx, `SELECT id, project, kind, subject, body, count, first_at, last_at, finding, finding_run,
		coalesce(finding_count, 0), coalesce(finding_last_at, 0) FROM alerts WHERE state = 'open' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []Entry
	for rs.Next() {
		var id, first, at, foundAt int64
		var count, foundCount int
		var project, kind, subject, body, finding, findingRun string
		if err := rs.Scan(&id, &project, &kind, &subject, &body, &count, &first, &at, &finding, &findingRun, &foundCount, &foundAt); err != nil {
			return nil, err
		}
		e := Entry{ID: rowEntry(KindAlert, id), Kind: KindAlert, Awaits: AwaitsCoordinator, Project: project, Status: kind,
			Title: subject, Text: body, Next: "loop inbox answer " + rowEntry(KindAlert, id) + " --body <what was done> closes it", At: at}
		switch {
		case finding != "":
			// The entry keeps the time it had at the note, so a repeat
			// after the finding rings no one. The count still shows it.
			e.Found, e.At = true, foundAt
			if count > 1 {
				e.Text = fmt.Sprintf("%s\n\nRaised %d times, %d of them after the finding.", body, count, count-foundCount)
			}
			e.Text += "\n\nThe finding:\n\n" + AgentText(findingRun, finding)
		case kind == sched.AlertStartFailed || kind == config.AlertOldStageNames:
			// A stage's cron raises this again at each refused dispatch,
			// for one cause. The entry keeps the time of the first raise,
			// so a repeat rings no one even before a finding (F-41). The
			// text shows the count and the last raise. Each config read
			// raises old_stage_names again, and the next clean read
			// clears it, so it keeps its first raise too (F-65).
			e.At = first
			if count > 1 {
				e.Text = fmt.Sprintf("%s\n\nRaised %d times. Last raised at %s.", body, count, time.UnixMilli(at).UTC().Format(time.RFC3339))
			}
		case count > 1:
			e.Text = fmt.Sprintf("%s\n\nRaised %d times.", body, count)
		}
		if personAlerts[kind] {
			e.Awaits = AwaitsPerson
		}
		out = append(out, e)
	}
	return out, rs.Err()
}

// faults shows each open or acknowledged loop fault (KTD31). It waits on
// the coordinator, which triages it with `loop fault ack` and
// `loop fault resolve`. A fault has no item or pull request, so its entry
// has no link. Its id names the fault.
func (in *Inbox) faults(ctx context.Context, tx *sql.Tx) ([]Entry, error) {
	fs, err := fault.Pending(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(fs))
	for _, f := range fs {
		by := f.Reporter
		if f.Stage != "" {
			by += " of stage " + f.Stage
		}
		if f.Project != "" {
			by += " on project " + f.Project
		}
		out = append(out, Entry{ID: KindFault + "/" + f.ID, Kind: KindFault, Awaits: AwaitsCoordinator, Project: f.Project, Item: f.Item,
			Status: f.Status, Title: fmt.Sprintf("loop fault %s, reported by %s %s under release %s", f.ID, by, f.RunID, f.Release),
			Text: "The report:\n\n" + AgentText(f.RunID, f.Text),
			Next: fmt.Sprintf("`loop fault ack %s`, then `loop fault resolve %s` with --fixed-in <release>, --duplicate-of <id>, or --not-a-fault --reason <text>",
				f.ID, f.ID),
			At: f.ReportedAt})
	}
	return out, nil
}

// pauses shows each stage pause (ADR 0004 D3). It waits on the
// coordinator, and only `loop stage resume` ends it. A pause has no item,
// so its entry has no link. It is dated when the stage paused.
func (in *Inbox) pauses(ctx context.Context, tx *sql.Tx) ([]Entry, error) {
	ps, err := admission.Pauses(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(ps))
	for _, p := range ps {
		scope := "every project"
		if p.Project != "" {
			scope = "project " + p.Project
		}
		out = append(out, Entry{ID: pauseEntry(p.Project, p.Stage), Kind: KindPause, Awaits: AwaitsCoordinator, Project: p.Project,
			Title: fmt.Sprintf("stage %s is paused in %s", p.Stage, scope),
			Text: fmt.Sprintf("No new run of %s starts in %s, and a live run continues. Paused by %s. The reason:", p.Stage, scope, p.By) +
				"\n\n" + AgentText(p.By, p.Reason),
			Next: resumeCommand(p.Project, p.Stage), At: p.At.UnixMilli()})
	}
	return out, nil
}

// resumeCommand is the one act that ends a pause entry.
func resumeCommand(project, stage string) string {
	if project == "" {
		return "loop stage resume " + stage
	}
	return "loop stage resume " + stage + " --project " + project
}

// notices shows each notice that no answer dismissed. It waits on a
// person, so the overwatch reminder brings it back when the coordinator
// never relays it, and its next asks nothing.
func (in *Inbox) notices(ctx context.Context, tx *sql.Tx) ([]Entry, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id, project, subject, body, at FROM notices WHERE dismissed_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rs.Close() }()
	var out []Entry
	for rs.Next() {
		var id, at int64
		var project, subject, body string
		if err := rs.Scan(&id, &project, &subject, &body, &at); err != nil {
			return nil, err
		}
		e := rowEntry(KindNotice, id)
		out = append(out, Entry{ID: e, Kind: KindNotice, Awaits: AwaitsPerson, Project: project, Title: subject, Text: body,
			Next: "tell the operator; `loop inbox answer " + e + " --body <anything>` dismisses it", At: at})
	}
	return out, rs.Err()
}

// learnings shows each retro learning that the route escalated. It waits
// on a person. Its text holds the learning and its cases, each judge's
// answers and Jev's reading, so the operator decides from the entry alone.
func (in *Inbox) learnings(ctx context.Context, tx *sql.Tx) ([]Entry, error) {
	ls, err := retro.Escalations(ctx, tx)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(ls))
	for _, l := range ls {
		id, _ := strings.CutPrefix(l.ID, "L-")
		e := KindLearning + "/" + id
		cases := make([]string, len(l.Cases))
		for i, c := range l.Cases {
			cases[i] = string(c)
		}
		judges := make([]string, len(l.Panel.Answers))
		for i, a := range l.Panel.Answers {
			judges[i] = fmt.Sprintf("%s: target %s, evidence %s, change %s. Reason: %s", a.Judge, a.Target, a.Evidence, a.Change, a.Reason)
		}
		jev := "Jev: no reading."
		if r := l.Panel.Reading; r != nil {
			jev = fmt.Sprintf("Jev: agreement %.2f, confidence %.2f.", escalation.Agreement(r), r.Confidence)
			if why := escalation.Unusable(r); why != "" {
				jev += " The reading is unusable: " + why + "."
			}
		}
		out = append(out, Entry{ID: e, Kind: KindLearning, Awaits: AwaitsPerson, Project: l.Project, Item: l.RetroItem,
			Title: fmt.Sprintf("learning %s from the retro of %s escalated, reason %s", l.ID, l.RetroItem, l.Reason),
			Text: "The learning and its cases:\n\n" + AgentText(l.RunID, l.Statement+"\n\nCases: "+strings.Join(cases, ", ")) +
				"\n\nThe judges:\n\n" + AgentText(l.RunID, strings.Join(judges, "\n")) + "\n\n" + jev,
			Next: "loop inbox answer " + e + " --body <the operator's answer>; then apply it with `loop work create --request` or `loop fault report`, or do nothing",
			At:   l.At})
	}
	return out, nil
}
