package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/sched"
)

// stallsPart is the part `stalls`, with tickertown's `sweep` folded in
// (ADR 0009, "Which parts port"): work that stopped moving. An escalation
// that the fixer's panel passed by is work (C9). An item in a state that a
// stage takes stalls at the second fire of that stage with no run since it
// entered the state (C10), unless a live run of any stage holds its
// lease. A fire that took only older items of the stage's queue is the
// queue draining, and does not count. A fire while the server drains
// admits no run, and does not count either. A ticket in
// in_progress that no lease holds and no live run names is lost, and work
// at once (C11). The stage claim states come from admission, so the part
// lists no state of its own.
var stallsPart = Part{Name: "stalls", Read: readStalls}

// stallFires is how many fires of the stage that takes it an item or an
// escalation waits before it stalls. Two, not one: an item that entered its
// state just before a fire may miss that fire while the stage is at its
// cap (spec, Decisions).
const stallFires = 2

// panelRow is one escalation in awaiting_panel, and the fixer runs of its
// project.
type panelRow struct {
	id            int64
	project, item string
	raised        time.Time
	// passed is the newest fixer run of the project that ended after the
	// raise, and passedAt is when it ended. Empty when none did.
	passed   string
	passedAt time.Time
	// live is a fixer run of the project that has not ended. Empty when
	// none runs.
	live  string
	fixer *sched.Cron // the fixer's schedule in the project
}

// itemRow is one item in a state that a stage takes.
type itemRow struct {
	item    machine.Item
	stage   string
	entered time.Time
	// off says why the project runs no such stage. Empty when it does.
	off  string
	cron *sched.Cron // the stage's schedule in the project, while it runs
	// run is the newest run on the item that started at or after entered,
	// and runAt is when it started. Empty when none did.
	run   string
	runAt time.Time
	// queue is each run of the stage in the project on another item that
	// started at or after entered, oldest first.
	queue []queueRun
	lease string // the run that holds the item's lease and has not ended, such as "run r-c733f4f7982a of code-reviewer"
	hold  string // the room or escalation that holds the item, such as "room room-r7"
	alert string // an open retry_limit or run_blocked alert that names the item, such as "alert/7 retry_limit"
}

// queueRun is one run of a stage on an item of its queue.
type queueRun struct {
	run, item string
	at        time.Time
	// entered is when the item last entered a state that the stage takes,
	// at or before the run started. Zero when no transition dates it.
	entered time.Time
}

// lostRow is one ticket in in_progress.
type lostRow struct {
	item   machine.Item
	leased bool // a lease holds it
	live   bool // a run that has not ended names it
}

// stallRows are the rows the stalls part reads.
type stallRows struct {
	panels []panelRow
	items  []itemRow
	lost   []lostRow
}

