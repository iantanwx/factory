package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// CoordinatorConfig starts the coordinator session of each project whose
// default branch holds a project config (R24, ADR 0015).
type CoordinatorConfig struct {
	Harness agenthost.Harness
	Model   string
	Effort  string
}

// CoordinatorStage names the coordinator in its brief and runbook.
const CoordinatorStage = "coordinator"

// AlertCoordinatorCheckoutMissing names a project whose coordinator
// checkout the server made and no longer finds. The inbox routes it to the
// loop project's coordinator, as AlertCoordinator.
const AlertCoordinatorCheckoutMissing = "coordinator_checkout_missing"

// coordinatorCheckout names the checkout of each project's coordinator.
const coordinatorCheckout = "loop-coordinator"

// The schedule rows of the coordinators (spec factorio-1edo, Data).
const (
	// oldKind is the one coordinator row of a release before ADR 0015,
	// with project ''. Only the old session's row has it.
	oldKind = "coordinator"
	// homeKind is the coordinator row of one project, keyed by the project.
	homeKind = "home_coordinator"
	// notifyKind and homeNotifyKind are inbox.NotifyKind and
	// inbox.HomeNotifyKind, the doorbell's cursors. sched cannot import
	// inbox, which imports sched.
	notifyKind     = "inbox_notify"
	homeNotifyKind = "home_notify"
	// ringKind is overwatch.RingKind, the last ring to each destination.
	// sched cannot import overwatch, which imports sched.
	ringKind = "coordinator_ring"
)

// CoordinatorState is one coordinator session the server keeps.
type CoordinatorState struct {
	RunID string `json:"run_id,omitempty"`
	// Project is the project the coordinator serves, and whose repository
	// the checkout clones.
	Project  string             `json:"project,omitempty"`
	Checkout string             `json:"checkout,omitempty"`
	Session  *agenthost.Session `json:"session,omitempty"`
	// Release is the server release that started the session.
	Release string `json:"release,omitempty"`
	// NotifiedRelease is the later release that the session was told
	// about, so each release messages a live session once.
	NotifiedRelease string `json:"notified_release,omitempty"`
	// Made marks a checkout the server made, so a missing directory is an
	// alert, and not a first start.
	Made bool `json:"made,omitempty"`
	// Fresh makes the next session start fresh, with the work a previous
	// session left open in its prompt. A session that starts clears it.
	Fresh bool `json:"fresh,omitempty"`
	// Revoked marks a session whose token a rollback's hand-back revoked.
	// The tick closes that session and starts the next one fresh.
	Revoked bool `json:"revoked,omitempty"`
}

// LeftOpen lists the work a previous coordinator of project left open,
// one line for each piece.
type LeftOpen func(ctx context.Context, project string) ([]string, error)

// SetLeftOpen sets the list of the left-open work. The server sets it
// before the scheduler ticks.
func (s *Scheduler) SetLeftOpen(f LeftOpen) { s.leftOpen = f }

// Topology is the coordinators of the instance now: the loop project, each
// project with a home_coordinator row, and the old session's project while
// its row stands.
func (s *Scheduler) Topology() coord.Topology {
	old, homes, err := s.coordinatorRows(context.Background())
	if err != nil {
		s.log.Error("read the coordinators", "err", err)
	}
	return s.topology(old, homes)
}

func (s *Scheduler) topology(old *CoordinatorState, homes map[string]CoordinatorState) coord.Topology {
	ps, _ := s.cfg.Projects()
	return topologyOf(old, homes, ps)
}

func topologyOf(old *CoordinatorState, homes map[string]CoordinatorState, ps []config.Project) coord.Topology {
	t := coord.Topology{Homes: map[string]bool{}}
	for p := range homes {
		t.Homes[p] = true
	}
	if old != nil {
		t.Old = old.Project
	}
	t.Loop, _ = coord.LoopProject(ps)
	return t
}

