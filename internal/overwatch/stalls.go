package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/sched"
)

// stallsPart is the part `stalls`, with tickertown's `sweep` folded in
// (ADR 0009, "Which parts port"): work that stopped moving. An escalation
// that the triage panel passed by is work (C9). An item in a state that a
// stage takes stalls at the second fire of that stage with no run since it
// entered the state (C10), unless a live run of any stage holds its
// lease. A fire that took only older items of the stage's queue is the
// queue draining, and does not count. So is a fire of a mutex stage while
// a run on an older item of the queue held the stage mutex. A fire while
// the server drained admitted no run, and does not count either, also
// after the restart that ended the drain. Nor does a fire that started no
// run while a limit of the stage was full, nor a fire at which a live
// implementer run held a ticket whose "May change" list meets the
// implementer ticket's own (rule files_in_flight), nor a code review that
// admission refuses while CI at the head a simplify run left runs inside the CI
// budget (rule simplify_head_ci). An item at a stage that a
// pause holds, in its project or in every project, waits for the resume
// (C25). A ticket in
// in_progress that no lease holds and no live run names is lost, and work
// at once (C11). The stage claim states come from admission, so the part
// lists no state of its own.
var stallsPart = Part{Name: "stalls", Read: readStalls}

// stallFires is how many fires of the stage that takes it an item or an
// escalation waits before it stalls. Two, not one: an item that entered its
// state just before a fire may miss that fire while the stage is at its
// cap (spec, Decisions).
const stallFires = 2

// panelRow is one escalation in awaiting_panel, and the triage runs of its
// project.
type panelRow struct {
	id            int64
	project, item string
	raised        time.Time
	// passed is the newest triage run of the project that ended after the
	// raise, and passedAt is when it ended. Empty when none did.
	passed   string
	passedAt time.Time
	// live is a triage run of the project that has not ended. Empty when
	// none runs.
	live   string
	triage *sched.Cron // the triage schedule in the project
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
	// mutex is each run of a mutex stage in the project on another item
	// that had not ended when the item entered, oldest first. While one is
	// live, it holds the stage mutex, and admission refuses every other
	// run of the stage.
	mutex []queueRun
	// limits are the stage's caps in host config, and capRuns each run of
	// a limit's stages on the instance that had not ended when the item
	// entered, so a fire can tell whether a cap was full.
	limits  []config.Limit
	capRuns []capRun
	lease   string // the run that holds the item's lease and has not ended, such as "run r-c733f4f7982a of code-reviewer"
	pause   string // the pause that holds the stage and the verb that ends it, such as `implementer is paused in factorio ("the queue is red"), and ...`
	hold    string // the room or escalation that holds the item, such as "room room-r7"
	alert   string // an open retry_limit or run_blocked alert that names the item, such as "alert/7 retry_limit"
	ci      string // the CI run at the simplify head that admission waits for, inside the CI budget
	// files is each implementer run on another ticket of the project that
	// had not ended when an implementer row entered, and whose ticket's
	// "May change" list meets the row's. While one is live, admission
	// refuses the row with rule files_in_flight.
	files []fileRun
}

// queueRun is one run of a stage on an item of its queue.
type queueRun struct {
	run, item string
	at        time.Time
	// entered is when the item last entered a state that the stage takes,
	// at or before the run started. Zero when no transition dates it.
	entered time.Time
	ended   time.Time // a zero ended: the run is live
}

// fileRun is one implementer run on another ticket, and the entry of the
// row's list that meets the entry of that ticket's list.
type fileRun struct {
	queueRun
	mine, theirs string
}

