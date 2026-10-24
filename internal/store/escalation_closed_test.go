package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// Migration 0010 closes each escalation that already waits for its panel
// on a terminal item, and drops its hold, so it leaves the fixer's queue.
// An escalation on an item that can still move keeps waiting, and the id
// sequence goes on where it stood, past a deleted row (F-25).
func TestMigrationClosesEscalationsStuckOnTerminalItems(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "loop.db")
	upTo9 := fstest.MapFS{}
	ms, err := loadMigrations(embeddedMigrations)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms[:9] {
		upTo9[m.name] = file(m.sql)
	}
	s, err := openWith(ctx, path, upTo9)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	db := rawDB(t, path)
	for _, q := range []string{
		// tk-1 merged to done, tk-2 still moves, bg-1 was shaped after it
		// went through todo.
		`INSERT INTO transitions (intent_id, project, item, seq, kind, verb, from_status, to_status, epoch, at) VALUES
			(1, 'alpha', 'tk-1', 1, 'ticket', 'claim', 'todo', 'in_progress', 1, 0),
			(2, 'alpha', 'tk-1', 2, 'ticket', 'merged', 'in_progress', 'done', 1, 0),
			(3, 'alpha', 'tk-2', 1, 'ticket', 'claim', 'todo', 'in_progress', 1, 0),
			(4, 'alpha', 'bg-1', 1, 'bug', 'triage', 'new', 'reproducing', 1, 0),
			(5, 'alpha', 'bg-1', 2, 'bug', 'shape', 'reproducing', 'shaped', 1, 0)`,
		`INSERT INTO escalations (id, project, item, level, choice, finding, state, created_at, updated_at) VALUES
			(3, 'alpha', 'tk-1', 4, 'inside', 'layouts', 'awaiting_panel', 0, 0),
			(4, 'alpha', 'tk-2', 4, 'inside', 'colours', 'awaiting_panel', 0, 0),
			(5, 'alpha', 'bg-1', 5, 'inside', 'a fact', 'awaiting_panel', 0, 0),
			(6, 'alpha', 'tk-3', 4, 'inside', 'no history', 'awaiting_panel', 0, 0),
			(7, 'alpha', 'tk-1', 4, 'inside', 'routed', 'person', 0, 0),
			(9, 'alpha', 'tk-5', 4, 'inside', 'gone', 'loop', 0, 0)`,
		`DELETE FROM escalations WHERE id = 9`,
		`INSERT INTO holds (project, item, holder, kind, created_at) VALUES
			('alpha', 'tk-1', 'escalation:3', 'escalation', 0),
			('alpha', 'tk-2', 'escalation:4', 'escalation', 0),
			('alpha', 'bg-1', 'escalation:5', 'escalation', 0),
			('alpha', 'tk-3', 'escalation:6', 'escalation', 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.DatabaseSchema() < 10 {
		t.Fatalf("schema = %d, want 10 or later", s.DatabaseSchema())
	}
	want := map[int64]string{3: "closed", 4: "awaiting_panel", 5: "closed", 6: "awaiting_panel", 7: "person"}
	got := map[int64]string{}
	var holds []string
	err = s.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id, state FROM escalations`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id int64
			var st string
			if err := rows.Scan(&id, &st); err != nil {
				return err
			}
			got[id] = st
		}
		if err := rows.Err(); err != nil {
			return err
		}
		hs, err := tx.Query(`SELECT holder FROM holds ORDER BY holder`)
		if err != nil {
			return err
		}
		defer func() { _ = hs.Close() }()
		for hs.Next() {
			var h string
			if err := hs.Scan(&h); err != nil {
				return err
			}
			holds = append(holds, h)
		}
		return hs.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, st := range want {
		if got[id] != st {
			t.Errorf("escalation %d = %q, want %q", id, got[id], st)
		}
	}
	if len(holds) != 2 || holds[0] != "escalation:4" || holds[1] != "escalation:6" {
		t.Errorf("holds = %v, want escalation:4 and escalation:6", holds)
	}

	var id int64
	err = s.Write(ctx, func(tx *Tx) error {
		res, err := tx.Exec(`INSERT INTO escalations (project, item, level, choice, finding, created_at, updated_at)
			VALUES ('alpha', 'tk-4', 4, 'inside', 'next', 0, 0)`)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	if err != nil || id != 10 {
		t.Fatalf("the next escalation id = %d %v, want 10", id, err)
	}
}
