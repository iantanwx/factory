package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// Alert kinds the machine raises. The inbox reads the alerts table.
const (
	AlertDivergence = "bead_divergence" // a bead changed outside the server
	AlertEpochAhead = "epoch_ahead"     // a bead held a higher epoch than SQLite
)

// ErrBeadsUnavailable is a reconcile that could not reach beads. The
// server then does not start (R17), and its supervisor starts it again.
var ErrBeadsUnavailable = errors.New("machine: beads unavailable during reconcile")

// Reconcile finishes every write a crash interrupted, before the socket
// binds (R17, KTD6). For each project it:
//
//  1. replays every unapplied intent in intent order: an intent the bead's
//     sequence reached is applied, a stale epoch is LEASE_NOT_HELD, a moved
//     status is STATUS_CHANGED with a divergence alert, and any other
//     intent is written now;
//  2. raises each SQLite epoch and intent sequence to the bead's, with an
//     alert when the bead's epoch was ahead;
//  3. checks each item's last applied intent against its bead: a write
//     Dolt lost is written again through the status check, and a bead
//     that changed outside the server raises a divergence alert and is
//     never overwritten;
//  4. completes each epic in execution whose children are all terminal
//     (CompleteEpics), which a crash between the two writes left open.
//
// It holds each item's lock while it works on the item, so it may also
// run while the server serves, for example after a Dolt restart.
func (m *Machine) Reconcile(ctx context.Context) error {
	projects, err := m.projects(ctx)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := m.reconcileProject(ctx, p); err != nil {
			return fmt.Errorf("machine: reconcile %s: %w", p, err)
		}
	}
	return nil
}

