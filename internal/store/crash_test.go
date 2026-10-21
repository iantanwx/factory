package store

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	helperEnv     = "LOOP_STORE_HELPER"
	helperPathEnv = "LOOP_STORE_HELPER_DB"
	helperCommits = 20
)

// TestHelperProcess is the child of TestKilledWriterLosesOnlyItsOpenTransaction.
// It does nothing unless the parent sets helperEnv.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "write-then-block" {
		t.Skip("helper process only")
	}
	ctx := context.Background()
	s, err := Open(ctx, os.Getenv(helperPathEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(2)
	}
	for i := 1; i <= helperCommits; i++ {
		err := s.Write(ctx, func(tx *Tx) error {
			_, err := tx.Exec(`INSERT INTO audit (at, actor, action) VALUES (?, 'helper', 'committed')`, i)
			return err
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "commit:", err)
			os.Exit(2)
		}
	}
	_ = s.Write(ctx, func(tx *Tx) error {
		for i := 0; i < 500; i++ {
			if _, err := tx.Exec(`INSERT INTO audit (at, actor, action) VALUES (?, 'helper', 'uncommitted')`, i); err != nil {
				fmt.Fprintln(os.Stderr, "open write:", err)
				os.Exit(2)
			}
		}
		fmt.Println("READY")
		select {} // block inside the transaction until the parent kills us
	})
	os.Exit(3)
}

func TestKilledWriterLosesOnlyItsOpenTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loop.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.v=false")
	cmd.Env = append(os.Environ(), helperEnv+"=write-then-block", helperPathEnv+"="+path)
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "READY" {
				ready <- nil
				return
			}
		}
		ready <- fmt.Errorf("helper exited before READY: %v", sc.Err())
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("helper never signalled READY")
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("helper did not die by SIGKILL: %v", err)
	}

	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen after SIGKILL: %v", err)
	}
	defer func() { _ = s.Close() }()
	var committed, uncommitted int
	var check string
	err = s.Read(context.Background(), func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT count(*) FROM audit WHERE action = 'committed'`).Scan(&committed); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT count(*) FROM audit WHERE action = 'uncommitted'`).Scan(&uncommitted); err != nil {
			return err
		}
		return tx.QueryRow(`PRAGMA integrity_check`).Scan(&check)
	})
	if err != nil {
		t.Fatal(err)
	}
	if committed != helperCommits || uncommitted != 0 || check != "ok" {
		t.Fatalf("after SIGKILL: committed %d (want %d), uncommitted %d (want 0), integrity %q",
			committed, helperCommits, uncommitted, check)
	}
	// The writer lock died with the process.
	addProject(t, s, "after-crash")
}
