package overwatch

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/sched"
)

// reachWorld is the fake world with the reads of the inbox and server
// parts: the coordinator, the agent host inventory and the doctor.
type reachWorld struct {
	*fakeWorld
	coord     sched.CoordinatorState
	inv       agenthost.Inventory
	invErr    error
	lines     []github.CheckLine
	doctorErr error
}

func (w *reachWorld) Coordinators() map[string]sched.CoordinatorState {
	return map[string]sched.CoordinatorState{w.coord.Project: w.coord}
}
func (w *reachWorld) Inventory(context.Context) (agenthost.Inventory, error) {
	return w.inv, w.invErr
}
func (w *reachWorld) Doctor(context.Context) ([]github.CheckLine, error) {
	return w.lines, w.doctorErr
}

var _ World = (*reachWorld)(nil)

// coordinatorSession is the coordinator's session in every inbox test.
var coordinatorSession = agenthost.Session{ID: agenthost.RunID{Host: "orca", Terminal: "t-3", Incarnation: "i1"}, Kind: agenthost.KindCoordinator}

// newReachWorld is a world whose coordinator session runs and is
// connected.
func newReachWorld(t *testing.T) *reachWorld {
	t.Helper()
	s := coordinatorSession
	return &reachWorld{
		fakeWorld: newFakeWorld(t),
		coord:     sched.CoordinatorState{RunID: "coordinator-1", Project: "factorio", Session: &s},
		inv:       agenthost.Inventory{Complete: true, Epoch: "e1", Rows: []agenthost.Row{{ID: s.ID, Connected: true}}},
	}
}

// record writes the coordinator_ring row of the destination "".
func (w *reachWorld) record(t *testing.T, o RingOutcome) {
	t.Helper()
	if err := RecordRing(context.Background(), w.st, []string{""}, o); err != nil {
		t.Fatal(err)
	}
}

// sweepInbox sweeps the inbox part alone, and returns its report.
func sweepInbox(t *testing.T, w World) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{inboxPart()})
	if len(r.Parts) != 1 || r.Parts[0].Name != "inbox" {
		t.Fatalf("report = %+v", r)
	}
	return r.Parts[0]
}

var ringAt = time.Date(2026, 10, 24, 8, 53, 0, 0, time.UTC)

// C18: inbox holds work when the agent host inventory shows no live
// coordinator session, or when the coordinator_ring row holds refused. An
// accepted row and a live session read code 0. No row yet reads code 0,
// with one line that says so.
func TestC18AnUnreachableCoordinatorIsWork(t *testing.T) {
	session := "the coordinator session orca/t-3/i1 of run coordinator-1 runs"
	cases := []struct {
		name  string
		setup func(w *reachWorld)
		ring  *RingOutcome
		want  PartReport
	}{
		{name: "no row yet", want: PartReport{Name: "inbox", Code: Quiet, Lines: []string{
			session,
			"the store holds no coordinator ring yet",
		}}},
		{name: "accepted", ring: &RingOutcome{At: ringAt, Source: SourceOverwatch, Result: RingAccepted},
			want: PartReport{Name: "inbox", Code: Quiet, Lines: []string{
				session,
				"the last ring, from overwatch at 2026-10-24T08:53:00Z, was accepted",
			}}},
		{name: "nobody, and a session runs now", ring: &RingOutcome{At: ringAt, Source: SourceInbox, Result: RingNobody},
			want: PartReport{Name: "inbox", Code: Quiet, Lines: []string{
				session,
				"the last ring, from inbox at 2026-10-24T08:53:00Z, found no coordinator session",
			}}},
		{name: "refused", ring: &RingOutcome{At: ringAt, Source: SourceInbox, Result: RingRefused, Detail: "orca: notify refused: terminal t-3 is closed"},
			want: PartReport{Name: "inbox", Code: Work, Lines: []string{
				"the last ring, from inbox at 2026-10-24T08:53:00Z, was refused: orca: notify refused: terminal t-3 is closed (overwatch:inbox:refused)",
				session,
			}}},
		{name: "no session", setup: func(w *reachWorld) { w.coord.Session = nil },
			ring: &RingOutcome{At: ringAt, Source: SourceOverwatch, Result: RingNobody},
			want: PartReport{Name: "inbox", Code: Work, Lines: []string{
				"the scheduler holds no coordinator session, so no ring reaches the coordinator (overwatch:inbox:no-session)",
				"the last ring, from overwatch at 2026-10-24T08:53:00Z, found no coordinator session",
			}}},
		{name: "a session the inventory does not list", setup: func(w *reachWorld) { w.inv.Rows = nil },
			ring: &RingOutcome{At: ringAt, Source: SourceOverwatch, Result: RingAccepted},
			want: PartReport{Name: "inbox", Code: Work, Lines: []string{
				"the agent host lists no live session orca/t-3/i1 for the coordinator run coordinator-1 (overwatch:inbox:no-session)",
				"the last ring, from overwatch at 2026-10-24T08:53:00Z, was accepted",
			}}},
		{name: "a session that is not connected", setup: func(w *reachWorld) { w.inv.Rows[0].Connected = false },
			want: PartReport{Name: "inbox", Code: Work, Lines: []string{
				"the agent host lists no live session orca/t-3/i1 for the coordinator run coordinator-1 (overwatch:inbox:no-session)",
				"the store holds no coordinator ring yet",
			}}},
		{name: "no session and a refused ring", setup: func(w *reachWorld) { w.inv.Rows = nil },
			ring: &RingOutcome{At: ringAt, Source: SourceOverwatch, Result: RingRefused, Detail: "orca: notify refused"},
			want: PartReport{Name: "inbox", Code: Work, Lines: []string{
				"the agent host lists no live session orca/t-3/i1 for the coordinator run coordinator-1 (overwatch:inbox:no-session)",
				"the last ring, from overwatch at 2026-10-24T08:53:00Z, was refused: orca: notify refused (overwatch:inbox:refused)",
			}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newReachWorld(t)
			if c.setup != nil {
				c.setup(w)
			}
			if c.ring != nil {
				w.record(t, *c.ring)
			}
			if got := sweepInbox(t, w); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("inbox =\n%#v\nwant\n%#v", got, c.want)
			}
		})
	}
}

