package tracker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/iantanwx/loop/internal/store"
)

// CheckpointStore holds one journal checkpoint for each adapter of each
// project. A checkpoint is the seq of the last journal record the adapter
// applied. A project and adapter with no checkpoint start at 0.
type CheckpointStore interface {
	Load(ctx context.Context, project, adapter string) (int64, error)
	Save(ctx context.Context, project, adapter string, seq int64) error
}

// Checkpoints keeps checkpoints in the adapter_checkpoints table, keyed by
// project and adapter name. The checkpoint column holds the seq in decimal.
type Checkpoints struct{ s *store.Store }

var _ CheckpointStore = (*Checkpoints)(nil)

// NewCheckpoints returns the table-backed checkpoint store.
func NewCheckpoints(s *store.Store) *Checkpoints { return &Checkpoints{s: s} }

// Load implements CheckpointStore.
func (c *Checkpoints) Load(ctx context.Context, project, adapter string) (int64, error) {
	var text string
	err := c.s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx,
			`SELECT checkpoint FROM adapter_checkpoints WHERE project = ? AND adapter = ?`,
			project, adapter).Scan(&text)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("tracker: load checkpoint %s/%s: %w", project, adapter, err)
	}
	seq, err := strconv.ParseInt(text, 10, 64)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("tracker: checkpoint %s/%s is not a journal seq: %q", project, adapter, text)
	}
	return seq, nil
}

// Save implements CheckpointStore. It writes the row in one transaction.
func (c *Checkpoints) Save(ctx context.Context, project, adapter string, seq int64) error {
	if seq < 0 {
		return fmt.Errorf("tracker: checkpoint %s/%s: negative seq %d", project, adapter, seq)
	}
	err := c.s.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO adapter_checkpoints (project, adapter, checkpoint, updated_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (project, adapter) DO UPDATE SET
				checkpoint = excluded.checkpoint, updated_at = excluded.updated_at`,
			project, adapter, strconv.FormatInt(seq, 10), tx.Now().UnixMilli())
		return err
	})
	if err != nil {
		return fmt.Errorf("tracker: save checkpoint %s/%s: %w", project, adapter, err)
	}
	return nil
}