// readStalls reads the rows of the stalls part, and classifies them.
func readStalls(ctx context.Context, w World) (Reading, error) {
	hc, err := w.Host(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read host config: %w", err)
	}
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the projects: %w", err)
	}
	var rows stallRows
	configs := map[string]*config.ProjectConfig{}
	for _, p := range projects {
		snap, err := w.ProjectConfig(ctx, p.ID)
		switch {
		case errors.Is(err, config.ErrConfigMissing):
		case err != nil:
			return Reading{}, fmt.Errorf("read the config of %s: %w", p.ID, err)
		default:
			configs[p.ID] = snap.Config
		}
		items, err := stageItems(ctx, w, hc, p.ID, configs[p.ID])
		if err != nil {
			return Reading{}, err
		}
		rows.items = append(rows.items, items...)
		lost, err := w.Items(ctx, p.ID, machine.ListFilter{Kind: machine.KindTicket, Status: machine.InProgress})
		if err != nil {
			return Reading{}, fmt.Errorf("read the tickets of %s in %s: %w", p.ID, machine.InProgress, err)
		}
		for _, it := range lost {
			rows.lost = append(rows.lost, lostRow{item: it})
		}
	}
	st := w.Store()
	if st == nil {
		return Reading{}, errors.New("no store to read escalations and runs from")
	}
	err = st.Read(ctx, func(tx *sql.Tx) error {
		var err error
		rows.panels, err = readPanels(ctx, tx, func(project string) (*sched.Cron, error) {
			return scheduleOf(hc, configs[project], config.StageFixer)
		})
		if err != nil {
			return err
		}
		for i := range rows.items {
			if err := readItem(ctx, tx, &rows.items[i]); err != nil {
				return err
			}
		}
		for i := range rows.lost {
			if err := readLost(ctx, tx, &rows.lost[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Reading{}, fmt.Errorf("read escalations and runs: %w", err)
	}
	return classifyStalls(rows, w.Now(), w.DrainSince()), nil
}

// scheduleOf parses a stage's schedule in a project: the project's
// override, else host config's.
func scheduleOf(hc *config.HostConfig, cfg *config.ProjectConfig, stage string) (*sched.Cron, error) {
	expr := hc.ThroughputFor(stage).Schedule
	if cfg != nil {
		if sc, ok := cfg.Stages[stage]; ok && sc.Schedule != "" {
			expr = sc.Schedule
		}
	}
	return sched.ParseCron(expr)
}

// stageItems lists each item of a project in a state that a run stage
// takes, with that stage's schedule, or why the project runs no such stage.
func stageItems(ctx context.Context, w World, hc *config.HostConfig, project string, cfg *config.ProjectConfig) ([]itemRow, error) {
	var out []itemRow
	for _, stage := range config.RunStages() {
		states := admission.ClaimStates(stage)
		if len(states) == 0 {
			continue
		}
		row := itemRow{stage: stage}
		switch {
		case cfg == nil:
			row.off = fmt.Sprintf("project %s has no config yet, so %s never fires", project, stage)
		case !cfg.StageEnabled(stage):
			row.off = fmt.Sprintf("project config disables %s", stage)
		default:
			var err error
			if row.cron, err = scheduleOf(hc, cfg, stage); err != nil {
				return nil, fmt.Errorf("the %s schedule of %s: %w", stage, project, err)
			}
		}
		for _, s := range states {
			items, err := w.Items(ctx, project, machine.ListFilter{Kind: s.Kind, Status: s.Status})
			if err != nil {
				return nil, fmt.Errorf("read the %ss of %s in %s: %w", s.Kind, project, s.Status, err)
			}
			for _, it := range items {
				row.item = it
				out = append(out, row)
			}
		}
	}
	return out, nil
}

// readItem reads when the item entered its state, the live run that holds
// its lease, the newest run on it since then, the runs of its stage on
// other items since then, a hold on it and an open alert that names it.
// The entry is the item's last transition that changed its status, as the
// work query dates it. When that transition led elsewhere, or none exists,
// the item's last bead write dates it.
func readItem(ctx context.Context, tx *sql.Tx, row *itemRow) error {
	it := row.item
	row.entered = it.Updated
	var to string
	var enteredAt int64
	err := tx.QueryRowContext(ctx, `SELECT to_status, at FROM transitions WHERE project = ? AND item = ? AND from_status <> to_status
		ORDER BY seq DESC LIMIT 1`, it.Project, it.ID).Scan(&to, &enteredAt)
	switch {
	case err == nil && to == it.Status:
		row.entered = time.UnixMilli(enteredAt)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}
	var owner, stage string
	err = tx.QueryRowContext(ctx, `SELECT r.run_id, r.stage FROM leases l JOIN runs r ON r.run_id = l.owner_run_id
		WHERE l.project = ? AND l.item = ? AND r.ended_at IS NULL`, it.Project, it.ID).Scan(&owner, &stage)
	switch {
	case err == nil:
		row.lease = "run " + owner + " of " + stage
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	var started int64
	err = tx.QueryRowContext(ctx, `SELECT run_id, started_at FROM runs WHERE project = ? AND item = ? AND started_at >= ?
		ORDER BY started_at DESC, run_id LIMIT 1`, it.Project, it.ID, row.entered.UnixMilli()).Scan(&row.run, &started)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	row.runAt = time.UnixMilli(started)
	if err := readQueue(ctx, tx, row); err != nil {
		return err
	}
	var kind, holder string
	err = tx.QueryRowContext(ctx, `SELECT kind, holder FROM holds WHERE project = ? AND item = ? ORDER BY created_at, holder LIMIT 1`,
		it.Project, it.ID).Scan(&kind, &holder)
	switch {
	case err == nil:
		row.hold = kind + " " + holder
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	// The scheduler keys both alerts <kind>:<project>/<item>/<stage>/<inputs>.
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id, kind FROM alerts WHERE state = 'open' AND kind IN (?, ?)
		AND substr(dedupe_key, 1, length(kind) + ?) = kind || ? ORDER BY id LIMIT 1`,
		sched.AlertRetryLimit, sched.AlertRunBlocked, len(it.Project)+len(it.ID)+3, ":"+it.Project+"/"+it.ID+"/").Scan(&id, &kind)
	switch {
	case err == nil:
		row.alert = fmt.Sprintf("alert/%d %s", id, kind)
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	return nil
}

// readQueue reads each run of the row's stage in its project on another
// item that started at or after the row entered, and when that item last
// entered a state that the stage takes before the run started.
func readQueue(ctx context.Context, tx *sql.Tx, row *itemRow) error {
	var in []string
	var args []any
	for _, s := range admission.ClaimStates(row.stage) {
		in = append(in, "?")
		args = append(args, s.Status)
	}
	args = append(args, row.item.Project, row.stage, row.item.ID, row.entered.UnixMilli())
	rs, err := tx.QueryContext(ctx, `SELECT r.run_id, r.item, r.started_at,
		(SELECT t.at FROM transitions t WHERE t.project = r.project AND t.item = r.item AND t.from_status <> t.to_status
			AND t.at <= r.started_at AND t.to_status IN (`+strings.Join(in, ", ")+`) ORDER BY t.seq DESC LIMIT 1)
		FROM runs r WHERE r.project = ? AND r.stage = ? AND r.item <> ? AND r.started_at >= ?
		ORDER BY r.started_at, r.run_id`, args...)
	if err != nil {
		return err
	}
	for rs.Next() {
		var q queueRun
		var started int64
		var entered sql.NullInt64
		if err := rs.Scan(&q.run, &q.item, &started, &entered); err != nil {
			_ = rs.Close()
			return err
		}
		q.at = time.UnixMilli(started)
		if entered.Valid {
			q.entered = time.UnixMilli(entered.Int64)
		}
		row.queue = append(row.queue, q)
	}
	return errors.Join(rs.Err(), rs.Close())
}

// readLost reads whether a lease holds the ticket, and whether a live run
// names it.
func readLost(ctx context.Context, tx *sql.Tx, row *lostRow) error {
	return tx.QueryRowContext(ctx, `SELECT
		EXISTS (SELECT 1 FROM leases WHERE project = ?1 AND item = ?2 AND owner_run_id IS NOT NULL AND owner_run_id <> ''),
		EXISTS (SELECT 1 FROM runs WHERE project = ?1 AND item = ?2 AND ended_at IS NULL)`,
		row.item.Project, row.item.ID).Scan(&row.leased, &row.live)
}

// readPanels reads each escalation in awaiting_panel, and the fixer runs
// of its project.
func readPanels(ctx context.Context, tx *sql.Tx, fixer func(project string) (*sched.Cron, error)) ([]panelRow, error) {
	rs, err := tx.QueryContext(ctx, `SELECT id, project, item, created_at FROM escalations WHERE state = ? ORDER BY id`,
		escalation.StateAwaitingPanel)
	if err != nil {
		return nil, err
	}
	var out []panelRow
	for rs.Next() {
		var p panelRow
		var raised int64
		if err := rs.Scan(&p.id, &p.project, &p.item, &raised); err != nil {
			_ = rs.Close()
			return nil, err
		}
		p.raised = time.UnixMilli(raised)
		out = append(out, p)
	}
	if err := errors.Join(rs.Err(), rs.Close()); err != nil {
		return nil, err
	}
	for i := range out {
		p := &out[i]
		var ended sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT run_id, ended_at FROM runs WHERE project = ? AND stage = ? AND ended_at > ?
			ORDER BY ended_at DESC, run_id LIMIT 1`, p.project, config.StageFixer, p.raised.UnixMilli()).Scan(&p.passed, &ended)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		p.passedAt = time.UnixMilli(ended.Int64)
		err = tx.QueryRowContext(ctx, `SELECT run_id FROM runs WHERE project = ? AND stage = ? AND ended_at IS NULL
			ORDER BY started_at, run_id LIMIT 1`, p.project, config.StageFixer).Scan(&p.live)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if p.fixer, err = fixer(p.project); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// classifyStalls is the stalls part's rule: the rows, the clock and the
// start of a drain in, the reading out. A zero drain means none is on.
func classifyStalls(rows stallRows, now, drain time.Time) Reading {
	var r Reading
	for _, p := range rows.panels {
		key := Key("stalls", fmt.Sprintf("escalation-%d", p.id))
		head := fmt.Sprintf("escalation %d (panel/%d) of %s on %s waits in %s", p.id, p.id, p.project, p.item, escalation.StateAwaitingPanel)
		switch n := fires(p.fixer, p.raised, now); {
		case p.passed != "":
			r.Findings = append(r.Findings, Finding{Key: key, Line: fmt.Sprintf("%s, and fixer run %s ended at %s, after its raise at %s (%s)",
				head, p.passed, stamp(p.passedAt), stamp(p.raised), key)})
		case p.live != "":
			r.Notes = append(r.Notes, fmt.Sprintf("%s: fixer run %s runs now", head, p.live))
		case n >= stallFires:
			r.Findings = append(r.Findings, Finding{Key: key, Line: fmt.Sprintf("%s: the fixer fired twice since its raise at %s, and no fixer run ended (%s)",
				head, stamp(p.raised), key)})
		default:
			r.Notes = append(r.Notes, fmt.Sprintf("%s: %s of the fixer since its raise at %s", head, fireCount(n), stamp(p.raised)))
		}
	}
	for _, row := range rows.items {
		it := row.item
		key := Key("stalls", it.ID+"@"+it.Status)
		head := fmt.Sprintf("%s (%s of %s) waits in %s", it.ID, it.Kind, it.Project, it.Status)
		var why, tookOlder string
		switch {
		case row.lease != "":
			why = row.lease + " holds its lease"
		case row.off != "":
			why = row.off
		case row.hold != "":
			why = row.hold + " holds it"
		case row.alert != "":
			why = row.alert + " is open on it"
		case row.run != "":
			why = fmt.Sprintf("run %s started at %s, after it entered at %s", row.run, stamp(row.runAt), stamp(row.entered))
		default:
			n, drained := queueFires(row, now, drain)
			if len(drained) > 0 {
				tookOlder = "; the fires that took only older items of its queue started " + tookList(drained)
			}
			if n < stallFires {
				why = fmt.Sprintf("%s of %s since it entered at %s", fireCount(n), row.stage, stamp(row.entered))
				if tookOlder != "" {
					why += " counts against it" + tookOlder
				}
				if !drain.IsZero() {
					why += fmt.Sprintf("; the server drains since %s and admits no run, so no fire since then counts against it", stamp(drain))
				}
			}
		}
		if why != "" {
			r.Notes = append(r.Notes, head+": "+why)
			continue
		}
		r.Findings = append(r.Findings, Finding{Key: key, Line: fmt.Sprintf("%s (%s of %s) stalls in %s: two fires of %s since it entered at %s count against it: neither started a run on it, and neither took only older items of its queue%s (%s)",
			it.ID, it.Kind, it.Project, it.Status, row.stage, stamp(row.entered), tookOlder, key)})
	}
	for _, row := range rows.lost {
		if row.leased || row.live {
			continue
		}
		it := row.item
		key := Key("stalls", it.ID+"@"+it.Status)
		r.Findings = append(r.Findings, Finding{Key: key, Line: fmt.Sprintf("%s (%s of %s) is lost in %s: no lease holds it, and no live run names it (%s)",
			it.ID, it.Kind, it.Project, it.Status, key)})
	}
	return r
}

// fires counts the fires of c after since and at or before now, up to
// stallFires. A schedule that no date holds never fires.
func fires(c *sched.Cron, since, now time.Time) int {
	n := 0
	for t := since; n < stallFires; n++ {
		next, err := c.Next(t)
		if err != nil || next.After(now) {
			return n
		}
		t = next
	}
	return n
}

// queueFires counts the fires of the row's stage after it entered and at
// or before now that count against it, up to stallFires, and returns the
// runs of the fires that did not count. A fire counts unless the runs that
// started from it until the next fire all took items that entered the
// queue before the row did: that is the queue draining. An item that no
// transition dates counts as younger. A fire at or after a non-zero drain
// never counts: the drain lasts until the server restarts, and admits no
// run. A schedule that no date holds never fires.
func queueFires(row itemRow, now, drain time.Time) (n int, drained []queueRun) {
	next, err := row.cron.Next(row.entered)
	for n < stallFires && err == nil && !next.After(now) && (drain.IsZero() || next.Before(drain)) {
		fire := next
		next, err = row.cron.Next(fire)
		var took []queueRun
		younger := false
		for _, q := range row.queue {
			switch {
			case q.at.Before(fire) || err == nil && !q.at.Before(next): // another fire started it
			case q.entered.IsZero() || !q.entered.Before(row.entered):
				younger = true
			default:
				took = append(took, q)
			}
		}
		if len(took) == 0 || younger {
			n++
			continue
		}
		drained = append(drained, took...)
	}
	return n, drained
}

// tookList names each run and the item it took.
func tookList(runs []queueRun) string {
	var out []string
	for _, q := range runs {
		out = append(out, fmt.Sprintf("run %s on %s at %s", q.run, q.item, stamp(q.at)))
	}
	return strings.Join(out, ", ")
}

// fireCount says n fires below stallFires in words.
func fireCount(n int) string {
	if n == 0 {
		return "no fire"
	}
	return "only one fire"
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