// capRun is one run that a limit of a row's stage counts.
type capRun struct {
	stage          string
	started, ended time.Time // a zero ended: the run is live
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
	pauses, err := w.Pauses(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the stage pauses: %w", err)
	}
	drains, err := w.Drains(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the drains: %w", err)
	}
	var rows stallRows
	var unread []string
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
		for i := range items {
			items[i].pause = pauseOf(pauses, p.ID, items[i].stage)
		}
		if cfg := configs[p.ID]; cfg != nil {
			unread = append(unread, readSimplifyCI(ctx, w, p, cfg.Health.CIBudget, items)...)
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
			return scheduleOf(hc, configs[project], config.StageTriage)
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
	unread = append(unread, readFiles(ctx, w, rows.items)...)
	r := classifyStalls(rows, w.Now(), drains)
	r.Notes = append(r.Notes, unread...)
	return r, nil
}

// readSimplifyCI reads CI at the open pull request's head of each code
// review row whose item holds a simplify_from that the head differs from.
// When admission's rule simplify_head_ci refuses the review while that CI
// runs inside budget, the row's ci says so. A read that fails leaves the
// row as it is, and returns its UNREAD line.
func readSimplifyCI(ctx context.Context, w World, p config.Project, budget time.Duration, rows []itemRow) []string {
	waits := func(row itemRow) bool {
		return row.stage == config.StageCodeReviewer && row.item.Fact(machine.FactSimplifyFrom) != "" && row.off == ""
	}
	if w.GitHub() == nil || !slices.ContainsFunc(rows, waits) {
		return nil
	}
	pulls, err := w.GitHub().OpenPulls(ctx, p)
	if err != nil {
		return []string{unreadLine(p, "the open pull requests", err)}
	}
	var unread []string
	for i := range rows {
		row := &rows[i]
		if !waits(*row) {
			continue
		}
		var line string
		if row.ci, line = simplifyHeadCI(ctx, w, p, pulls, row.item, budget); line != "" {
			unread = append(unread, line)
		}
	}
	return unread
}

// simplifyHeadCI reads CI at the head of the open pull request of it among
// pulls, when it holds a simplify_from that the head differs from, and
// returns the run that admission waits for there by ciWait. A read that
// fails returns its UNREAD line, and no wait.
func simplifyHeadCI(ctx context.Context, w World, p config.Project, pulls []github.PR, it machine.Item, budget time.Duration) (wait, unread string) {
	from := it.Fact(machine.FactSimplifyFrom)
	j := slices.IndexFunc(pulls, func(pr github.PR) bool { return pr.Head.Ref == github.BranchFor(it.ID) })
	if from == "" || j < 0 || pulls[j].Head.SHA == from {
		return "", ""
	}
	head := pulls[j].Head.SHA
	runs, err := w.GitHub().WorkflowRuns(ctx, p, github.RunQuery{HeadSHA: head})
	if err != nil {
		return "", unreadLine(p, fmt.Sprintf("CI at %s, the head of %s", head, it.ID), err)
	}
	return ciWait(head, runs, budget, w.Now()), ""
}

// ciWait says which run admission waits for at head: the oldest that has
// not completed, while it runs inside budget and no run there is red. A
// project with no budget sets no limit. Empty when no run waits so: CI at
// head is red, green, past budget, or not started.
func ciWait(head string, runs []github.RunView, budget time.Duration, now time.Time) string {
	var wait *github.RunView
	for i, run := range runs {
		switch {
		case run.Status != "completed":
			if wait == nil || run.StartedAt.Before(wait.StartedAt) {
				wait = &runs[i]
			}
		case !slices.Contains([]string{"success", "neutral", "skipped", "cancelled"}, run.Conclusion):
			return ""
		}
	}
	if wait == nil {
		return ""
	}
	inside := "no health.ci_budget limits it"
	if budget > 0 {
		if now.Sub(wait.StartedAt) > budget {
			return ""
		}
		inside = fmt.Sprintf("inside the CI budget of %s", budget)
	}
	return fmt.Sprintf("admission refuses its code review with rule simplify_head_ci until CI is green at %s, the head its simplify run left: run %s %d there runs since %s, %s",
		head, wait.Workflow, wait.ID, stamp(wait.StartedAt), inside)
}

// scheduleOf parses a stage's schedule in force in a project.
func scheduleOf(hc *config.HostConfig, cfg *config.ProjectConfig, stage string) (*sched.Cron, error) {
	return sched.ParseCron(config.Effective(hc, cfg).Schedule(stage))
}

// pauseOf names the oldest pause that holds stage in project, its reason
// and the verb that ends it. Empty when none does.
func pauseOf(pauses []admission.StagePause, project, stage string) string {
	for _, p := range pauses {
		if p.Stage != stage || p.Project != "" && p.Project != project {
			continue
		}
		scope, flag := "every project", ""
		if p.Project != "" {
			scope, flag = p.Project, " --project "+p.Project
		}
		return fmt.Sprintf("%s is paused in %s (%q), and `loop stage resume %s%s` ends the pause", stage, scope, p.Reason, stage, flag)
	}
	return ""
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
		row := itemRow{stage: stage, limits: hc.Limits(stage)}
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
	if row.lease, err = leaseOf(ctx, tx, it); err != nil {
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
	if err := readCapRuns(ctx, tx, row); err != nil {
		return err
	}
	if row.stage == config.StageImplementer {
		runs, err := queueRuns(ctx, tx, row, `(r.ended_at IS NULL OR r.ended_at > ?)`)
		if err != nil {
			return err
		}
		for _, q := range runs {
			row.files = append(row.files, fileRun{queueRun: q})
		}
	}
	if row.hold, err = holdOf(ctx, tx, it); err != nil {
		return err
	}
	row.alert, err = alertOf(ctx, tx, it)
	return err
}

// leaseOf names the run that holds the item's lease and has not ended,
// such as "run r-c733f4f7982a of code-reviewer". Empty when none does.
func leaseOf(ctx context.Context, tx *sql.Tx, it machine.Item) (string, error) {
	var owner, stage string
	err := tx.QueryRowContext(ctx, `SELECT r.run_id, r.stage FROM leases l JOIN runs r ON r.run_id = l.owner_run_id
		WHERE l.project = ? AND l.item = ? AND r.ended_at IS NULL`, it.Project, it.ID).Scan(&owner, &stage)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return "run " + owner + " of " + stage, err
}

// holdOf names the oldest room or escalation that holds the item, such as
// "room room-r7". Empty when none does.
func holdOf(ctx context.Context, tx *sql.Tx, it machine.Item) (string, error) {
	var kind, holder string
	err := tx.QueryRowContext(ctx, `SELECT kind, holder FROM holds WHERE project = ? AND item = ? ORDER BY created_at, holder LIMIT 1`,
		it.Project, it.ID).Scan(&kind, &holder)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return kind + " " + holder, err
}

// alertOf names the oldest open retry_limit or run_blocked alert that names
// the item, such as "alert/7 retry_limit". Empty when none does.
func alertOf(ctx context.Context, tx *sql.Tx, it machine.Item) (string, error) {
	// The scheduler keys both alerts <kind>:<project>/<item>/<stage>/<inputs>.
	var id int64
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT id, kind FROM alerts WHERE state = 'open' AND kind IN (?, ?)
		AND substr(dedupe_key, 1, length(kind) + ?) = kind || ? ORDER BY id LIMIT 1`,
		sched.AlertRetryLimit, sched.AlertRunBlocked, len(it.Project)+len(it.ID)+3, ":"+it.Project+"/"+it.ID+"/").Scan(&id, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return fmt.Sprintf("alert/%d %s", id, kind), err
}

// readQueue reads each run of the row's stage in its project on another
// item that started at or after the row entered, and for a mutex stage
// each such run that had not ended when the row entered.
func readQueue(ctx context.Context, tx *sql.Tx, row *itemRow) (err error) {
	if row.queue, err = queueRuns(ctx, tx, row, `r.started_at >= ?`); err != nil || !admission.MutexStage(row.stage) {
		return err
	}
	row.mutex, err = queueRuns(ctx, tx, row, `(r.ended_at IS NULL OR r.ended_at > ?)`)
	return err
}

// queueRuns reads each run of the row's stage in its project on another
// item that cond holds for at the row's entry, oldest first, and when that
// item last entered a state that the stage takes before the run started.
func queueRuns(ctx context.Context, tx *sql.Tx, row *itemRow, cond string) ([]queueRun, error) {
	var in []string
	var args []any
	for _, s := range admission.ClaimStates(row.stage) {
		in = append(in, "?")
		args = append(args, s.Status)
	}
	args = append(args, row.item.Project, row.stage, row.item.ID, row.entered.UnixMilli())
	rs, err := tx.QueryContext(ctx, `SELECT r.run_id, r.item, r.started_at, r.ended_at,
		(SELECT t.at FROM transitions t WHERE t.project = r.project AND t.item = r.item AND t.from_status <> t.to_status
			AND t.at <= r.started_at AND t.to_status IN (`+strings.Join(in, ", ")+`) ORDER BY t.seq DESC LIMIT 1)
		FROM runs r WHERE r.project = ? AND r.stage = ? AND r.item <> ? AND `+cond+`
		ORDER BY r.started_at, r.run_id`, args...)
	if err != nil {
		return nil, err
	}
	var out []queueRun
	for rs.Next() {
		var q queueRun
		var started int64
		var ended, entered sql.NullInt64
		if err := rs.Scan(&q.run, &q.item, &started, &ended, &entered); err != nil {
			_ = rs.Close()
			return nil, err
		}
		q.at = time.UnixMilli(started)
		if ended.Valid {
			q.ended = time.UnixMilli(ended.Int64)
		}
		if entered.Valid {
			q.entered = time.UnixMilli(entered.Int64)
		}
		out = append(out, q)
	}
	return out, errors.Join(rs.Err(), rs.Close())
}

// readFiles reads the "May change" list of each implementer row's ticket,
// and of the ticket of each run in its files. It keeps the runs whose list
// meets the row's, with the two entries that meet, as admission's rule
// files_in_flight meets them. A body that does not read holds no list, and
// returns its UNREAD line.
func readFiles(ctx context.Context, w World, rows []itemRow) []string {
	lists := map[string][]string{}
	var unread []string
	read := func(project, item string) []string {
		key := project + "/" + item
		if l, ok := lists[key]; ok {
			return l
		}
		body, err := w.Body(ctx, project, item)
		if err != nil {
			unread = append(unread, fmt.Sprintf("UNREAD: %s: read the body of %s: %v", project, item, err))
		}
		lists[key] = admission.MayChangeEntries(body)
		return lists[key]
	}
	for i := range rows {
		row := &rows[i]
		if len(row.files) == 0 {
			continue
		}
		var kept []fileRun
		if mine := read(row.item.Project, row.item.ID); len(mine) > 0 {
			for _, f := range row.files {
				var ok bool
				if f.mine, f.theirs, ok = admission.MeetingEntries(mine, read(row.item.Project, f.item)); ok {
					kept = append(kept, f)
				}
			}
		}
		row.files = kept
	}
	return unread
}

// readCapRuns reads each run on the instance of a stage that a limit of
// the row's stage counts, and that had not ended when the row entered.
// Host config is instance scope, so its caps are too, as admission counts
// them.
func readCapRuns(ctx context.Context, tx *sql.Tx, row *itemRow) error {
	var stages []string
	for _, l := range row.limits {
		stages = append(stages, l.Stages...)
	}
	if len(stages) == 0 {
		return nil
	}
	args := []any{row.entered.UnixMilli()}
	for _, s := range stages {
		args = append(args, s)
	}
	rs, err := tx.QueryContext(ctx, `SELECT stage, started_at, ended_at FROM runs WHERE (ended_at IS NULL OR ended_at > ?)
		AND stage IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(stages)), ", ")+`)`, args...)
	if err != nil {
		return err
	}
	for rs.Next() {
		var c capRun
		var started int64
		var ended sql.NullInt64
		if err := rs.Scan(&c.stage, &started, &ended); err != nil {
			_ = rs.Close()
			return err
		}
		c.started = time.UnixMilli(started)
		if ended.Valid {
			c.ended = time.UnixMilli(ended.Int64)
		}
		row.capRuns = append(row.capRuns, c)
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

// readPanels reads each escalation in awaiting_panel, and the triage runs
// of its project.
func readPanels(ctx context.Context, tx *sql.Tx, triage func(project string) (*sched.Cron, error)) ([]panelRow, error) {
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
			ORDER BY ended_at DESC, run_id LIMIT 1`, p.project, config.StageTriage, p.raised.UnixMilli()).Scan(&p.passed, &ended)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		p.passedAt = time.UnixMilli(ended.Int64)
		err = tx.QueryRowContext(ctx, `SELECT run_id FROM runs WHERE project = ? AND stage = ? AND ended_at IS NULL
			ORDER BY started_at, run_id LIMIT 1`, p.project, config.StageTriage).Scan(&p.live)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if p.triage, err = triage(p.project); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// classifyStalls is the stalls part's rule: the rows, the clock and the
// drains of the server in, the reading out.
func classifyStalls(rows stallRows, now time.Time, drains []sched.Drain) Reading {
	var r Reading
	for _, p := range rows.panels {
		key := Key("stalls", fmt.Sprintf("escalation-%d", p.id))
		head := fmt.Sprintf("escalation %d (panel/%d) of %s on %s waits in %s", p.id, p.id, p.project, p.item, escalation.StateAwaitingPanel)
		switch n := fires(p.triage, p.raised, now); {
		case p.passed != "":
			r.Findings = append(r.Findings, Finding{Key: key, Project: p.project, Line: fmt.Sprintf("%s, and triage run %s ended at %s, after its raise at %s (%s)",
				head, p.passed, stamp(p.passedAt), stamp(p.raised), key)})
		case p.live != "":
			r.Notes = append(r.Notes, fmt.Sprintf("%s: triage run %s runs now", head, p.live))
		case n >= stallFires:
			r.Findings = append(r.Findings, Finding{Key: key, Project: p.project, Line: fmt.Sprintf("%s: the triage stage fired twice since its raise at %s, and no triage run ended (%s)",
				head, stamp(p.raised), key)})
		default:
			r.Notes = append(r.Notes, fmt.Sprintf("%s: %s of the triage stage since its raise at %s", head, fireCount(n), stamp(p.raised)))
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
		case row.pause != "":
			why = row.pause
		case row.hold != "":
			why = row.hold + " holds it"
		case row.alert != "":
			why = row.alert + " is open on it"
		case row.run != "":
			why = fmt.Sprintf("run %s started at %s, after it entered at %s", row.run, stamp(row.runAt), stamp(row.entered))
		case row.ci != "":
			why = row.ci
		default:
			c := queueFires(row, now, drains)
			if len(c.drained) > 0 {
				tookOlder = "; the fires that took only older items of its queue started " + tookList(c.drained)
			}
			if c.n < stallFires {
				why = fmt.Sprintf("%s of %s since it entered at %s", fireCount(c.n), row.stage, stamp(row.entered))
				others := c.others(row.stage)
				if tookOlder != "" || len(others) > 0 {
					why += " counts against it" + tookOlder
				}
				for _, o := range others {
					why += "; " + o
				}
			}
		}
		if why != "" {
			r.Notes = append(r.Notes, head+": "+why)
			continue
		}
		r.Findings = append(r.Findings, Finding{Key: key, Project: it.Project, Line: fmt.Sprintf("%s (%s of %s) stalls in %s: two fires of %s since it entered at %s count against it: neither started a run on it, and neither took only older items of its queue%s (%s)",
			it.ID, it.Kind, it.Project, it.Status, row.stage, stamp(row.entered), tookOlder, key)})
	}
	for _, row := range rows.lost {
		if row.leased || row.live {
			continue
		}
		it := row.item
		key := Key("stalls", it.ID+"@"+it.Status)
		r.Findings = append(r.Findings, Finding{Key: key, Project: it.Project, Line: fmt.Sprintf("%s (%s of %s) is lost in %s: no lease holds it, and no live run names it (%s)",
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

// fireTally is what queueFires counts: n fires against the row, and why
// each other fire does not count.
type fireTally struct {
	n       int
	drained []queueRun    // the runs of the fires that took only older items
	held    []heldFire    // the fires that an older item's run refused with the stage mutex
	refused []sched.Drain // the drains that refused a fire
	full    string        // the newest fire that a full limit refused
	files   string        // the newest fire that rule files_in_flight refused
}

// heldFire is one fire that a run on an older item of the queue refused,
// since it held the stage mutex.
type heldFire struct {
	holder queueRun
	fire   time.Time
}

// others says why the fires that neither counted nor took only older items
// did not count.
func (c fireTally) others(stage string) []string {
	var out []string
	if len(c.held) > 0 {
		var fires []string
		for _, h := range c.held {
			fires = append(fires, fmt.Sprintf("run %s on %s at the fire of %s", h.holder.run, h.holder.item, stamp(h.fire)))
		}
		out = append(out, fmt.Sprintf("a run on an older item of its queue held the %s mutex: %s", stage, strings.Join(fires, ", ")))
	}
	if c.full != "" {
		out = append(out, c.full)
	}
	if c.files != "" {
		out = append(out, c.files)
	}
	for _, d := range c.refused {
		if d.To.IsZero() {
			out = append(out, fmt.Sprintf("the server drains since %s and admits no run, so no fire since then counts against it", stamp(d.From)))
		} else {
			out = append(out, fmt.Sprintf("the server drained from %s until its restart at %s and admitted no run, so no fire then counts against it",
				stamp(d.From), stamp(d.To)))
		}
	}
	return out
}

// queueFires counts the fires of the row's stage after it entered and at
// or before now that count against it, up to stallFires, and says why the
// other fires did not count. A fire counts unless the runs that started
// from it until the next fire all took items that entered the queue before
// the row did: that is the queue draining. An item that no transition
// dates counts as younger. A fire of a mutex stage while a run on an older
// item held the mutex is the queue draining too. A fire that started no
// run while a limit of the stage was full does not count either: full says
// so for the newest such fire. A fire during a drain never counts: a drain
// lasts from its start until the server restarts, and admits no run. A
// schedule that no date holds never fires.
func queueFires(row itemRow, now time.Time, drains []sched.Drain) (c fireTally) {
	next, err := row.cron.Next(row.entered)
	for c.n < stallFires && err == nil && !next.After(now) {
		fire := next
		next, err = row.cron.Next(fire)
		if d, ok := drainAt(drains, fire); ok {
			if !slices.Contains(c.refused, d) {
				c.refused = append(c.refused, d)
			}
			if d.To.IsZero() {
				break // the drain is on, and holds every later fire too
			}
			continue
		}
		if line := filesHeld(row, fire); line != "" {
			c.files = line + " at the fire of " + stamp(fire)
			continue
		}
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
		if younger {
			c.n++
			continue
		}
		if len(took) > 0 {
			c.drained = append(c.drained, took...)
			continue
		}
		if holder, ok := mutexHolder(row, fire); ok {
			c.held = append(c.held, heldFire{holder: holder, fire: fire})
			continue
		}
		if line := capHeld(row, fire); line != "" {
			c.full = line + " at the fire of " + stamp(fire)
			continue
		}
		c.n++
	}
	return c
}

// drainAt returns the drain that the fire fell in: at or after its start,
// and before the restart that ended it.
func drainAt(drains []sched.Drain, fire time.Time) (sched.Drain, bool) {
	for _, d := range drains {
		if !fire.Before(d.From) && (d.To.IsZero() || fire.Before(d.To)) {
			return d, true
		}
	}
	return sched.Drain{}, false
}

// mutexHolder returns the run on an older item of the row's queue that held
// the stage mutex at the fire: it started before the fire, and had not
// ended at it. A run on a younger item, or on an item that no transition
// dates, holds none that counts.
func mutexHolder(row itemRow, fire time.Time) (queueRun, bool) {
	for _, q := range row.mutex {
		if q.at.Before(fire) && (q.ended.IsZero() || q.ended.After(fire)) && !q.entered.IsZero() && q.entered.Before(row.entered) {
			return q, true
		}
	}
	return queueRun{}, false
}

// filesHeld names the implementer run in the row's files that was live at
// the fire: it started before the fire, and had not ended at it. Admission
// refused the row then with rule files_in_flight. Empty when none was.
func filesHeld(row itemRow, fire time.Time) string {
	for _, f := range row.files {
		if f.at.Before(fire) && (f.ended.IsZero() || f.ended.After(fire)) {
			return fmt.Sprintf("admission refused it with rule files_in_flight: `%s` meets `%s` of %s, which implementer run %s held",
				f.mine, f.theirs, f.item, f.run)
		}
	}
	return ""
}

// capHeld checks each limit of the row's stage at the fire, as admission
// does, and says which one its live runs fill. A run is live at the fire
// when it started before the fire and had not ended at it. Empty when
// every limit has room.
func capHeld(row itemRow, fire time.Time) string {
	for _, l := range row.limits {
		live := 0
		for _, c := range row.capRuns {
			if slices.Contains(l.Stages, c.stage) && c.started.Before(fire) && (c.ended.IsZero() || c.ended.After(fire)) {
				live++
			}
		}
		switch {
		case live < l.Cap:
		case l.Name == row.stage:
			return fmt.Sprintf("%s holds its cap (%d of %d live)", row.stage, live, l.Cap)
		default:
			return fmt.Sprintf("%s holds the cap of the %s pool (%d of %d live)", row.stage, l.Name, live, l.Cap)
		}
	}
	return ""
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
