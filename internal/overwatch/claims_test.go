package overwatch

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/store"
)

// execStore runs one write on st.
func execStore(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if err := st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// project registers a project row, which a run row needs.
func (w *fakeWorld) project(t *testing.T, id string) {
	t.Helper()
	execStore(t, w.st, `INSERT OR IGNORE INTO projects (id, repo, created_at, updated_at) VALUES (?, 'o/r', 0, 0)`, id)
}

// run writes one run row. A zero ended leaves the run live.
func (w *fakeWorld) run(t *testing.T, project, id, item string, ended time.Time) {
	t.Helper()
	w.project(t, project)
	var end sql.NullInt64
	if !ended.IsZero() {
		end = sql.NullInt64{Int64: ended.UnixMilli(), Valid: true}
	}
	execStore(t, w.st, `INSERT INTO runs (run_id, project, stage, item, state, started_at, ended_at) VALUES (?, ?, 'implementer', ?, 'running', ?, ?)`,
		id, project, item, w.now.Add(-3*time.Hour).UnixMilli(), end)
}

// lease writes one lease of item, owned by run, or released when run is
// empty.
func (w *fakeWorld) lease(t *testing.T, project, item, run string) {
	t.Helper()
	var owner sql.NullString
	if run != "" {
		owner = sql.NullString{String: run, Valid: true}
	}
	execStore(t, w.st, `INSERT INTO leases (project, item, epoch, owner_run_id, granted_at) VALUES (?, ?, 1, ?, ?)`,
		project, item, owner, w.now.Add(-2*time.Hour).UnixMilli())
}

func readClaims(t *testing.T, w World) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{claimsPart()})
	if len(r.Parts) != 1 || r.Parts[0].Name != "claims" {
		t.Fatalf("report = %+v", r)
	}
	return r.Parts[0]
}

// C15: a lease whose owner run ended is work, keyed by its item.
func TestC15ALeaseOfAnEndedRunIsWork(t *testing.T) {
	w := newFakeWorld(t)
	w.run(t, "factorio", "r-ended", "factorio-a1", w.now.Add(-time.Hour))
	w.lease(t, "factorio", "factorio-a1", "r-ended")
	p := readClaims(t, w)
	if p.Code != Work {
		t.Fatalf("code = %d, want 1: %v", p.Code, p.Lines)
	}
	key := "overwatch:claims:factorio-a1"
	if len(p.Lines) == 0 || !strings.Contains(p.Lines[0], key) || !strings.Contains(p.Lines[0], "r-ended") {
		t.Fatalf("lines = %q, want one that quotes %s and names the run", p.Lines, key)
	}
}

// C15: a lease whose owner has no run row is work.
func TestC15ALeaseWithNoRunRowIsWork(t *testing.T) {
	w := newFakeWorld(t)
	w.lease(t, "factorio", "factorio-b2", "r-lost")
	p := readClaims(t, w)
	if p.Code != Work {
		t.Fatalf("code = %d, want 1: %v", p.Code, p.Lines)
	}
	if !strings.Contains(p.Lines[0], "overwatch:claims:factorio-b2") || !strings.Contains(p.Lines[0], "r-lost") {
		t.Fatalf("lines = %q", p.Lines)
	}
}

// C15: a lease of a live run, and a released lease, read code 0.
func TestC15ALeaseOfALiveRunReadsQuiet(t *testing.T) {
	w := newFakeWorld(t)
	w.run(t, "factorio", "r-live", "factorio-c3", time.Time{})
	w.lease(t, "factorio", "factorio-c3", "r-live")
	w.lease(t, "factorio", "factorio-d4", "")
	p := readClaims(t, w)
	if p.Code != Quiet {
		t.Fatalf("code = %d, want 0: %v", p.Code, p.Lines)
	}
	for _, l := range p.Lines {
		if strings.Contains(l, "overwatch:claims:") {
			t.Fatalf("a quiet claims part quotes a key: %q", p.Lines)
		}
	}
}

// C15: a fault that names the key files the finding, so it reads code 0.
func TestC15AFaultThatNamesTheKeyFilesTheLease(t *testing.T) {
	w := newFakeWorld(t)
	w.lease(t, "factorio", "factorio-b2", "r-lost")
	w.fault(t, "the lease leaked: overwatch:claims:factorio-b2.", "open")
	if p := readClaims(t, w); p.Code != Quiet {
		t.Fatalf("code = %d, want 0: %v", p.Code, p.Lines)
	}
}
