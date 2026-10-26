package overwatch

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// stallsWorld is the fake world with host config and project config, the
// two reads the stalls part makes that the records read does not.
type stallsWorld struct {
	*fakeWorld
	host    *config.HostConfig
	configs map[string]*config.ProjectConfig // a project with none has no config yet
}

func (w *stallsWorld) Host(context.Context) (*config.HostConfig, error) { return w.host, nil }
func (w *stallsWorld) ProjectConfig(_ context.Context, project string) (*config.ProjectSnapshot, error) {
	c, ok := w.configs[project]
	if !ok {
		return nil, config.ErrConfigMissing
	}
	return &config.ProjectSnapshot{Config: c}, nil
}

// at is a time of the fixture's day, 2026-10-24, in UTC.
func at(hour, minute int) time.Time { return time.Date(2026, 10, 24, hour, minute, 0, 0, time.UTC) }

// newStallsWorld is a world at 09:05 on the host's default schedules:
// the implementer fires at 3, 8, 13 ... past each hour, the code-reviewer
// every five minutes and the fixer at 14 and 44. Project factorio enables
// every run stage. Project alpha has no config yet.
func newStallsWorld(t *testing.T) *stallsWorld {
	t.Helper()
	w := &stallsWorld{fakeWorld: newFakeWorld(t), host: config.DefaultHost(t.TempDir()), configs: map[string]*config.ProjectConfig{}}
	w.now = at(9, 5)
	stages := map[string]config.StageConfig{}
	for _, s := range config.RunStages() {
		stages[s] = config.StageConfig{}
	}
	w.configs["factorio"] = &config.ProjectConfig{Stages: stages}
	for _, p := range w.projects {
		w.exec(t, `INSERT INTO projects (id, repo, created_at, updated_at) VALUES (?, ?, 1, 1)`, p.ID, p.Repo)
	}
	return w
}

func (w *stallsWorld) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// item puts one item of factorio in status, last written by beads at
// updated, with no transition that dates its entry.
func (w *stallsWorld) item(id string, kind machine.Kind, status string, updated time.Time) {
	w.add("factorio", machine.Item{ID: id, Kind: kind, Status: status, Updated: updated}, "")
}

