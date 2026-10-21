package machine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/iantanwx/loop/internal/store"
)

// Lease is the fencing epoch and the owner of one item (KTD6, KTD7). The
// epoch rises only on a grant, a reclaim or an observed merge. A renewal
// and a re-adoption keep it.
type Lease struct {
	Project   string    `json:"project"`
	Item      string    `json:"item"`
	Epoch     int64     `json:"epoch"`
	Owner     string    `json:"owner"` // the run id; empty when nobody holds it
	GrantedAt time.Time `json:"granted_at"`
	RenewedAt time.Time `json:"renewed_at"`
}

// ErrNotOwner is a renewal by a run that does not hold the lease.
var ErrNotOwner = errors.New("machine: the run does not hold the lease")

type rowQuerier func(query string, args ...any) *sql.Row

func readLease(q rowQuerier, project, item string) (Lease, error) {
	l := Lease{Project: project, Item: item}
	var (
		owner              sql.NullString
		granted, renewedAt sql.NullInt64
	)
	err := q(`SELECT epoch, owner_run_id, granted_at, renewed_at FROM leases WHERE project = ? AND item = ?`, project, item).
		Scan(&l.Epoch, &owner, &granted, &renewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return Lease{}, fmt.Errorf("machine: read the lease of %s/%s: %w", project, item, err)
	}
	l.Owner = owner.String
	if granted.Valid {
		l.GrantedAt = time.UnixMilli(granted.Int64)
	}
	if renewedAt.Valid {
		l.RenewedAt = time.UnixMilli(renewedAt.Int64)
	}
	return l, nil
}

// Lease returns the lease view of one item.
func (m *Machine) Lease(ctx context.Context, project, item string) (Lease, error) {
	var l Lease
	err := m.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		l, err = readLease(func(q string, args ...any) *sql.Row { return tx.QueryRowContext(ctx, q, args...) }, project, item)
		return err
	})
	return l, err
}

// moveLease applies an intent's lease effect in its applied mark. The
// epoch never goes back.
func moveLease(tx *store.Tx, in store.Intent, f facts) error {
	now := tx.Now().UnixMilli()
	owner := sql.NullString{}
	setOwner := f.Owner != nil
	if setOwner && *f.Owner != "" {
		owner = sql.NullString{String: *f.Owner, Valid: true}
	}
	_, err := tx.Exec(`INSERT INTO leases (project, item, epoch, owner_run_id, granted_at, renewed_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (project, item) DO UPDATE SET
			epoch = max(epoch, excluded.epoch),
			owner_run_id = CASE WHEN ? THEN excluded.owner_run_id ELSE owner_run_id END,
			granted_at = CASE WHEN ? THEN excluded.granted_at ELSE granted_at END,
			renewed_at = CASE WHEN ? THEN excluded.renewed_at ELSE renewed_at END`,
		in.Project, in.Item, f.Epoch, owner, nullTime(owner.Valid, now), nullTime(owner.Valid, now),
		setOwner, owner.Valid, owner.Valid)
	if err != nil {
		return fmt.Errorf("machine: move the lease of %s/%s: %w", in.Project, in.Item, err)
	}
	return nil
}

func nullTime(valid bool, ms int64) sql.NullInt64 { return sql.NullInt64{Int64: ms, Valid: valid} }

// Renew records that the run still holds the item, for a re-adopted run
// (AE2). It keeps the epoch, so the run's next write succeeds.
func (m *Machine) Renew(ctx context.Context, project, item, runID string) (Lease, error) {
	var l Lease
	err := m.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		res, err := tx.Exec(`UPDATE leases SET renewed_at = ? WHERE project = ? AND item = ? AND owner_run_id = ?`,
			tx.Now().UnixMilli(), project, item, runID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("%w: %s on %s/%s", ErrNotOwner, runID, project, item)
		}
		l, err = readLease(tx.QueryRow, project, item)
		return err
	})
	return l, err
}
