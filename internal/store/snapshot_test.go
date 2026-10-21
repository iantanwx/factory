package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestSnapshotWhileWriterHoldsAnOpenTransaction(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		err := s.Write(ctx, func(tx *Tx) error {
			_, err := tx.Exec(`INSERT INTO audit (at, actor, action) VALUES (?, 'test', 'committed')`, i)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Hold the writer inside an open transaction while the snapshot runs.
	inTx := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- s.Write(ctx, func(tx *Tx) error {
			if _, err := tx.Exec(`INSERT INTO audit (at, actor, action) VALUES (0, 'test', 'in-flight')`); err != nil {
				return err
			}
			close(inTx)
			<-release
			return nil
		})
	}()
	<-inTx
	dst := filepath.Join(t.TempDir(), "snap.db")
	snapErr := s.Snapshot(ctx, dst)
	close(release)
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if snapErr != nil {
		t.Fatalf("Snapshot: %v", snapErr)
	}

	snap, err := Open(ctx, dst)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer func() { _ = snap.Close() }()
	var committed, inFlight int
	var check string
	err = snap.Read(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT count(*) FROM audit WHERE action = 'committed'`).Scan(&committed); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT count(*) FROM audit WHERE action = 'in-flight'`).Scan(&inFlight); err != nil {
			return err
		}
		return tx.QueryRow(`PRAGMA integrity_check`).Scan(&check)
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed != 50 || inFlight != 0 || check != "ok" {
		t.Fatalf("snapshot: committed %d (want 50), in-flight %d (want 0), integrity %q", committed, inFlight, check)
	}
}

func TestSnapshotUnderConcurrentCommitsHoldsEveryEarlierCommit(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	var committed atomic.Int64
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		for i := 1; ; i++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			err := s.Write(ctx, func(tx *Tx) error {
				_, err := tx.Exec(`INSERT INTO audit (id, at, actor, action) VALUES (?, 0, 'test', 'row')`, i)
				return err
			})
			if err != nil {
				done <- err
				return
			}
			committed.Store(int64(i))
		}
	}()
	for committed.Load() < 20 {
		// let the writer get ahead
		_ = countRows(t, s, "audit")
	}
	before := committed.Load()
	dst := filepath.Join(t.TempDir(), "snap.db")
	err := s.Snapshot(ctx, dst)
	close(stop)
	if werr := <-done; werr != nil {
		t.Fatal(werr)
	}
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snap, err := Open(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = snap.Close() }()
	var n, maxID int64
	err = snap.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT count(*), coalesce(max(id), 0) FROM audit`).Scan(&n, &maxID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < before || n != maxID {
		t.Fatalf("snapshot holds %d rows (max id %d); want at least %d and no gap", n, maxID, before)
	}
}

func TestSnapshotRefusesAnExistingTarget(t *testing.T) {
	s := openTemp(t)
	dst := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(dst, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Snapshot(context.Background(), dst); err == nil {
		t.Fatal("Snapshot overwrote an existing file")
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "keep" {
		t.Fatalf("existing target changed to %q", b)
	}
}
