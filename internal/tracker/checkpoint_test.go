package tracker_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/store"
	"github.com/iantanwx/loop/internal/tracker"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func checkpointRows(t *testing.T, s *store.Store) int {
	t.Helper()
	var n int
	err := s.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*) FROM adapter_checkpoints`).Scan(&n)
	})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func load(t *testing.T, cp tracker.CheckpointStore, project, adapter string) int64 {
	t.Helper()
	seq, err := cp.Load(context.Background(), project, adapter)
	if err != nil {
		t.Fatalf("Load(%s, %s): %v", project, adapter, err)
	}
	return seq
}

func TestCheckpointMissingIsZero(t *testing.T) {
	cp := tracker.NewCheckpoints(openStore(t))
	if got := load(t, cp, "p", "linear"); got != 0 {
		t.Fatalf("Load = %d, want 0", got)
	}
}

func TestCheckpointSaveAndOverwrite(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	cp := tracker.NewCheckpoints(s)
	for _, seq := range []int64{5, 9} {
		if err := cp.Save(ctx, "p", "linear", seq); err != nil {
			t.Fatalf("Save(%d): %v", seq, err)
		}
		if got := load(t, cp, "p", "linear"); got != seq {
			t.Fatalf("Load = %d, want %d", got, seq)
		}
	}
	if n := checkpointRows(t, s); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	var updated int64
	err := s.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT updated_at FROM adapter_checkpoints WHERE project = 'p' AND adapter = 'linear'`).Scan(&updated)
	})
	if err != nil || updated <= 0 {
		t.Fatalf("updated_at = %d, err %v; want a unix millisecond time", updated, err)
	}
}

func TestCheckpointKeyedByProjectAndAdapter(t *testing.T) {
	ctx := context.Background()
	cp := tracker.NewCheckpoints(openStore(t))
	_ = cp.Save(ctx, "p1", "linear", 3)
	_ = cp.Save(ctx, "p1", "jira", 4)
	_ = cp.Save(ctx, "p2", "linear", 5)
	if a, b, c := load(t, cp, "p1", "linear"), load(t, cp, "p1", "jira"), load(t, cp, "p2", "linear"); a != 3 || b != 4 || c != 5 {
		t.Fatalf("checkpoints = %d %d %d, want 3 4 5", a, b, c)
	}
}

func TestCheckpointCorruptValueIsAnError(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	err := s.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO adapter_checkpoints (project, adapter, checkpoint, updated_at) VALUES ('p', 'linear', 'abc', 1)`)
		return err
	})
	if err != nil {
		t.Fatalf("forge row: %v", err)
	}
	if _, err := tracker.NewCheckpoints(s).Load(ctx, "p", "linear"); err == nil {
		t.Fatal("Load of a corrupt checkpoint = nil error")
	}
}

func TestCheckpointRefusesNegative(t *testing.T) {
	if err := tracker.NewCheckpoints(openStore(t)).Save(context.Background(), "p", "linear", -1); err == nil {
		t.Fatal("Save(-1) = nil error")
	}
}
