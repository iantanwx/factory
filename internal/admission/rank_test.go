package admission

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// rankNow is the fake clock of the rank tests.
// It reads a time before any real bead write of a test, so the last bead
// write of an undated ticket is no earlier than any move the test records.
var rankNow = time.UnixMilli(1_700_000_000_000)

// rankWorld is a world whose admission reads the fake clock rankNow.
func rankWorld(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	w.a.cfg.Now = func() time.Time { return rankNow }
	return w
}

// fact sets one loop fact on an item, as a machine move would.
func (w *world) fact(item, name, value string) {
	w.t.Helper()
	is, err := w.bx.Get(context.Background(), item)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.bx.Update(context.Background(), item, beadsx.UpdateSpec{IfStatus: is.Status,
		SetMetadata: map[string]string{"loop." + name: value}}); err != nil {
		w.t.Fatal(err)
	}
}

// history records one applied move of item at the given time. Its seq
// starts past any seq the machine gives a test item.
func (w *world) history(item, verb, from, to string, at time.Time) {
	w.t.Helper()
	if err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		var seq int64
		if err := tx.QueryRow(`SELECT coalesce(max(seq), 1000) + 1 FROM intents WHERE project = ? AND item = ?`, prj, item).Scan(&seq); err != nil {
			return err
		}
		r, err := tx.Exec(`INSERT INTO intents (project, item, seq, kind, verb, epoch, state, created_at)
			VALUES (?, ?, ?, 'ticket', ?, 0, 'applied', ?)`, prj, item, seq, verb, at.UnixMilli())
		if err != nil {
			return err
		}
		id, err := r.LastInsertId()
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO transitions (intent_id, project, item, seq, kind, verb, from_status, to_status, epoch, at)
			VALUES (?, ?, ?, ?, 'ticket', ?, ?, ?, 0, ?)`, id, prj, item, seq, verb, from, to, at.UnixMilli())
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// blocks records that each of waiters waits on item through a blocks edge.
func (w *world) blocks(item string, waiters ...string) {
	w.t.Helper()
	for _, from := range waiters {
		if err := w.bx.AddDependency(context.Background(), from, item, beadsx.DepBlocks); err != nil {
			w.t.Fatal(err)
		}
	}
}

// setOverdue writes host config with the implementer's overdue wait.
func (w *world) setOverdue(d time.Duration) {
	w.t.Helper()
	cfg := config.DefaultHost("/var/lib/loop")
	e := cfg.Throughput[config.StageImplementer]
	e.Overdue = d
	cfg.Throughput[config.StageImplementer] = e
	data, err := yaml.Marshal(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(w.hostPath, data, 0o600); err != nil {
		w.t.Fatal(err)
	}
}

// peekWant peeks the implementer's work query and fails unless it names want.
func (w *world) peekWant(want, why string) {
	w.t.Helper()
	c, r := w.a.Peek(context.Background(), prj, machine.StageImplementer)
	if r != nil || c == nil || c.Item != want {
		w.t.Fatalf("peek = %+v %+v, want %s: %s", c, r, want, why)
	}
}

// C21. The implementer takes work in rank order: priority 1, repair,
// overdue, resume, rework, the rest. Each ticket is made in the reverse of
// that order, so the oldest and the lowest id belong to the last class, and
// the overdue ticket waited in todo longest of all.
func TestC21ImplementerTakesWorkInRankOrder(t *testing.T) {
	w := rankWorld(t)
	newTicket := w.ticket(machine.Todo)
	w.fact(newTicket, machine.FactEntry, machine.EntryNew)
	rework := w.ticket(machine.Todo)
	w.fact(rework, machine.FactEntry, machine.EntryRework)
	resume := w.ticket(machine.Todo)
	w.fact(resume, machine.FactEntry, machine.EntryResume)
	overdue := w.ticket(machine.Todo)
	w.fact(overdue, machine.FactEntry, machine.EntryNew)
	w.history(overdue, machine.VerbCreate, machine.Initial, machine.Todo, rankNow.Add(-2*time.Hour))
	repair := w.ticket(machine.Repair)
	priority1 := w.ticket(machine.Todo)
	w.fact(priority1, machine.FactEntry, machine.EntryNew)
	w.fact(priority1, machine.FactPriority, "1")

	for _, step := range []struct{ item, class string }{
		{priority1, "priority 1"}, {repair, "repair"}, {overdue, "overdue"},
		{resume, "resume"}, {rework, "rework"}, {newTicket, "the rest"},
	} {
		w.peekWant(step.item, "the "+step.class+" ticket is the first of the classes left")
		w.mustAdmit(machine.StageImplementer, step.item)
	}
}

// C21. A ticket in repair comes before a new ticket, though the new ticket
// is older and has the lower id.
func TestC21RepairComesBeforeANewTicket(t *testing.T) {
	w := rankWorld(t)
	newTicket := w.ticket(machine.Todo)
	w.fact(newTicket, machine.FactEntry, machine.EntryNew)
	w.history(newTicket, machine.VerbCreate, machine.Initial, machine.Todo, rankNow.Add(-time.Minute))
	repair := w.ticket(machine.Repair)
	w.peekWant(repair, "a repair comes before a new ticket")
}

// C22. Inside a class, the ticket that more backlog tickets wait on goes
// first, at any depth of blocks edges. b1 waits on deep, b2 on b1 and b3 on
// b2, so deep counts three. Two backlog tickets wait on shallow directly, so
// it counts two. shallow is older and has the lower id. An edge out of a
// ticket in any state but backlog, and an edge of another kind, counts
// nothing.
func TestC22MostBlockingTicketGoesFirst(t *testing.T) {
	w := rankWorld(t)
	shallow := w.ticket(machine.Todo)
	w.history(shallow, machine.VerbCreate, machine.Initial, machine.Todo, rankNow.Add(-time.Hour))
	deep := w.ticket(machine.Todo)
	w.history(deep, machine.VerbCreate, machine.Initial, machine.Todo, rankNow.Add(-time.Minute))
	b1, b2, b3 := w.ticket(machine.Backlog), w.ticket(machine.Backlog), w.ticket(machine.Backlog)
	w.blocks(deep, b1)
	w.blocks(b1, b2)
	w.blocks(b2, b3)
	w.blocks(shallow, w.ticket(machine.Backlog), w.ticket(machine.Backlog))
	for _, other := range []string{w.ticket(machine.InReview), w.ticket(machine.Escalated)} {
		w.blocks(shallow, other)
	}
	related := w.ticket(machine.Backlog)
	if err := w.bx.AddDependency(context.Background(), related, shallow, beadsx.DepRelated); err != nil {
		t.Fatal(err)
	}
	w.peekWant(deep, "three backlog tickets wait on it through a chain, and two on the other")
	w.mustAdmit(machine.StageImplementer, deep)
	w.peekWant(shallow, "it is the one ticket left")
}

// C22. On equal blocked counts, the earlier create move goes first, then the
// lower id. A ticket with no create move sorts as the oldest. older entered
// todo last, so only the create move puts it before low and high.
func TestC22OnEqualCountsTheOldestGoesFirst(t *testing.T) {
	w := rankWorld(t)
	low := w.ticket(machine.Todo)
	w.history(low, machine.VerbCreate, machine.Initial, machine.Todo, rankNow.Add(-time.Minute))
	high := w.ticket(machine.Todo)
	w.history(high, machine.VerbCreate, machine.Initial, machine.Todo, rankNow.Add(-time.Minute))
	older := w.ticket(machine.Todo)
	w.history(older, machine.VerbCreate, machine.Initial, machine.Backlog, rankNow.Add(-time.Hour))
	w.history(older, machine.VerbUnblock, machine.Backlog, machine.Todo, rankNow.Add(-time.Second))
	undated := w.ticket(machine.Todo)
	w.peekWant(undated, "a ticket with no create move sorts as the oldest")
	w.mustAdmit(machine.StageImplementer, undated)
	w.peekWant(older, "its create move is the earliest")
	w.mustAdmit(machine.StageImplementer, older)
	w.peekWant(low, "both were made in one millisecond, and its id is the lower")
}

// C23. A ticket becomes overdue at exactly the configured wait in todo, and
// not one millisecond before. The clock starts at the latest move into todo.
func TestC23OverdueStartsAtTheConfiguredWait(t *testing.T) {
	for _, c := range []struct {
		name string
		set  time.Duration // 0 leaves the key out
		wait time.Duration
	}{
		{"no overdue key", 0, 90 * time.Minute},
		{"overdue of 30m", 30 * time.Minute, 30 * time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, at := range []struct {
				waited  time.Duration
				overdue bool
			}{{c.wait - time.Millisecond, false}, {c.wait, true}} {
				w := rankWorld(t)
				if c.set != 0 {
					w.setOverdue(c.set)
				}
				resume := w.ticket(machine.Todo)
				w.fact(resume, machine.FactEntry, machine.EntryResume)
				waiting := w.ticket(machine.Todo)
				w.fact(waiting, machine.FactEntry, machine.EntryRework)
				w.history(waiting, machine.VerbCreate, machine.Initial, machine.Todo, rankNow.Add(-24*time.Hour))
				w.history(waiting, "claim", machine.Todo, machine.InProgress, rankNow.Add(-23*time.Hour))
				w.history(waiting, "review_reject", machine.InReview, machine.Todo, rankNow.Add(-at.waited))
				if at.overdue {
					w.peekWant(waiting, "it waited in todo for exactly "+at.waited.String())
				} else {
					w.peekWant(resume, "the rework waited in todo for only "+at.waited.String())
				}
			}
		})
	}
}

// C24. One implementer peek reads the edges of each backlog ticket once,
// and of no ticket in any other state.
func TestC24RankBeadsReadsAreBounded(t *testing.T) {
	w := rankWorld(t)
	target := w.ticket(machine.Todo)
	other := w.ticket(machine.Todo)
	w.history(other, machine.VerbCreate, machine.Initial, machine.Todo, rankNow.Add(-time.Hour))
	w.ticket(machine.Repair)
	w.fact(w.ticket(machine.Todo), machine.FactEntry, machine.EntryNew)
	var backlog []string
	for range 20 {
		backlog = append(backlog, w.ticket(machine.Backlog))
	}
	w.blocks(target, backlog[0])
	for i := 1; i < len(backlog); i++ {
		w.blocks(backlog[i-1], backlog[i])
	}
	for _, s := range []string{machine.InProgress, machine.InReview, machine.Escalated, machine.Done, machine.Canceled} {
		w.ticket(s)
	}
	w.beads.countDeps()
	c, r := w.a.Peek(context.Background(), prj, machine.StageImplementer)
	if r != nil || c == nil || c.Status != machine.Repair {
		t.Fatalf("peek = %+v %+v, want the repair ticket", c, r)
	}
	calls := w.beads.depCalls()
	for _, id := range backlog {
		if calls[id] != 1 {
			t.Errorf("Dependencies(%s) ran %d times, want once for a backlog ticket", id, calls[id])
		}
	}
	for id, n := range calls {
		if !slices.Contains(backlog, id) {
			t.Errorf("Dependencies(%s) ran %d times, want none for a ticket outside backlog", id, n)
		}
	}
	w.mustAdmit(machine.StageImplementer, c.Item)
	w.peekWant(target, "twenty backlog tickets wait on it through one chain")
}
