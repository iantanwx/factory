package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// rang is one ring of the fake doorbell.
type rang struct {
	session string // the terminal of the session it rang
	dests   []string
	text    string
}

// notifyWorld is an inbox whose topology and sessions a test sets, and a
// Notifier whose doorbell records each ring by session. A session is
// named by its terminal.
type notifyWorld struct {
	*routeWorld
	topo     coord.Topology
	sessions func(project string) (string, bool)
	rings    []rang
	// during runs inside each ring, before the doorbell answers.
	during func()
	n      *Notifier
}

func newNotifyWorld(t *testing.T) *notifyWorld {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	w := &notifyWorld{sessions: func(string) (string, bool) { return "", false }}
	in := New(Config{Store: st,
		Topology: func() coord.Topology { return w.topo },
		Sessions: func(project string) (agenthost.RunID, bool) {
			term, ok := w.sessions(project)
			if !ok {
				return agenthost.RunID{}, false
			}
			return agenthost.RunID{Host: "fake", Terminal: term, Incarnation: "1"}, true
		},
	})
	w.routeWorld = &routeWorld{t: t, ctx: ctx, st: st, in: in}
	w.n = w.notifier()
	return w
}

// notifier is a second Notifier over the same inbox and doorbell, as a
// second server process holds.
func (w *notifyWorld) notifier() *Notifier {
	return &Notifier{Inbox: w.in, Ring: func(_ context.Context, id agenthost.RunID, dests []string, text string) error {
		w.rings = append(w.rings, rang{session: id.Terminal, dests: dests, text: text})
		if during := w.during; during != nil {
			w.during = nil
			during()
		}
		return nil
	}}
}

// serve names the session of each project from a map. A project the map
// does not name has no session.
func (w *notifyWorld) serve(m map[string]string) {
	w.sessions = func(p string) (string, bool) {
		s, ok := m[p]
		return s, ok
	}
}

func (w *notifyWorld) poke() {
	w.t.Helper()
	if err := w.n.Poke(w.ctx); err != nil {
		w.t.Fatal(err)
	}
}

// alertAt raises an alert of project that entered the inbox at at, and
// returns its entry.
func (w *notifyWorld) alertAt(project string, at int64) Entry {
	w.t.Helper()
	dedupe := fmt.Sprintf("run_crashed:%s-%d", project, at)
	w.alert(project, "run_crashed", dedupe)
	if err := w.st.Write(w.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE alerts SET first_at = ?, last_at = ? WHERE dedupe_key = ?`, at, at, dedupe)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	es, err := w.in.Entries(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, e := range es {
		if e.Dedupe == dedupe {
			return e
		}
	}
	w.t.Fatalf("the alert %s does not list", dedupe)
	return Entry{}
}

// cursor reads the home_notify cursor of dest. ok is false with no row.
func (w *notifyWorld) cursor(dest string) (at int64, ok bool) {
	w.t.Helper()
	err := w.st.Read(w.ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT due_at FROM schedule WHERE kind = 'home_notify' AND project = ?`, dest).Scan(&at)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		w.t.Fatal(err)
	}
	return at, true
}

func (w *notifyWorld) wantRings(want ...rang) {
	w.t.Helper()
	if !slices.EqualFunc(w.rings, want, func(a, b rang) bool {
		return a.session == b.session && slices.Equal(a.dests, b.dests) && a.text == b.text
	}) {
		w.t.Fatalf("rings =\n%q\nwant\n%q", w.rings, want)
	}
	w.rings = nil
}

// twoHomes is a topology where a and b each have their own coordinator.
var twoHomes = coord.Topology{Loop: "loop", Homes: map[string]bool{"a": true, "b": true, "loop": true}}

// C13: two projects each have one session. An entry of a rings a's
// session, and b's session gets no ring.
func TestC13AnEntryRingsTheSessionOfItsProjectOnly(t *testing.T) {
	w := newNotifyWorld(t)
	w.topo = twoHomes
	w.serve(map[string]string{"a": "coord-a", "b": "coord-b", "loop": "coord-loop"})
	e := w.alertAt("a", 100)
	w.poke()
	w.wantRings(rang{session: "coord-a", dests: []string{"a"}, text: RingText([]Entry{e})})
	if at, ok := w.cursor("a"); !ok || at != 100 {
		t.Fatalf("a's cursor = %d %v, want 100", at, ok)
	}
	if _, ok := w.cursor("b"); ok {
		t.Fatal("b's cursor moved with no ring to b")
	}
}