// entered records the transition that moved item into status at when.
func (w *stallsWorld) entered(t *testing.T, item, from, to string, when time.Time) {
	t.Helper()
	err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		var seq int64
		if err := tx.QueryRow(`SELECT coalesce(max(seq), 0) + 1 FROM intents WHERE project = 'factorio' AND item = ?`, item).Scan(&seq); err != nil {
			return err
		}
		r, err := tx.Exec(`INSERT INTO intents (project, item, seq, kind, verb, epoch, state, created_at)
			VALUES ('factorio', ?, ?, 'ticket', 'move', 0, 'applied', ?)`, item, seq, when.UnixMilli())
		if err != nil {
			return err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO transitions (intent_id, project, item, seq, kind, verb, from_status, to_status, epoch, at)
			VALUES (?, 'factorio', ?, ?, 'ticket', 'move', ?, ?, 0, ?)`, id, item, seq, from, to, when.UnixMilli())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// run records one run of factorio. A zero ended leaves it live.
func (w *stallsWorld) run(t *testing.T, id, stage, item string, started, ended time.Time) {
	t.Helper()
	var end any
	state := "running"
	if !ended.IsZero() {
		end, state = ended.UnixMilli(), "settled"
	}
	w.exec(t, `INSERT INTO runs (run_id, project, stage, item, state, outcome_class, started_at, ended_at) VALUES (?, 'factorio', ?, ?, ?, 'done', ?, ?)`,
		id, stage, item, state, started.UnixMilli(), end)
}

// escalation raises one escalation of factorio at raised, in state.
func (w *stallsWorld) escalation(t *testing.T, item, state string, raised time.Time) {
	t.Helper()
	w.exec(t, `INSERT INTO escalations (project, item, run_id, stage, level, choice, finding, state, created_at, updated_at)
		VALUES ('factorio', ?, 'r-raiser', 'implementer', 4, 'inside', 'a finding', ?, ?, ?)`, item, state, raised.UnixMilli(), raised.UnixMilli())
}

// readStallsPart reads the stalls part of Parts() once.
func readStallsPart(t *testing.T, w World) Reading {
	t.Helper()
	i := slices.IndexFunc(Parts(), func(p Part) bool { return p.Name == "stalls" })
	if i < 0 {
		t.Fatal("Parts() holds no stalls part")
	}
	r, err := Parts()[i].Read(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func findingOf(r Reading, key string) (Finding, bool) {
	for _, f := range r.Findings {
		if f.Key == key {
			return f, true
		}
	}
	return Finding{}, false
}

// noteOf returns the one note that names subject.
func noteOf(t *testing.T, r Reading, subject string) string {
	t.Helper()
	var got []string
	for _, n := range r.Notes {
		if slices.Contains(Tokens(n), subject) {
			got = append(got, n)
		}
	}
	if len(got) != 1 {
		t.Fatalf("notes that name %s = %q, want one; every note: %q", subject, got, r.Notes)
	}
	return got[0]
}

func mustContain(t *testing.T, line string, want ...string) {
	t.Helper()
	for _, s := range want {
		if !strings.Contains(line, s) {
			t.Errorf("line %q does not hold %q", line, s)
		}
	}
}

// C9: an escalation in awaiting_panel that a fixer run of its project ended
// after is work, and the line names the escalation, the run and the key.
func TestC9AnEscalationThatAFixerRunPassedByIsAStall(t *testing.T) {
	w := newStallsWorld(t)
	w.escalation(t, "factorio-e1", "awaiting_panel", at(8, 50))
	w.run(t, "r-0ldf1xer0000", "fixer", "", at(8, 14), at(8, 20)) // ended before the raise
	w.run(t, "r-332c1c7116a7", "fixer", "", at(8, 59), at(9, 1))
	r := readStallsPart(t, w)
	key := "overwatch:stalls:escalation-1"
	f, ok := findingOf(r, key)
	if !ok {
		t.Fatalf("no finding %s in %+v", key, r)
	}
	if !slices.Contains(Tokens(f.Line), key) {
		t.Errorf("line %q does not quote %s", f.Line, key)
	}
	mustContain(t, f.Line, "escalation 1", "r-332c1c7116a7")
	if strings.Contains(f.Line, "r-0ldf1xer0000") {
		t.Errorf("line %q names a run that ended before the raise", f.Line)
	}
}

// C9: an escalation with no fixer run since its raise waits two fixer
// fires. The fixer fires at 14 and 44 past each hour.
func TestC9AnEscalationWaitsTwoFixerFires(t *testing.T) {
	cases := []struct {
		name   string
		raised time.Time
		work   bool
	}{
		{"no fire since its raise", at(8, 50), false},
		{"one fire since its raise", at(8, 40), false},
		{"two fires since its raise", at(8, 10), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStallsWorld(t)
			w.escalation(t, "factorio-e1", "awaiting_panel", c.raised)
			w.run(t, "r-0ldf1xer0000", "fixer", "", at(7, 44), at(7, 50))
			r := readStallsPart(t, w)
			f, ok := findingOf(r, "overwatch:stalls:escalation-1")
			if ok != c.work {
				t.Fatalf("work = %v, want %v: %+v", ok, c.work, r)
			}
			if c.work {
				mustContain(t, f.Line, "escalation 1", "fixer")
				return
			}
			mustContain(t, noteOf(t, r, "panel/1"), "escalation 1", "fixer")
		})
	}
}

// C9: a live fixer run that started after the raise is the panel at work,
// and the escalation's move out of awaiting_panel ends the work.
func TestC9ALiveFixerRunAndAMoveOutOfThePanelAreNotWork(t *testing.T) {
	w := newStallsWorld(t)
	w.escalation(t, "factorio-e1", "awaiting_panel", at(8, 10))
	w.run(t, "r-11vef1xer000", "fixer", "", at(9, 4), time.Time{})
	w.escalation(t, "factorio-e2", "loop", at(8, 10))
	w.run(t, "r-332c1c7116a7", "fixer", "", at(8, 14), at(8, 20))
	r := readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:escalation-2"); ok {
		t.Errorf("an escalation routed to the loop is work: %q", f.Line)
	}
	// escalation 1 has a fixer run that ended after its raise too, so it
	// is work even while another fixer run is live.
	if _, ok := findingOf(r, "overwatch:stalls:escalation-1"); !ok {
		t.Errorf("escalation 1 is not work: %+v", r)
	}

	w = newStallsWorld(t)
	w.escalation(t, "factorio-e1", "awaiting_panel", at(8, 10))
	w.run(t, "r-11vef1xer000", "fixer", "", at(9, 4), time.Time{})
	r = readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:escalation-1"); ok {
		t.Errorf("an escalation whose panel runs now is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "panel/1"), "r-11vef1xer000")
}

// f4 builds the F-4 fixture: escalations 1, 2 and 3 in awaiting_panel,
// fixer runs r-332c1c7116a7, r-53f391c45f88 and r-131cf44e5259 that ended
// done after them, and a doorbell that already rang through Notifier.Poke
// for each panel entry.
func f4(t *testing.T) (*stallsWorld, *doorbell) {
	t.Helper()
	w := newStallsWorld(t)
	w.escalation(t, "factorio-e1", "awaiting_panel", at(7, 50))
	w.escalation(t, "factorio-e2", "awaiting_panel", at(7, 55))
	w.escalation(t, "factorio-e3", "awaiting_panel", at(8, 0))
	w.run(t, "r-332c1c7116a7", "fixer", "", at(8, 14), at(8, 20))
	w.run(t, "r-53f391c45f88", "fixer", "", at(8, 44), at(8, 50))
	w.run(t, "r-131cf44e5259", "fixer", "", at(9, 0), at(9, 4))
	d := &doorbell{}
	n := &inbox.Notifier{Inbox: inbox.New(inbox.Config{Store: w.st, Now: w.Now}), Ring: d.ring}
	for range 2 {
		if err := n.Poke(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.texts) != 1 || !strings.Contains(d.texts[0], "panel/1") || !strings.Contains(d.texts[0], "panel/3") {
		t.Fatalf("the doorbell rang %q, want one ring for the three panel entries", d.texts)
	}
	d.texts = nil
	return w, d
}

// stallsOnly is the stalls entry of Parts().
func stallsOnly(t *testing.T) []Part {
	t.Helper()
	for _, p := range Parts() {
		if p.Name == "stalls" {
			return []Part{p}
		}
	}
	t.Fatal("Parts() holds no stalls part")
	return nil
}

// C9 and A2: with the F-4 fixture, whose panel entries already rang, a
// tick rings once and names stalls.
func TestC9TheF4FixtureRingsOnceAndNamesStalls(t *testing.T) {
	w, d := f4(t)
	j := &Job{World: w, Parts: stallsOnly(t), Ring: d.ring}
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "loop overwatch: work in stalls. Run `loop overwatch sweep`, then follow section 1 of your runbook."
	if len(d.texts) != 1 || d.texts[0] != want {
		t.Fatalf("the tick rang %q, want once %q", d.texts, want)
	}
	r, _ := j.Last()
	if len(r.Parts) != 1 || r.Parts[0].Code != Work {
		t.Fatalf("report = %+v", r)
	}
	for _, n := range []string{"1", "2", "3"} {
		key := "overwatch:stalls:escalation-" + n
		i := slices.IndexFunc(r.Parts[0].Lines, func(l string) bool { return slices.Contains(Tokens(l), key) })
		if i < 0 {
			t.Fatalf("no line names %s: %q", key, r.Parts[0].Lines)
		}
		// The line names the newest fixer run that ended after the raise.
		mustContain(t, r.Parts[0].Lines[i], "escalation "+n, "r-131cf44e5259")
	}
}

// C9: a fault whose text holds an escalation's key ends that escalation's
// work. Once every key is filed, the tick rings nobody.
func TestC9AFaultThatNamesTheKeyEndsTheWork(t *testing.T) {
	w, d := f4(t)
	w.fault(t, "fixer runs end done with escalations waiting: overwatch:stalls:escalation-1.", "open")
	r := Sweep(context.Background(), w, stallsOnly(t))
	if r.Parts[0].Code != Work {
		t.Fatalf("escalations 2 and 3 are filed too: %+v", r)
	}
	i := slices.IndexFunc(r.Parts[0].Lines, func(l string) bool { return slices.Contains(Tokens(l), "overwatch:stalls:escalation-1") })
	if i < 0 || !strings.Contains(r.Parts[0].Lines[i], "filed in F-1") {
		t.Fatalf("escalation 1's line is not filed in F-1: %q", r.Parts[0].Lines)
	}
	w.fault(t, "the same for overwatch:stalls:escalation-2 and overwatch:stalls:escalation-3", "acknowledged")
	j := &Job{World: w, Parts: stallsOnly(t), Ring: d.ring}
	if err := j.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.texts) != 0 {
		t.Fatalf("a tick whose every stall is filed rang %q", d.texts)
	}
}

// C10: an item stalls at the second fire of its stage's schedule after it
// entered its state. The implementer fires at 3, 8, 13 ... past each hour,
// and the clock reads 09:05.
func TestC10AnItemStallsAtTheSecondFireOfItsStage(t *testing.T) {
	cases := []struct {
		name    string
		updated time.Time
		work    bool
		says    string
	}{
		{"zero fires", at(9, 4), false, "no fire"},
		{"one fire", at(9, 2), false, "one fire"},
		{"two fires", at(8, 57), true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStallsWorld(t)
			w.item("factorio-t1", machine.KindTicket, machine.Todo, c.updated)
			r := readStallsPart(t, w)
			key := "overwatch:stalls:factorio-t1@todo"
			f, ok := findingOf(r, key)
			if ok != c.work {
				t.Fatalf("work = %v, want %v: %+v", ok, c.work, r)
			}
			if c.work {
				if !slices.Contains(Tokens(f.Line), key) {
					t.Errorf("line %q does not quote %s", f.Line, key)
				}
				mustContain(t, f.Line, "factorio-t1", "implementer")
				return
			}
			mustContain(t, noteOf(t, r, "factorio-t1"), "implementer", c.says)
		})
	}
}

// C10: the entry into the state comes from the item's last transition that
// changed its status, ahead of the item's last bead write.
func TestC10TheEntryIsTheTransitionIntoTheState(t *testing.T) {
	w := newStallsWorld(t)
	w.item("factorio-t1", machine.KindTicket, machine.Todo, at(9, 4))
	w.entered(t, "factorio-t1", machine.Backlog, machine.Todo, at(8, 50))
	w.item("factorio-t2", machine.KindTicket, machine.Todo, at(7, 0))
	w.entered(t, "factorio-t2", machine.Backlog, machine.Todo, at(9, 4))
	r := readStallsPart(t, w)
	if _, ok := findingOf(r, "overwatch:stalls:factorio-t1@todo"); !ok {
		t.Errorf("factorio-t1 entered todo at 08:50 and is not work: %+v", r)
	}
	if f, ok := findingOf(r, "overwatch:stalls:factorio-t2@todo"); ok {
		t.Errorf("factorio-t2 entered todo at 09:04 and is work: %q", f.Line)
	}
}

// C10: the project's schedule overrides the host's. The code-reviewer
// fires every five minutes on the host, and hourly in factorio.
func TestC10TheProjectScheduleOverridesTheHost(t *testing.T) {
	w := newStallsWorld(t)
	w.item("factorio-t1", machine.KindTicket, machine.InReview, at(8, 30))
	r := readStallsPart(t, w)
	if _, ok := findingOf(r, "overwatch:stalls:factorio-t1@in_review"); !ok {
		t.Fatalf("on the host's schedule factorio-t1 is not work: %+v", r)
	}
	w.configs["factorio"].Stages[config.StageCodeReviewer] = config.StageConfig{Schedule: "0 * * * *"}
	r = readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:factorio-t1@in_review"); ok {
		t.Fatalf("one hourly fire since 08:30 is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "factorio-t1"), "code-reviewer", "one fire")
}

// C10: the five exclusions each print a line that says why the item waits,
// and none is work.
func TestC10TheExclusionsSayWhyTheItemWaits(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, w *stallsWorld)
		says  []string
	}{
		{"only one fire passed", func(t *testing.T, w *stallsWorld) {
			w.item("factorio-t1", machine.KindTicket, machine.Todo, at(9, 0))
		}, []string{"one fire", "implementer"}},
		{"a run started after the item entered", func(t *testing.T, w *stallsWorld) {
			w.item("factorio-t1", machine.KindTicket, machine.Todo, at(8, 0))
			w.run(t, "r-0lderrun0000", "implementer", "factorio-t1", at(7, 0), at(7, 30))
			w.run(t, "r-a1b2c3d4e5f6", "implementer", "factorio-t1", at(8, 3), at(8, 40))
		}, []string{"r-a1b2c3d4e5f6"}},
		{"an open retry_limit alert", func(t *testing.T, w *stallsWorld) {
			w.item("factorio-t1", machine.KindTicket, machine.Repair, at(8, 0))
			w.exec(t, `INSERT INTO alerts (project, kind, dedupe_key, first_at, last_at) VALUES ('factorio', 'retry_limit', 'retry_limit:factorio/factorio-t1/implementer/ab12', 1, 1)`)
		}, []string{"retry_limit", "alert/1"}},
		{"an open run_blocked alert", func(t *testing.T, w *stallsWorld) {
			w.item("factorio-t1", machine.KindTicket, machine.InReview, at(8, 0))
			w.exec(t, `INSERT INTO alerts (project, kind, dedupe_key, first_at, last_at) VALUES ('factorio', 'run_blocked', 'run_blocked:factorio/factorio-t1/code-reviewer/ab12', 1, 1)`)
		}, []string{"run_blocked", "alert/1"}},
		{"a room's hold", func(t *testing.T, w *stallsWorld) {
			w.item("factorio-t1", machine.KindTicket, machine.Todo, at(8, 0))
			w.exec(t, `INSERT INTO holds (project, item, holder, kind, created_at) VALUES ('factorio', 'factorio-t1', 'room-r7', 'room', 1)`)
		}, []string{"room", "room-r7"}},
		{"project config disables the stage", func(t *testing.T, w *stallsWorld) {
			w.item("factorio-t1", machine.KindBug, machine.Reported, at(8, 0))
			off := false
			w.configs["factorio"].Stages[config.StageReproducer] = config.StageConfig{Enabled: &off}
		}, []string{"disables", "reproducer"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newStallsWorld(t)
			c.setup(t, w)
			r := readStallsPart(t, w)
			if len(r.Findings) != 0 {
				t.Fatalf("findings = %+v, want none", r.Findings)
			}
			mustContain(t, noteOf(t, r, "factorio-t1"), c.says...)
			if code := Sweep(context.Background(), w, stallsOnly(t)).Code; code != Quiet {
				t.Fatalf("the sweep's code = %d, want 0", code)
			}
		})
	}
}

// C10: what lifts an exclusion makes the item work again: a closed alert,
// an alert of another item, a hold of another item, and a run that started
// before the item entered.
func TestC10AnExclusionThatNoLongerHoldsIsWork(t *testing.T) {
	w := newStallsWorld(t)
	w.item("factorio-t1", machine.KindTicket, machine.Todo, at(8, 0))
	w.exec(t, `INSERT INTO alerts (project, kind, dedupe_key, state, first_at, last_at, closed_at) VALUES ('factorio', 'retry_limit', 'retry_limit:factorio/factorio-t1/implementer/ab12', 'closed', 1, 1, 2)`)
	w.exec(t, `INSERT INTO alerts (project, kind, dedupe_key, first_at, last_at) VALUES ('factorio', 'retry_limit', 'retry_limit:factorio/factorio-t10/implementer/ab12', 1, 1)`)
	w.exec(t, `INSERT INTO holds (project, item, holder, kind, created_at) VALUES ('factorio', 'factorio-t10', 'room-r7', 'room', 1)`)
	w.run(t, "r-0lderrun0000", "implementer", "factorio-t1", at(7, 0), at(7, 30))
	r := readStallsPart(t, w)
	if _, ok := findingOf(r, "overwatch:stalls:factorio-t1@todo"); !ok {
		t.Fatalf("factorio-t1 is not work: %+v", r)
	}
}

// C10: a project with no config yet runs no stage, so its items wait.
func TestC10AProjectWithNoConfigRunsNoStage(t *testing.T) {
	w := newStallsWorld(t)
	w.add("alpha", machine.Item{ID: "alpha-t1", Kind: machine.KindTicket, Status: machine.Todo, Updated: at(7, 0)}, "")
	r := readStallsPart(t, w)
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v", r.Findings)
	}
	mustContain(t, noteOf(t, r, "alpha-t1"), "implementer")
}

// C11: a ticket in in_progress that no lease holds and no live run names
// is work at once.
func TestC11LostWorkIsAStall(t *testing.T) {
	w := newStallsWorld(t)
	w.item("factorio-w1", machine.KindTicket, machine.InProgress, at(9, 4))
	w.item("factorio-w2", machine.KindTicket, machine.InProgress, at(9, 4))
	w.exec(t, `INSERT INTO leases (project, item, epoch, owner_run_id) VALUES ('factorio', 'factorio-w2', 3, NULL)`)
	w.run(t, "r-3nded000000", "implementer", "factorio-w2", at(8, 0), at(9, 0))
	r := readStallsPart(t, w)
	for _, item := range []string{"factorio-w1", "factorio-w2"} {
		key := "overwatch:stalls:" + item + "@in_progress"
		f, ok := findingOf(r, key)
		if !ok {
			t.Fatalf("no finding %s: %+v", key, r)
		}
		if !slices.Contains(Tokens(f.Line), key) {
			t.Errorf("line %q does not quote %s", f.Line, key)
		}
		mustContain(t, f.Line, item, "lease", "live run")
	}
}

// C11: a ticket in in_progress that a lease holds, or that a live run
// names, is not work.
func TestC11HeldWorkIsNotAStall(t *testing.T) {
	w := newStallsWorld(t)
	w.item("factorio-w1", machine.KindTicket, machine.InProgress, at(7, 0))
	w.exec(t, `INSERT INTO leases (project, item, epoch, owner_run_id, granted_at) VALUES ('factorio', 'factorio-w1', 1, 'r-0wner000000', 1)`)
	w.item("factorio-w2", machine.KindTicket, machine.InProgress, at(7, 0))
	w.run(t, "r-11ve00000000", "implementer", "factorio-w2", at(7, 0), time.Time{})
	r := readStallsPart(t, w)
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", r.Findings)
	}
}

// C10, F-36: the reproducer is a mutex stage and takes one bug per fire,
// oldest first. A fire that started a run on an older bug of the same
// queue is the queue draining, not a stall of the youngest bug. The
// reproducer fires every five minutes in factorio, and the clock reads
// 09:04.
func TestC10AFireThatTookAnOlderItemOfTheQueueIsNoStall(t *testing.T) {
	w := newStallsWorld(t)
	w.now = at(9, 4)
	w.configs["factorio"].Stages[config.StageReproducer] = config.StageConfig{Schedule: "*/5 * * * *"}
	// The coordinator files three bugs at 08:48, 08:49 and 08:50.
	w.item("factorio-b1", machine.KindBug, machine.Shaped, at(8, 57))
	w.entered(t, "factorio-b1", "", machine.Reported, at(8, 48))
	w.entered(t, "factorio-b1", machine.Reported, machine.Reproducing, at(8, 55))
	w.entered(t, "factorio-b1", machine.Reproducing, machine.Shaped, at(8, 57))
	w.item("factorio-b2", machine.KindBug, machine.Shaped, at(9, 2))
	w.entered(t, "factorio-b2", "", machine.Reported, at(8, 49))
	w.entered(t, "factorio-b2", machine.Reported, machine.Reproducing, at(9, 0))
	w.entered(t, "factorio-b2", machine.Reproducing, machine.Shaped, at(9, 2))
	w.item("factorio-b3", machine.KindBug, machine.Reported, at(8, 50))
	w.entered(t, "factorio-b3", "", machine.Reported, at(8, 50))
	// The fire at 08:55 takes b1, and the fire at 09:00 takes b2.
	w.run(t, "r-b1b1b1b1b1b1", config.StageReproducer, "factorio-b1", at(8, 55), at(8, 57))
	w.run(t, "r-b2b2b2b2b2b2", config.StageReproducer, "factorio-b2", at(9, 0), at(9, 2))
	r := readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:factorio-b3@reported"); ok {
		t.Fatalf("factorio-b3 waits behind two older bugs that each fire took, and is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "factorio-b3"), "reproducer")
}

// queueWorld is the F-36 queue at 09:04 with the reproducer firing every
// five minutes: factorio-b3 entered reported at 08:50, and factorio-b1
// entered it at 08:48. The fire at 08:55 takes b1.
func queueWorld(t *testing.T) *stallsWorld {
	t.Helper()
	w := newStallsWorld(t)
	w.now = at(9, 4)
	w.configs["factorio"].Stages[config.StageReproducer] = config.StageConfig{Schedule: "*/5 * * * *"}
	w.item("factorio-b1", machine.KindBug, machine.Shaped, at(8, 57))
	w.entered(t, "factorio-b1", "", machine.Reported, at(8, 48))
	w.entered(t, "factorio-b1", machine.Reported, machine.Reproducing, at(8, 55))
	w.entered(t, "factorio-b1", machine.Reproducing, machine.Shaped, at(8, 57))
	w.item("factorio-b3", machine.KindBug, machine.Reported, at(8, 50))
	w.entered(t, "factorio-b3", "", machine.Reported, at(8, 50))
	w.run(t, "r-b1b1b1b1b1b1", config.StageReproducer, "factorio-b1", at(8, 55), at(8, 57))
	return w
}

// C10, F-36: the note of an item that waits behind its queue says that the
// stage's fires took older items of the queue, and names each run.
func TestC10TheNoteSaysTheFiresTookOlderItemsOfTheQueue(t *testing.T) {
	w := queueWorld(t)
	w.item("factorio-b2", machine.KindBug, machine.Shaped, at(9, 2))
	w.entered(t, "factorio-b2", "", machine.Reported, at(8, 49))
	w.entered(t, "factorio-b2", machine.Reported, machine.Reproducing, at(9, 0))
	w.run(t, "r-b2b2b2b2b2b2", config.StageReproducer, "factorio-b2", at(9, 0), at(9, 2))
	r := readStallsPart(t, w)
	if len(r.Findings) != 0 {
		t.Fatalf("findings = %+v, want none", r.Findings)
	}
	mustContain(t, noteOf(t, r, "factorio-b3"), "reproducer", "no fire", "older items of its queue",
		"r-b1b1b1b1b1b1", "factorio-b1", "r-b2b2b2b2b2b2", "factorio-b2")
}

// C10, F-36: a fire whose run took an item that entered the queue after
// this one counts against it. factorio-b4 entered reported at 08:51, after
// factorio-b3, and the fire at 09:00 takes it.
func TestC10AFireThatTookAYoungerItemOfTheQueueCounts(t *testing.T) {
	w := queueWorld(t)
	w.item("factorio-b4", machine.KindBug, machine.Shaped, at(9, 2))
	w.entered(t, "factorio-b4", "", machine.Reported, at(8, 51))
	w.entered(t, "factorio-b4", machine.Reported, machine.Reproducing, at(9, 0))
	w.run(t, "r-b4b4b4b4b4b4", config.StageReproducer, "factorio-b4", at(9, 0), at(9, 2))
	r := readStallsPart(t, w)
	if _, ok := findingOf(r, "overwatch:stalls:factorio-b3@reported"); ok {
		t.Fatalf("one fire counts against factorio-b3, and it is work: %+v", r.Findings)
	}
	mustContain(t, noteOf(t, r, "factorio-b3"), "reproducer", "only one fire")

	// A fire whose runs took an older item and a younger one counts too.
	w = queueWorld(t)
	w.item("factorio-b4", machine.KindBug, machine.Shaped, at(8, 58))
	w.entered(t, "factorio-b4", "", machine.Reported, at(8, 51))
	w.run(t, "r-b4b4b4b4b4b4", config.StageReproducer, "factorio-b4", at(8, 56), at(8, 58))
	w.item("factorio-b5", machine.KindBug, machine.Shaped, at(9, 2))
	w.entered(t, "factorio-b5", "", machine.Reported, at(8, 52))
	w.run(t, "r-b5b5b5b5b5b5", config.StageReproducer, "factorio-b5", at(9, 0), at(9, 2))
	r = readStallsPart(t, w)
	f, ok := findingOf(r, "overwatch:stalls:factorio-b3@reported")
	if !ok {
		t.Fatalf("two fires took younger items, and factorio-b3 is not work: %+v", r)
	}
	// The fire at 08:55 also took factorio-b1, so the line says what counted.
	mustContain(t, f.Line, "factorio-b3", "two fires of reproducer", "neither started a run on it",
		"neither took only older items of its queue")
	if strings.Contains(f.Line, "r-b1b1b1b1b1b1") {
		t.Errorf("line %q names a run of a fire that counts", f.Line)
	}
}

// C10, F-36: a fire that started no run still counts after a fire that took
// an older item. The fire at 08:55 takes factorio-b1, and the fires at 09:00
// and 09:05 start no run.
func TestC10AFireWithNoRunCountsAfterTheQueueDrained(t *testing.T) {
	w := queueWorld(t)
	r := readStallsPart(t, w)
	if f, ok := findingOf(r, "overwatch:stalls:factorio-b3@reported"); ok {
		t.Fatalf("at 09:04 one fire counts against factorio-b3, and it is work: %q", f.Line)
	}
	mustContain(t, noteOf(t, r, "factorio-b3"), "only one fire", "r-b1b1b1b1b1b1")
	w.now = at(9, 6)
	r = readStallsPart(t, w)
	f, ok := findingOf(r, "overwatch:stalls:factorio-b3@reported")
	if !ok {
		t.Fatalf("at 09:06 two fires count against factorio-b3, and it is not work: %+v", r)
	}
	// Three fires since 08:50, and the one that took factorio-b1 does not
	// count, so the line names it and does not say the stage fired twice.
	mustContain(t, f.Line, "factorio-b3", "two fires of reproducer", "neither started a run on it",
		"neither took only older items of its queue", "took only older items of its queue started run r-b1b1b1b1b1b1 on factorio-b1")
	if strings.Contains(f.Line, "fired twice") {
		t.Errorf("line %q says the stage fired twice, and it fired three times", f.Line)
	}
}

// C10, F-36: a run on an item that no transition into reported dates
// counts as a run on a younger item, so missing data never hides a stall.
// factorio-b0 holds no transition, and the fires at 08:55 and 09:00 take it.
func TestC10ARunOnAnUndatedItemOfTheQueueCounts(t *testing.T) {
	w := newStallsWorld(t)
	w.now = at(9, 4)
	w.configs["factorio"].Stages[config.StageReproducer] = config.StageConfig{Schedule: "*/5 * * * *"}
	w.item("factorio-b0", machine.KindBug, machine.Shaped, at(9, 2))
	w.item("factorio-b3", machine.KindBug, machine.Reported, at(8, 50))
	w.entered(t, "factorio-b3", "", machine.Reported, at(8, 50))
	w.run(t, "r-b0b0b0b0b0b0", config.StageReproducer, "factorio-b0", at(8, 55), at(8, 57))
	w.run(t, "r-b0b0b0b0b0b1", config.StageReproducer, "factorio-b0", at(9, 0), at(9, 2))
	r := readStallsPart(t, w)
	if _, ok := findingOf(r, "overwatch:stalls:factorio-b3@reported"); !ok {
		t.Fatalf("two fires took an undated item, and factorio-b3 is not work: %+v", r)
	}
}