// Coordinators holds each coordinator by the project it serves, the old
// session's too.
func (s *Scheduler) Coordinators() map[string]CoordinatorState {
	old, homes, err := s.coordinatorRows(context.Background())
	if err != nil {
		s.log.Error("read the coordinators", "err", err)
	}
	if old != nil {
		homes[old.Project] = *old
	}
	return homes
}

// Session names the session that serves project: its own coordinator's,
// or the old session's for the old session's project. No session serves
// "", so on a host with no loop project the instance-wide work waits in
// the inbox for the host to read.
func (s *Scheduler) Session(project string) (agenthost.RunID, bool) {
	if project == "" {
		return agenthost.RunID{}, false
	}
	old, homes, err := s.coordinatorRows(context.Background())
	if err != nil {
		return agenthost.RunID{}, false
	}
	cs := homes[project]
	if old != nil && old.Project == project {
		cs = *old
	}
	if cs.Session == nil {
		return agenthost.RunID{}, false
	}
	return cs.Session.ID, true
}

// coordinatorRows reads the old row, nil when the store holds none, and
// each home row by its project.
func (s *Scheduler) coordinatorRows(ctx context.Context) (*CoordinatorState, map[string]CoordinatorState, error) {
	var old *CoordinatorState
	var homes map[string]CoordinatorState
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, coordinatorRowsQuery, oldKind, homeKind)
		if err != nil {
			return err
		}
		old, homes, err = scanCoordinators(rs)
		return err
	})
	return old, homes, err
}

const coordinatorRowsQuery = `SELECT kind, payload FROM schedule WHERE kind IN (?, ?)`

// scanCoordinators reads the rows of coordinatorRowsQuery, and closes them.
func scanCoordinators(rs *sql.Rows) (*CoordinatorState, map[string]CoordinatorState, error) {
	defer func() { _ = rs.Close() }()
	var old *CoordinatorState
	homes := map[string]CoordinatorState{}
	for rs.Next() {
		var kind, raw string
		var cs CoordinatorState
		if err := rs.Scan(&kind, &raw); err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal([]byte(raw), &cs); err != nil {
			return nil, nil, err
		}
		if kind == oldKind {
			old = &cs
		} else {
			homes[cs.Project] = cs
		}
	}
	return old, homes, rs.Err()
}