// C13 and the done-means of spec work entry 5: with one coordinator for
// every project, a poke over entries of two projects rings that session
// once, and the ring names both entries.
func TestC13OneSessionForEveryProjectGetsOneRing(t *testing.T) {
	for _, c := range []struct {
		name string
		topo coord.Topology
		dest []string
	}{
		// Before work entry 7: the old session serves a, and b routes to
		// the destination "" of a host with no loop project.
		{name: "the old session", topo: coord.Topology{Old: "a"}, dest: []string{"", "a"}},
		{name: "two homes", topo: twoHomes, dest: []string{"a", "b"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newNotifyWorld(t)
			w.topo = c.topo
			w.sessions = func(string) (string, bool) { return "coord-1", true }
			a, b := w.alertAt("a", 100), w.alertAt("b", 200)
			w.poke()
			w.wantRings(rang{session: "coord-1", dests: c.dest, text: RingText([]Entry{a, b})})
			w.poke()
			w.wantRings()
		})
	}
}

// C13: a destination with no session keeps its cursor, and the first
// ring after its session starts names the entry.
func TestC13ADestinationWithNoSessionKeepsItsCursor(t *testing.T) {
	w := newNotifyWorld(t)
	w.topo = twoHomes
	w.serve(map[string]string{"a": "coord-a"})
	a, b := w.alertAt("a", 100), w.alertAt("b", 200)
	w.poke()
	w.wantRings(rang{session: "coord-a", dests: []string{"a"}, text: RingText([]Entry{a})})
	if _, ok := w.cursor("b"); ok {
		t.Fatal("b's cursor moved while no session served b")
	}

	w.serve(map[string]string{"a": "coord-a", "b": "coord-b"})
	w.poke()
	w.wantRings(rang{session: "coord-b", dests: []string{"b"}, text: RingText([]Entry{b})})
}

// C13: a refused ring keeps the cursors of its session, and the other
// sessions of the poke still ring.
func TestC13ARefusedRingKeepsItsCursors(t *testing.T) {
	w := newNotifyWorld(t)
	w.topo = twoHomes
	w.serve(map[string]string{"a": "coord-a", "b": "coord-b"})
	a, b := w.alertAt("a", 100), w.alertAt("b", 200)
	w.n.Ring = func(_ context.Context, id agenthost.RunID, dests []string, text string) error {
		w.rings = append(w.rings, rang{session: id.Terminal, dests: dests, text: text})
		if id.Terminal == "coord-a" {
			return errors.New("the host refused input")
		}
		return nil
	}
	if err := w.n.Poke(w.ctx); err == nil {
		t.Fatal("a poke with a refused ring returned no error")
	}
	w.wantRings(rang{session: "coord-a", dests: []string{"a"}, text: RingText([]Entry{a})},
		rang{session: "coord-b", dests: []string{"b"}, text: RingText([]Entry{b})})
	if _, ok := w.cursor("a"); ok {
		t.Fatal("a's cursor moved after a refused ring")
	}
	if at, ok := w.cursor("b"); !ok || at != 200 {
		t.Fatalf("b's cursor = %d %v, want 200", at, ok)
	}
}