// projects lists every project that may hold work. The host and global
// scopes' rows hold secrets only (secrets.ensureScope creates them), and
// they have no beads workspace, so they are never one.
func (m *Machine) projects(ctx context.Context) ([]string, error) {
	var out []string
	err := m.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM projects WHERE id NOT IN (?, ?) UNION SELECT project FROM intents
			UNION SELECT project FROM leases ORDER BY 1`, config.HostScope, config.GlobalScope)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var p string
			if err := rows.Scan(&p); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func (m *Machine) reconcileProject(ctx context.Context, project string) error {
	bc, err := m.cfg.Beads(project)
	if err != nil {
		return err
	}
	var pending []store.Intent
	err = m.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		all, err := tx.UnappliedIntents()
		for _, in := range all {
			if in.Project == project {
				pending = append(pending, in)
			}
		}
		return err
	})
	if err != nil {
		return err
	}
	for _, in := range pending {
		unlock := m.lock(in.Project, in.Item)
		_, settled := m.drive(ctx, bc, in, nil)
		unlock()
		if !settled {
			return fmt.Errorf("%w: intent %d on %s", ErrBeadsUnavailable, in.ID, in.Item)
		}
	}

	beads, err := bc.List(ctx, beadsx.Filter{})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBeadsUnavailable, err)
	}
	last, err := m.lastApplied(ctx, project)
	if err != nil {
		return err
	}
	for _, is := range beads {
		if _, ok := kindOfType(is.Type); !ok {
			continue
		}
		unlock := m.lock(project, is.ID)
		err := m.reconcileItem(ctx, bc, project, is, last[is.ID])
		unlock()
		if err != nil {
			return err
		}
		delete(last, is.ID)
	}
	for item := range last {
		m.divergence(ctx, project, item, fmt.Sprintf("%s has applied transitions, and beads holds no such item", item))
	}
	// A crash between a child's terminal write and its epic's complete
	// write left the epic in execution. A fault here does not stop the
	// start: each dependency-advance tick runs the sweep again.
	if err := m.CompleteEpics(ctx, project); err != nil {
		m.log.WarnContext(ctx, "complete finished epics; dependency-advance tries again", "project", project, "err", err)
	}
	return nil
}

// lastApplied maps each item to its last applied intent.
func (m *Machine) lastApplied(ctx context.Context, project string) (map[string]store.Intent, error) {
	out := map[string]store.Intent{}
	err := m.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		rows, err := tx.Query(`SELECT max(id) FROM intents WHERE project = ? AND state = 'applied' GROUP BY item`, project)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			in, err := tx.GetIntent(id)
			if err != nil {
				return err
			}
			out[in.Item] = in
		}
		return nil
	})
	return out, err
}

func (m *Machine) reconcileItem(ctx context.Context, bc beadsx.Client, project string, is beadsx.Issue, last store.Intent) error {
	seq, err := is.IntentSeq()
	if err != nil {
		m.divergence(ctx, project, is.ID, fmt.Sprintf("%s holds an intent sequence the server cannot read", is.ID))
		return nil
	}
	be, err := beadEpoch(is)
	if err != nil {
		m.divergence(ctx, project, is.ID, fmt.Sprintf("%s holds an epoch the server cannot read", is.ID))
		return nil
	}
	var ahead int64 = -1
	err = m.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.RaiseItemSeq(project, is.ID, seq); err != nil {
			return err
		}
		l, err := readLease(tx.QueryRow, project, is.ID)
		if err != nil {
			return err
		}
		if be <= l.Epoch {
			return nil
		}
		ahead = l.Epoch
		_, err = tx.Exec(`INSERT INTO leases (project, item, epoch) VALUES (?, ?, ?)
			ON CONFLICT (project, item) DO UPDATE SET epoch = max(epoch, excluded.epoch)`, project, is.ID, be)
		return err
	})
	if err != nil {
		return err
	}
	if ahead >= 0 {
		m.alert(ctx, project, AlertEpochAhead, is.ID,
			fmt.Sprintf("%s held epoch %d in beads and %d in SQLite; SQLite now holds %d", is.ID, be, ahead, be))
	}
	if last.ID == 0 {
		return nil
	}
	switch {
	case seq > last.Seq:
		m.divergence(ctx, project, is.ID,
			fmt.Sprintf("%s holds intent sequence %d, and the server applied up to %d", is.ID, seq, last.Seq))
	case seq == last.Seq && is.Status != last.TargetStatus:
		m.divergence(ctx, project, is.ID,
			fmt.Sprintf("%s is %s in beads, and the server's last write left it %s", is.ID, is.Status, last.TargetStatus))
	case seq < last.Seq:
		m.rewrite(ctx, bc, project, is, last)
	}
	return nil
}

// rewrite writes a lost write again through the status check. A bead that
// moved meanwhile is divergence, and the server never overwrites it.
func (m *Machine) rewrite(ctx context.Context, bc beadsx.Client, project string, is beadsx.Issue, last store.Intent) {
	f, err := decodeFacts(last)
	if err == nil && is.Status == last.ExpectedStatus && is.Assignee == f.Fence {
		set := cloneSet(f.Set)
		set[beadsx.MetaIntentSeq] = strconv.FormatInt(last.Seq, 10)
		set[beadsx.MetaEpoch] = strconv.FormatInt(f.Epoch, 10)
		if last.IdemKey != "" {
			set[beadsx.MetaIdemKey] = last.IdemKey
		}
		spec := beadsx.UpdateSpec{IfStatus: last.ExpectedStatus, IfAssignee: &f.Fence, SetMetadata: set, UnsetMetadata: f.Unset}
		if last.TargetStatus != last.ExpectedStatus {
			spec.Status = last.TargetStatus
		}
		if next := fence(f.Epoch); next != f.Fence {
			spec.Assignee = &next
		}
		if _, err = bc.Update(ctx, is.ID, spec); err == nil {
			m.log.WarnContext(ctx, "rewrote a lost beads write", "project", project, "item", is.ID, "intent", last.ID)
			return
		}
	}
	m.divergence(ctx, project, is.ID, fmt.Sprintf("%s lost the write of intent %d (sequence %d), and the server could not write it again",
		is.ID, last.ID, last.Seq))
}

func decodeFacts(in store.Intent) (facts, error) {
	var f facts
	err := json.Unmarshal(in.Facts, &f)
	return f, err
}

// divergence raises the alert for a bead that changed outside the server.
func (m *Machine) divergence(ctx context.Context, project, item, body string) {
	m.alert(ctx, project, AlertDivergence, item, body)
}

// alert raises an alert, or counts up the open one with the same subject.
// The body passes the redactor first (KTD27).
func (m *Machine) alert(ctx context.Context, project, kind, item, body string) {
	body = m.cfg.Redactor.Redact(body)
	subject := project + "/" + item
	err := m.cfg.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO alerts (project, kind, subject, dedupe_key, body, first_at, last_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (dedupe_key) WHERE state = 'open'
			DO UPDATE SET count = count + 1, last_at = excluded.last_at, body = excluded.body`,
			project, kind, subject, kind+":"+subject, body, now, now)
		return err
	})
	if err != nil {
		m.log.ErrorContext(ctx, "raise an alert", "kind", kind, "project", project, "item", item, "err", err)
		return
	}
	m.log.WarnContext(ctx, "alert", "kind", kind, "project", project, "item", item, "body", body)
}