// saveCoordinator writes cs into its row: the old row, or its project's
// home row.
func (s *Scheduler) saveCoordinator(ctx context.Context, kind string, cs CoordinatorState) error {
	raw, err := json.Marshal(cs)
	if err != nil {
		return err
	}
	project := cs.Project
	if kind == oldKind {
		project = ""
	}
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE schedule SET payload = ?, updated_at = ? WHERE kind = ? AND project = ?`,
			string(raw), tx.Now().UnixMilli(), kind, project)
		return err
	})
}

// createHome writes the home row of cs.Project, and its doorbell cursor in
// the same transaction. The cursor starts at the cursor of from, the
// destination that got the project's entries until now, so no entry rings
// twice (C41). A cursor row that exists stays as it is. The hand-over also
// deletes the old row.
func (s *Scheduler) createHome(ctx context.Context, cs CoordinatorState, from string, handOver bool) error {
	raw, err := json.Marshal(cs)
	if err != nil {
		return err
	}
	return s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		ms := tx.Now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at) SELECT ?, ?, ?, ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM schedule WHERE kind = ? AND project = ?)`,
			cs.Project, homeKind, string(raw), ms, ms, homeKind, cs.Project); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO schedule (project, kind, due_at, created_at, updated_at)
			SELECT ?, ?, at, ?, ? FROM (SELECT coalesce(
				(SELECT due_at FROM schedule WHERE kind = ? AND project = ?),
				(SELECT due_at FROM schedule WHERE kind = ?)) AS at)
			WHERE at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM schedule WHERE kind = ? AND project = ?)`,
			cs.Project, homeNotifyKind, ms, ms, homeNotifyKind, from, notifyKind, homeNotifyKind, cs.Project); err != nil {
			return err
		}
		if !handOver {
			return nil
		}
		_, err := tx.Exec(`DELETE FROM schedule WHERE kind = ?`, oldKind)
		return err
	})
}

// ensureCoordinators keeps one coordinator session for each registered
// project whose default branch holds a project config (spec factorio-1edo,
// Approach, "Sessions"). The old session of an earlier release keeps its
// project until it ends. Then its project's coordinator starts in its
// checkout, or, with no config, its checkout goes. One project's failure
// raises its alert, and the tick continues with the next project.
func (s *Scheduler) ensureCoordinators(ctx context.Context) {
	cc := s.cfg.Coordinator
	if cc == nil || s.Draining() {
		return
	}
	ps, err := s.cfg.Projects()
	if err != nil {
		s.log.ErrorContext(ctx, "list the projects for the coordinators", "err", err)
		return
	}
	old, homes, err := s.coordinatorRows(ctx)
	if err == nil && old != nil {
		s.tendOld(ctx, cc, *old, ps)
		old, homes, err = s.coordinatorRows(ctx)
	}
	if err != nil {
		s.log.ErrorContext(ctx, "read the coordinators", "err", err)
		return
	}
	loop, _ := coord.LoopProject(ps)
	for _, p := range ps {
		cs, ok := homes[p.ID]
		if !ok {
			if old != nil && old.Project == p.ID {
				continue
			}
			has, err := s.hasConfig(ctx, p)
			if err != nil {
				s.startFailed(ctx, p.ID, err)
			}
			if !has {
				continue
			}
			cs = CoordinatorState{Project: p.ID}
			if err := s.createHome(ctx, cs, s.topology(old, homes).Dest(p.ID, false), false); err != nil {
				s.log.ErrorContext(ctx, "store the coordinator", "project", p.ID, "err", err)
				continue
			}
			homes[p.ID] = cs
		}
		s.tendHome(ctx, cc, cs, p, p.ID == loop)
	}
}

// tendOld acts on the old session: it closes it when its project is no
// longer registered, tells it about this release while it lives, and runs
// the hand-over once it ended.
func (s *Scheduler) tendOld(ctx context.Context, cc *CoordinatorConfig, old CoordinatorState, ps []config.Project) {
	i := slices.IndexFunc(ps, func(p config.Project) bool { return p.ID == old.Project })
	if i < 0 {
		s.endSession(ctx, old)
		s.removeCheckout(ctx, old.Project, old.Checkout)
		s.deleteOld(ctx)
		s.log.WarnContext(ctx, "the old coordinator's project is not registered; closed it", "project", old.Project, "run", old.RunID)
		return
	}
	switch v, moved := s.settleCoordinator(ctx, old.Session); v {
	case sessionLive:
		s.tellCoordinatorRelease(ctx, cc, oldKind, &old)
		return
	case sessionMoved:
		s.rebind(ctx, oldKind, old, moved)
		return
	case sessionUnknown:
		return
	}
	s.endSession(ctx, old)
	has, err := s.hasConfig(ctx, ps[i])
	if err != nil {
		s.startFailed(ctx, old.Project, err)
		return
	}
	if !has {
		s.removeCheckout(ctx, old.Project, old.Checkout)
		s.deleteOld(ctx)
		s.log.InfoContext(ctx, "the old coordinator ended, and its project has no config", "project", old.Project, "run", old.RunID)
		return
	}
	// While the old session lived, it was the destination of its project's
	// entries, so the new cursor starts at the project's own.
	cs := CoordinatorState{Project: old.Project, Checkout: old.Checkout, Made: true, Fresh: true}
	if err := s.createHome(ctx, cs, old.Project, true); err != nil {
		s.log.ErrorContext(ctx, "hand the old coordinator's project over", "project", old.Project, "err", err)
		return
	}
	s.log.InfoContext(ctx, "the old coordinator ended; its project's coordinator starts in its checkout", "project", old.Project, "run", old.RunID)
}

// tendHome keeps p's coordinator session: it tells a live session about
// this release, rebinds a moved one, and starts the next one when the
// session ended. A failed start raises an alert that names p.
func (s *Scheduler) tendHome(ctx context.Context, cc *CoordinatorConfig, cs CoordinatorState, p config.Project, loop bool) {
	if cs.Revoked {
		// A rollback's hand-back revoked the session's token, so its verbs
		// refuse. Its next session starts fresh.
		s.endSession(ctx, cs)
		cs.Session, cs.Revoked = nil, false
		s.log.WarnContext(ctx, "closed the coordinator session that a rollback revoked", "project", p.ID, "run", cs.RunID)
	}
	if cs.Session != nil {
		switch v, moved := s.settleCoordinator(ctx, cs.Session); v {
		case sessionLive:
			s.tellCoordinatorRelease(ctx, cc, homeKind, &cs)
			return
		case sessionMoved:
			s.rebind(ctx, homeKind, cs, moved)
			return
		case sessionUnknown:
			return
		}
		s.endSession(ctx, cs)
		cs.Session = nil
		s.log.WarnContext(ctx, "the coordinator session ended; starting another", "project", p.ID, "run", cs.RunID)
	}
	var reserved *brief.ReservedError
	if err := s.startCoordinator(ctx, cc, &cs, p, loop); errors.As(err, &reserved) {
		s.raise(ctx, config.Alert{Project: p.ID, Kind: AlertConfigConflict, DedupeKey: AlertConfigConflict + ":" + stageKey(p.ID, CoordinatorStage),
			Subject: "the coordinator session did not start", Body: ReservedRefusal("", reserved).Hint})
	} else if err != nil {
		s.startFailed(ctx, p.ID, err)
	}
	if err := s.saveCoordinator(ctx, homeKind, cs); err != nil {
		s.log.ErrorContext(ctx, "store the coordinator", "project", p.ID, "err", err)
	}
}

// HandBack leaves one coordinator for the release before ADR 0015, in loop
// rollback while the service is stopped (spec factorio-1edo, Approach,
// "Rollback"). That release reads only the coordinator row. A coordinator
// row that stands stays. Otherwise the home row of the first registered
// project becomes the coordinator row, and its token names no project. The
// token of each other home row is revoked, and the row is Revoked and
// Fresh. inbox_notify moves to the lowest cursor of the topology, so the
// earlier release rings each entry that a destination has not rung for.
func HandBack(ctx context.Context, st *store.Store, ps []config.Project) error {
	var revoke []string
	err := st.Write(ctx, func(tx *store.Tx) error {
		rs, err := tx.Query(coordinatorRowsQuery, oldKind, homeKind)
		if err != nil {
			return err
		}
		old, homes, err := scanCoordinators(rs)
		if err != nil {
			return err
		}
		if err := lowestCursor(tx, topologyOf(old, homes, ps)); err != nil {
			return err
		}
		ms := tx.Now().UnixMilli()
		var first string
		if len(ps) > 0 {
			first = ps[0].ID
		}
		// The earlier release names the first registered project as its
		// coordinator's, so it keeps this session.
		if cs, ok := homes[first]; ok && old == nil {
			if _, err := tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at)
				SELECT '', ?, payload, ?, ? FROM schedule WHERE kind = ? AND project = ?`, oldKind, ms, ms, homeKind, cs.Project); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM schedule WHERE kind = ? AND project = ?`, homeKind, cs.Project); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE run_tokens SET project = '' WHERE run_id = ?`, cs.RunID); err != nil {
				return err
			}
			delete(homes, cs.Project)
		}
		for _, cs := range homes {
			cs.Revoked, cs.Fresh = true, true
			raw, err := json.Marshal(cs)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE schedule SET payload = ?, updated_at = ? WHERE kind = ? AND project = ?`,
				string(raw), ms, homeKind, cs.Project); err != nil {
				return err
			}
			if cs.RunID != "" {
				revoke = append(revoke, cs.RunID)
			}
		}
		_, err = tx.Exec(`DELETE FROM schedule WHERE kind = ? OR (kind = ? AND project != '')`, homeNotifyKind, ringKind)
		return err
	})
	if err != nil {
		return fmt.Errorf("sched: hand the coordinator back: %w", err)
	}
	// A rerun after a failed revoke finds each row Revoked, and revokes
	// its token again.
	tokens := &rpc.Tokens{Store: st}
	for _, run := range revoke {
		if _, err := tokens.Revoke(ctx, run); err != nil {
			return err
		}
	}
	return nil
}

// lowestCursor moves inbox_notify to the lowest cursor over every
// destination of t. A destination with no home_notify row is at the
// inbox_notify value, so then the value stays. With no inbox_notify row, it
// writes none.
func lowestCursor(tx *store.Tx, t coord.Topology) error {
	var old sql.NullInt64
	if err := tx.QueryRow(`SELECT due_at FROM schedule WHERE kind = ?`, notifyKind).Scan(&old); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	dests := []string{t.Loop}
	for p := range t.Homes {
		dests = append(dests, p)
	}
	if t.Old != "" {
		dests = append(dests, t.Old)
	}
	var lowest sql.NullInt64
	for _, d := range dests {
		var at sql.NullInt64
		if err := tx.QueryRow(`SELECT due_at FROM schedule WHERE kind = ? AND project = ?`, homeNotifyKind, d).Scan(&at); errors.Is(err, sql.ErrNoRows) {
			at = old
		} else if err != nil {
			return err
		}
		if !at.Valid {
			return nil
		}
		if !lowest.Valid || at.Int64 < lowest.Int64 {
			lowest = at
		}
	}
	if lowest == old {
		return nil
	}
	ms := tx.Now().UnixMilli()
	if old.Valid {
		_, err := tx.Exec(`UPDATE schedule SET due_at = ?, updated_at = ? WHERE kind = ?`, lowest.Int64, ms, notifyKind)
		return err
	}
	_, err := tx.Exec(`INSERT INTO schedule (project, kind, due_at, created_at, updated_at) VALUES ('', ?, ?, ?, ?)`,
		notifyKind, lowest.Int64, ms, ms)
	return err
}

// startFailed raises the alert of project's coordinator that did not
// start. Each project holds its own open alert.
func (s *Scheduler) startFailed(ctx context.Context, project string, err error) {
	s.raise(ctx, config.Alert{Project: project, Kind: AlertCoordinator, DedupeKey: AlertCoordinator + ":" + project,
		Subject: "the coordinator session of " + project + " did not start", Body: err.Error()})
}

// sessionVerdict is what the agent host says of a stored session.
type sessionVerdict int

const (
	sessionEnded sessionVerdict = iota
	sessionLive
	// sessionMoved is a live session under a new handle.
	sessionMoved
	// sessionUnknown is a session whose liveness reads unknown, and that
	// no complete inventory proves gone. It is left alone.
	sessionUnknown
)

// settleCoordinator reads a stored session. A session whose liveness reads
// unknown is left alone, unless a complete inventory proves it gone, or
// lists it under a new handle with the same incarnation and checkout
// (Inventory.Rebound), or, with its incarnation gone, lists one agent of
// its harness in its checkout (Inventory.ReboundToAgent). Then the moved
// session takes that row. No session is an ended one.
func (s *Scheduler) settleCoordinator(ctx context.Context, sess *agenthost.Session) (sessionVerdict, agenthost.Session) {
	if sess == nil {
		return sessionEnded, agenthost.Session{}
	}
	st, err := s.cfg.Agents.Settle(ctx, sess.ID)
	if err == nil && st.Liveness == agenthost.Live {
		return sessionLive, agenthost.Session{}
	}
	if err == nil && st.Liveness == agenthost.Exited {
		return sessionEnded, agenthost.Session{}
	}
	inv, err := s.cfg.Agents.Inventory(ctx)
	if err != nil {
		return sessionUnknown, agenthost.Session{}
	}
	moved, ok := inv.Rebound(*sess)
	if !ok {
		// The incarnation is gone. The one agent of the harness in the
		// checkout is the coordinator (F-30).
		moved, ok = inv.ReboundToAgent(*sess)
	}
	if ok {
		return sessionMoved, moved
	}
	if inv.Lookup(*sess) != agenthost.Absent {
		return sessionUnknown, agenthost.Session{}
	}
	return sessionEnded, agenthost.Session{}
}

// rebind stores the new handle of a moved live session (F-17). The
// doorbell rings the stored handle, so it follows.
func (s *Scheduler) rebind(ctx context.Context, kind string, cs CoordinatorState, moved agenthost.Session) {
	s.log.WarnContext(ctx, "the coordinator session moved to a new handle; rebinding",
		"project", cs.Project, "run", cs.RunID, "from", cs.Session.ID.String(), "to", moved.ID.String())
	cs.Session = &moved
	if err := s.saveCoordinator(ctx, kind, cs); err != nil {
		s.log.ErrorContext(ctx, "store the coordinator", "project", cs.Project, "err", err)
	}
}

// endSession closes a coordinator's session and revokes its token.
func (s *Scheduler) endSession(ctx context.Context, cs CoordinatorState) {
	if cs.Session != nil {
		_ = s.cfg.Agents.Close(ctx, cs.Session.ID)
	}
	if cs.RunID == "" {
		return
	}
	if _, err := s.cfg.Tokens.Revoke(ctx, cs.RunID); err != nil {
		s.log.ErrorContext(ctx, "revoke the coordinator token", "project", cs.Project, "err", err)
	}
}

// removeCheckout removes a coordinator checkout that no session takes. A
// checkout left behind would refuse the next coordinator of the project. A
// terminal still open in it keeps it, and the log says so.
func (s *Scheduler) removeCheckout(ctx context.Context, project, path string) {
	if path == "" {
		return
	}
	s.checkoutMu.Lock()
	err := s.cfg.Checkouts(project).RemoveCheckout(ctx, path)
	s.checkoutMu.Unlock()
	if err != nil {
		s.log.WarnContext(ctx, "remove the old coordinator checkout", "checkout", path, "err", err)
	}
}

func (s *Scheduler) deleteOld(ctx context.Context) {
	if err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`DELETE FROM schedule WHERE kind = ?`, oldKind)
		return err
	}); err != nil {
		s.log.ErrorContext(ctx, "delete the old coordinator row", "err", err)
	}
}

// hasConfig reports whether the head the remote holds for p's default
// branch holds .loop/config.yaml. It fetches that head on each call and
// reads no cached config, so a config that lands starts the coordinator at
// the next tick (C18).
func (s *Scheduler) hasConfig(ctx context.Context, p config.Project) (bool, error) {
	sha, err := s.cfg.BaseHead(ctx, p.ID, p.BaseBranch())
	if err != nil {
		return false, err
	}
	if _, err := gitx.Run(ctx, p.Path, "cat-file", "-e", sha+":"+config.ProjectConfigPath); err == nil {
		return true, nil
	}
	if _, err := gitx.Run(ctx, p.Path, "cat-file", "-e", sha+"^{commit}"); err != nil {
		return false, fmt.Errorf("sched: read the head %s of project %s: %w", sha, p.ID, err)
	}
	return false, nil
}

// tellCoordinatorRelease tells a live session that started under another
// release about this one, once. An upgrade never closes a live session: it
// holds the operator's conversation, its subagents and writes in flight.
// The session runs on under the release that started it, and the next one
// starts under this release once it ends. A refused message is not
// recorded, so the next tick sends it again.
func (s *Scheduler) tellCoordinatorRelease(ctx context.Context, cc *CoordinatorConfig, kind string, cs *CoordinatorState) {
	if cs.Release == s.cfg.Release || cs.NotifiedRelease == s.cfg.Release {
		return
	}
	// The runbook the message names is the new release's.
	if err := brief.RefreshPack(ctx, cs.Checkout, cc.Harness, s.coordinatorPack()); err != nil {
		s.log.WarnContext(ctx, "write the new pack into the coordinator's checkout", "checkout", cs.Checkout, "err", err)
	}
	text := fmt.Sprintf("loop: the server now runs release %s, and you started under %s. Read %s again. "+
		"The server starts the next coordinator under %s when this session ends.",
		s.cfg.Release, cs.Release, brief.Runbook(cc.Harness, CoordinatorStage), s.cfg.Release)
	if err := s.cfg.Agents.Notify(ctx, cs.Session.ID, text); err != nil {
		s.log.WarnContext(ctx, "tell the coordinator about the new release", "run", cs.RunID, "err", err)
		return
	}
	cs.NotifiedRelease = s.cfg.Release
	if err := s.saveCoordinator(ctx, kind, *cs); err != nil {
		s.log.ErrorContext(ctx, "store the coordinator", "err", err)
	}
	s.log.InfoContext(ctx, "told the coordinator about the new release", "run", cs.RunID, "from", cs.Release, "to", s.cfg.Release)
}

// startCoordinator starts p's next coordinator session. It makes the
// loop-coordinator checkout from the head of p's default branch when cs has
// none. The token and the brief name p, and a Fresh start's prompt names
// the work a previous session left open.
func (s *Scheduler) startCoordinator(ctx context.Context, cc *CoordinatorConfig, cs *CoordinatorState, p config.Project, loop bool) error {
	base := p.BaseBranch()
	if cs.Checkout == "" {
		spec, err := BaseCheckout(ctx, s.cfg.BaseHead, p.ID, base, coordinatorCheckout)
		if err != nil {
			return err
		}
		s.checkoutMu.Lock()
		co, err := s.cfg.Checkouts(p.ID).CreateCheckout(ctx, spec)
		s.checkoutMu.Unlock()
		if err != nil {
			return fmt.Errorf("sched: the coordinator's checkout: %w", err)
		}
		cs.Checkout, cs.Made = co.Path, true
		if err := s.saveCoordinator(ctx, homeKind, *cs); err != nil {
			return err
		}
	}
	var left []string
	if cs.Fresh && s.leftOpen != nil {
		var err error
		if left, err = s.leftOpen(ctx, p.ID); err != nil {
			return fmt.Errorf("sched: list the work the last coordinator left open: %w", err)
		}
	}
	model := cc.Model
	if model == "" {
		model = DefaultModels[cc.Harness]
	}
	run := "coordinator-" + s.cfg.NewRunID()
	b := brief.Brief{RunID: run, Project: p.ID, LoopProject: loop, Stage: CoordinatorStage, Role: CoordinatorStage,
		Harness: string(cc.Harness), Model: model, BaseBranch: base, Secrets: brief.Secrets{Present: []string{}, Missing: []string{}},
		LeftOpen: left}
	res, err := brief.Inject(ctx, brief.Injection{Checkout: cs.Checkout, Harness: cc.Harness, Pack: s.coordinatorPack(), Brief: b})
	if err != nil {
		return err
	}
	if _, err := s.cfg.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: run, Kind: cli.PrincipalCoordinator,
		Project: p.ID, Checkout: cs.Checkout, Release: s.cfg.Release}); err != nil {
		return err
	}
	sess, err := s.cfg.Agents.Start(ctx, agenthost.StartSpec{Kind: agenthost.KindCoordinator, Checkout: cs.Checkout,
		Harness: cc.Harness, Model: model, Effort: cc.Effort, PromptFile: res.Prompt, Title: "loop coordinator " + p.ID})
	if err != nil {
		_, _ = s.cfg.Tokens.Revoke(ctx, run)
		return fmt.Errorf("sched: start the coordinator: %w", err)
	}
	cs.RunID, cs.Session, cs.Release, cs.NotifiedRelease, cs.Fresh = run, &sess, s.cfg.Release, "", false
	return nil
}

// coordinatorPack is the pack the coordinator holds: no conditional skill.
// A condition reads one project's config, and the loop project's
// coordinator serves the instance too.
func (s *Scheduler) coordinatorPack() brief.Pack {
	return Select(s.cfg.Pack, nil, Scope{})
}