// C14: with an inbox_notify cursor at T and entries before and after T,
// the first poke rings each destination with the entries after T only.
func TestC14TheFirstPokeRingsOnlyTheEntriesAfterTheOldCursor(t *testing.T) {
	w := newNotifyWorld(t)
	w.topo = twoHomes
	w.serve(map[string]string{"a": "coord-a", "b": "coord-b"})
	w.alertAt("a", 90)
	a := w.alertAt("a", 110)
	w.alertAt("b", 95)
	b := w.alertAt("b", 105)
	if err := w.st.Write(w.ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO schedule (project, kind, due_at, created_at, updated_at) VALUES ('', 'inbox_notify', 100, 0, 0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	w.poke()
	w.wantRings(rang{session: "coord-b", dests: []string{"b"}, text: RingText([]Entry{b})},
		rang{session: "coord-a", dests: []string{"a"}, text: RingText([]Entry{a})})
}

// C14: a cursor that a poke moved stays where it is when a second poke,
// which read the old value, tries to move it. So no entry rings again.
func TestC14ACursorAnotherPokeMovedStays(t *testing.T) {
	w := newNotifyWorld(t)
	w.topo = twoHomes
	w.serve(map[string]string{"a": "coord-a"})
	first := w.alertAt("a", 100)
	// While the first poke rings, a new entry arrives, and a second
	// notifier rings both entries and moves the cursor to 200.
	var second Entry
	w.during = func() {
		second = w.alertAt("a", 200)
		if err := w.notifier().Poke(w.ctx); err != nil {
			t.Fatal(err)
		}
	}
	w.poke()
	w.wantRings(rang{session: "coord-a", dests: []string{"a"}, text: RingText([]Entry{first})},
		rang{session: "coord-a", dests: []string{"a"}, text: RingText([]Entry{first, second})})
	if at, ok := w.cursor("a"); !ok || at != 200 {
		t.Fatalf("a's cursor = %d %v, want 200, where the second poke moved it", at, ok)
	}
	w.poke()
	w.wantRings()
}

// C10: for any generated inbox and topology, `inbox list` returns to a
// session the entries that ring it. Before work entry 7 one session
// serves every destination. After it each home serves its project, the
// old session serves its project, and nobody serves "". The generator
// makes topologies where Old equals Loop.
func TestC10TheInboxASessionListsIsTheSetThatRingsIt(t *testing.T) {
	w := newNotifyWorld(t)
	projects := []string{"a", "b", "c", "loop"}
	for _, p := range append([]string{""}, projects...) {
		w.alertAt(p, 100)
	}
	w.alert("b", "coordinator_start_failed", "coordinator_start_failed:b")
	w.question(w.alertAt("b", 120).ID)
	w.question("")
	if _, err := (&fault.Service{Store: w.st}).Report(w.ctx, rpc.Principal{Kind: cli.PrincipalRun, RunID: "r-c1", Project: "c",
		Item: "c-1", Stage: "implementer"}, "the CLI refused a valid call"); err != nil {
		t.Fatal(err)
	}
	es, err := w.in.Entries(w.ctx)
	if err != nil {
		t.Fatal(err)
	}

	r := rand.New(rand.NewPCG(1, 5))
	pick := func(xs ...string) string { return xs[r.IntN(len(xs))] }
	for i := range 300 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			topo := coord.Topology{Loop: pick("", "loop", "a"), Homes: map[string]bool{}}
			for _, p := range projects {
				if r.IntN(2) == 0 {
					topo.Homes[p] = true
				}
			}
			topo.Old = pick("", "a", "b", "c", "loop", topo.Loop, topo.Loop)
			delete(topo.Homes, topo.Old)
			w.topo = topo
			// Each session, and the principal of its coordinator.
			callers := map[string]rpc.Principal{}
			if r.IntN(4) == 0 {
				w.sessions = func(string) (string, bool) { return "one", true }
				callers["one"] = rpc.Principal{Kind: cli.PrincipalCoordinator}
			} else {
				for p := range topo.Homes {
					callers["home-"+p] = rpc.Principal{Kind: cli.PrincipalCoordinator, Project: p}
				}
				if topo.Old != "" {
					callers["old"] = rpc.Principal{Kind: cli.PrincipalCoordinator}
				}
				w.sessions = func(p string) (string, bool) {
					switch {
					case topo.Homes[p]:
						return "home-" + p, true
					case p != "" && p == topo.Old:
						return "old", true
					}
					return "", false
				}
			}
			if err := w.st.Write(w.ctx, func(tx *store.Tx) error {
				_, err := tx.Exec(`DELETE FROM schedule WHERE kind = 'home_notify'`)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			w.poke()
			rung := map[string]string{}
			for _, g := range w.rings {
				if _, twice := rung[g.session]; twice {
					t.Fatalf("topology %+v: %s rang twice in one poke", topo, g.session)
				}
				rung[g.session] = g.text
			}
			w.rings = nil
			for session := range rung {
				if _, ok := callers[session]; !ok {
					t.Fatalf("topology %+v: the poke rang %s, which is no session", topo, session)
				}
			}
			for session, p := range callers {
				listed := w.in.served(p, slices.Clone(es))
				if text, ok := rung[session]; ok != (len(listed) > 0) || ok && text != RingText(listed) {
					t.Fatalf("topology %+v: %s lists %d entries %q, and rang %v %q", topo, session, len(listed), RingText(listed), ok, text)
				}
			}
		})
	}
}