// Spec factorio-1edo work entry 5: the inbox part reads the ring record of
// each destination. A refused ring of one destination is work under a key
// that names it, and an accepted ring of another is a note.
func TestC18TheRingOfEachDestinationIsRead(t *testing.T) {
	w := newReachWorld(t)
	ctx := context.Background()
	if err := RecordRing(ctx, w.st, []string{"alpha"}, RingOutcome{At: ringAt, Source: SourceInbox, Result: RingAccepted}); err != nil {
		t.Fatal(err)
	}
	if err := RecordRing(ctx, w.st, []string{"beta"}, RingOutcome{At: ringAt, Source: SourceInbox, Result: RingRefused, Detail: "orca: notify refused"}); err != nil {
		t.Fatal(err)
	}
	want := PartReport{Name: "inbox", Code: Work, Lines: []string{
		"the last ring for beta, from inbox at 2026-10-24T08:53:00Z, was refused: orca: notify refused (overwatch:inbox:refused:beta)",
		"the coordinator session orca/t-3/i1 of run coordinator-1 runs",
		"the last ring for alpha, from inbox at 2026-10-24T08:53:00Z, was accepted",
	}}
	if got := sweepInbox(t, w); !reflect.DeepEqual(got, want) {
		t.Fatalf("inbox =\n%#v\nwant\n%#v", got, want)
	}
}

// C18: a fault that names a key ends that finding, as for every part.
func TestC18AFaultThatNamesTheKeyFilesIt(t *testing.T) {
	w := newReachWorld(t)
	w.record(t, RingOutcome{At: ringAt, Source: SourceOverwatch, Result: RingRefused, Detail: "orca: notify refused"})
	id := w.fault(t, "orca refuses each ring: overwatch:inbox:refused.", "open")
	got := sweepInbox(t, w)
	if got.Code != Quiet || len(got.Lines) == 0 || got.Lines[0] != "the last ring, from overwatch at 2026-10-24T08:53:00Z, was refused: orca: notify refused (overwatch:inbox:refused) (filed in "+id+")" {
		t.Fatalf("inbox = %#v", got)
	}
}

// C18: an inventory that cannot be read makes the part unread, not quiet.
func TestC18AnUnreadInventoryIsUnread(t *testing.T) {
	w := newReachWorld(t)
	w.invErr = errors.New("orca: serve is not running")
	if got := sweepInbox(t, w); got.Code != Unread || len(got.Lines) != 1 || got.Lines[0] != "UNREAD: orca: serve is not running" {
		t.Fatalf("inbox = %#v", got)
	}
}

// The ticket's done-when: a refused ring recorded at one tick makes the
// next tick's inbox part hold work, and that tick rings for inbox.
func TestARefusedRingAtOneTickIsWorkAtTheNext(t *testing.T) {
	w := newReachWorld(t)
	stalls := Key("stalls", "factorio-x1y@in_progress")
	var texts []string
	refused := func(ctx context.Context, _ agenthost.RunID, _ bool, _ []string, text string) error {
		texts = append(texts, text)
		err := errors.New("orca: notify refused")
		if rerr := RecordRing(ctx, w.st, []string{""}, RingOutcome{At: w.now, Source: SourceOverwatch, Result: RingRefused, Detail: err.Error()}); rerr != nil {
			t.Fatal(rerr)
		}
		return err
	}
	j := &Job{World: w, Ring: refused, Parts: []Part{inboxPart(), keyPart("stalls", stalls)}}
	if err := j.Tick(context.Background()); err == nil {
		t.Fatal("a refused ring returned no error")
	}
	first, _ := j.Last()
	if first.Parts[1].Name != "inbox" || first.Parts[1].Code != Quiet {
		t.Fatalf("the first tick's inbox = %+v", first.Parts[1])
	}
	j.Parts = []Part{inboxPart()}
	w.now = w.now.Add(10 * time.Minute)
	_ = j.Tick(context.Background())
	second, _ := j.Last()
	if second.Code != Work || second.Parts[0].Code != Work || len(second.Parts[0].Lines) == 0 || second.Parts[0].Lines[0] !=
		"the last ring, from overwatch at 2026-10-24T09:03:00Z, was refused: orca: notify refused (overwatch:inbox:refused)" {
		t.Fatalf("the second tick = %+v", second)
	}
	want := []string{
		"loop overwatch: work in stalls. Run `loop overwatch sweep`, then follow section 1 of your runbook.",
		"loop overwatch: work in inbox. Run `loop overwatch sweep`, then follow section 1 of your runbook.",
	}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("rings = %q", texts)
	}
}

// The shipped part list holds inbox and server, with their commands.
func TestPartsHoldTheReachabilityParts(t *testing.T) {
	got := map[string]string{}
	for _, p := range Parts() {
		got[p.Name] = p.Command
	}
	if cmd, ok := got["inbox"]; !ok || cmd != "" {
		t.Errorf("Parts() inbox = %q %v", cmd, ok)
	}
	if cmd, ok := got["server"]; !ok || cmd != "loop doctor" {
		t.Errorf("Parts() server = %q %v", cmd, ok)
	}
}
