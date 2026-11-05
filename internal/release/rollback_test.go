package release

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/server"
)

func insertProject(t *testing.T, db, id string) {
	t.Helper()
	conn, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES (?, 'o/r', 1, 1)`, id); err != nil {
		t.Fatal(err)
	}
}

func projectIDs(t *testing.T, db string) []string {
	t.Helper()
	conn, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	rows, err := conn.Query(`SELECT id FROM projects ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func (w *instanceWorld) rollback() *Rollback {
	return &Rollback{Host: Host{Layout: w.layout, Service: w.svc, Server: w.server}}
}

func TestRollbackAfterAnAdditiveMigrationKeepsEveryWrite(t *testing.T) {
	w := newInstance(t)
	rw := newReleaseWorld(t)
	m := w.next("v1.1.0")
	m.Schema++ // an additive migration: the minimum reader schema stays
	rw.publish(m, []byte("binary v1.1.0"), nil, "")
	db := w.layout.DB()
	w.server.onDrain = func() { insertProject(t, db, "during-drain") }
	rep, err := w.upgrader(rw).Run(context.Background(), "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.NoRollback) != 0 {
		t.Fatalf("an additive migration blocked rollback: %q", rep.NoRollback)
	}
	w.server.onDrain = nil
	insertProject(t, db, "after-swap")
	w.svc.events = nil

	rb, err := w.rollback().Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rb.From != "v1.1.0" || rb.To != "v1.0.0" || !rb.Started {
		t.Fatalf("report = %+v", rb)
	}
	if cur, _ := w.layout.CurrentVersion(); cur != "v1.0.0" {
		t.Fatalf("current = %s", cur)
	}
	if got := w.svc.list(); !slices.Equal(got, []string{"stop", "start"}) {
		t.Fatalf("events = %q", got)
	}
	if ids := projectIDs(t, db); !slices.Equal(ids, []string{"after-swap", "during-drain"}) {
		t.Fatalf("rollback lost a write: %q", ids)
	}
	if data, _ := os.ReadFile(filepath.Join(w.dir, server.PreviousVersionFile)); strings.TrimSpace(string(data)) != "v1.1.0" {
		t.Fatalf("previous_version = %q", data)
	}
	// The highest version stays, so an upgrade below it is still refused,
	// and an upgrade back to it is not.
	if st, _ := w.layout.ReadState(); st.Highest != "v1.1.0" {
		t.Fatalf("highest = %s", st.Highest)
	}
	if _, err := w.upgrader(rw).Run(context.Background(), "v1.1.0"); err != nil {
		t.Fatalf("upgrade back to the highest: %v", err)
	}
}

func TestRollbackPastANoRollbackReleaseIsRefused(t *testing.T) {
	cases := map[string]func(w *instanceWorld, m *Metadata){
		"marked": func(_ *instanceWorld, m *Metadata) { m.NoRollback = true },
		"non-additive migration": func(w *instanceWorld, m *Metadata) {
			m.Schema, m.MinReaderSchema = w.meta.Schema+1, w.meta.Schema+1
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			w := newInstance(t)
			rw := newReleaseWorld(t)
			m := w.next("v1.1.0")
			mutate(w, &m)
			rw.publish(m, []byte("binary v1.1.0"), nil, "")
			if _, err := w.upgrader(rw).Run(context.Background(), "v1.1.0"); err != nil {
				t.Fatal(err)
			}
			w.svc.events = nil
			before := tree(t, w.dir)
			_, err := w.rollback().Run(context.Background())
			wantRefusal(t, err, CodeNoRollback, "")
			if ev := w.svc.list(); len(ev) != 0 {
				t.Fatalf("a refused rollback acted: %q", ev)
			}
			if after := tree(t, w.dir); after != before {
				t.Fatalf("a refused rollback changed the instance")
			}
		})
	}
	t.Run("beads pin bump", func(t *testing.T) {
		w := newInstance(t)
		rw := newReleaseWorld(t)
		bump := newPinWorld(t, "1.4.0")
		m := w.next("v1.1.0")
		m.Pins = []Pin{bump.pins[0], w.pins.pins[1]}
		rw.publish(m, []byte("binary v1.1.0"), nil, "")
		u := w.upgrader(rw)
		u.Prov = bump.prov
		if _, err := u.Run(context.Background(), "v1.1.0"); err != nil {
			t.Fatal(err)
		}
		_, err := w.rollback().Run(context.Background())
		wantRefusal(t, err, CodeNoRollback, "")
	})
}

func TestRollbackRefusalsExitThree(t *testing.T) {
	for _, c := range Refusals() {
		if c.Code == CodeNoRollback && c.Exit != cli.ExitRefused {
			t.Fatalf("NO_ROLLBACK exits %d", c.Exit)
		}
	}
	r := cli.Surface()
	for _, c := range Refusals() {
		if err := r.RegisterRefusal(c); err != nil {
			t.Fatalf("register %s: %v", c.Code, err)
		}
	}
}

func TestRollbackWithNoEarlierVersion(t *testing.T) {
	w := newInstance(t)
	_, err := w.rollback().Run(context.Background())
	wantRefusal(t, err, cli.CodeNotFound, "no_earlier_version")
}

// C39: the rollback runs the hand-back after the service stops and before
// current moves. A failed hand-back leaves current where it was and starts
// the service again.
func TestRollbackRunsTheHandBackWhileTheServiceIsStopped(t *testing.T) {
	setup := func(t *testing.T) *instanceWorld {
		w := newInstance(t)
		rw := newReleaseWorld(t)
		rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
		if _, err := w.upgrader(rw).Run(context.Background(), "v1.1.0"); err != nil {
			t.Fatal(err)
		}
		w.svc.events = nil
		return w
	}
	t.Run("it runs", func(t *testing.T) {
		w := setup(t)
		var saw []string
		rb := w.rollback()
		rb.HandBack = func(context.Context) error {
			cur, _ := w.layout.CurrentVersion()
			saw = append(append(saw, w.svc.list()...), cur)
			return nil
		}
		if _, err := rb.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(saw, []string{"stop", "v1.1.0"}) {
			t.Fatalf("the hand-back saw %q, want the service stopped and current at v1.1.0", saw)
		}
		if cur, _ := w.layout.CurrentVersion(); cur != "v1.0.0" {
			t.Fatalf("current = %s", cur)
		}
	})
	t.Run("it fails", func(t *testing.T) {
		w := setup(t)
		rb := w.rollback()
		rb.HandBack = func(context.Context) error { return errors.New("the store is locked") }
		rep, err := rb.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), "the store is locked") || rep.Started {
			t.Fatalf("rollback = %+v, %v; want the hand-back's error", rep, err)
		}
		if cur, _ := w.layout.CurrentVersion(); cur != "v1.1.0" {
			t.Fatalf("a failed hand-back moved current to %s", cur)
		}
		if got := w.svc.list(); !slices.Equal(got, []string{"stop", "start"}) {
			t.Fatalf("events = %q, want the service started again", got)
		}
	})
}
