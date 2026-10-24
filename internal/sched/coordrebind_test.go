package sched

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/config"
)

// C-bug-1: when orca moves a live coordinator session to a new handle with
// the same incarnation and the same checkout, the next tick rebinds the
// stored handle, and a ring through the stored session reaches the live
// session. The server starts no second coordinator.
func TestCoordinatorRebindsAStaleHandleByIncarnation(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Projects = func() ([]config.Project, error) {
		return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}}, nil
	}
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	w.tick()
	first := w.s.Coordinator()
	if first.Session == nil {
		t.Fatalf("first coordinator = %+v", first)
	}

	w.host.Restart(false)
	live, ok := w.host.CurrentID(first.Session.ID)
	if !ok || live.Terminal == first.Session.ID.Terminal || live.Incarnation != first.Session.ID.Incarnation {
		t.Fatalf("fixture: the handle did not change under the same incarnation: %+v -> %+v", first.Session.ID, live)
	}
	if err := w.host.Notify(ctx, first.Session.ID, "ring"); err == nil {
		t.Fatal("fixture: the old handle still takes a ring")
	}

	w.tick()
	got := w.s.Coordinator()
	if got.Session == nil || got.RunID != first.RunID {
		t.Fatalf("the coordinator after the handle change = %+v; first %+v", got, first)
	}
	if got.Session.ID != live {
		t.Fatalf("stored handle = %+v, want the live handle %+v", got.Session.ID, live)
	}
	if err := w.host.Notify(ctx, got.Session.ID, "loop inbox: 1 new"); err != nil {
		t.Fatalf("a ring through the stored session: %v", err)
	}
	if notes := w.host.Notifications(first.Session.ID); len(notes) != 1 {
		t.Fatalf("notifications of the live session = %q, want one ring", notes)
	}
	if n := len(w.host.Launches()); n != 1 {
		t.Fatalf("launches = %d, want 1: the server started a second coordinator", n)
	}
}

// editedInventory is the fake host with each inventory edited, so a test
// can list rows the fake never makes on its own.
type editedInventory struct {
	*fake.Host
	edit func(agenthost.Inventory) agenthost.Inventory
}

func (h editedInventory) Inventory(ctx context.Context) (agenthost.Inventory, error) {
	inv, err := h.Host.Inventory(ctx)
	if err != nil {
		return inv, err
	}
	return h.edit(inv), nil
}

// C-bug-1: a doubt rebinds nothing. An incomplete inventory, no row of the
// stored incarnation, two such rows, or the one such row in another
// checkout keeps the stale handle as stored, and the server starts no
// second coordinator.
func TestCoordinatorRebindKeepsTheStoredHandleOnADoubt(t *testing.T) {
	cases := []struct {
		name string
		// doubt sets the host up after the handle moved.
		doubt func(w *world, live agenthost.RunID)
	}{
		{"incomplete inventory", func(w *world, _ agenthost.RunID) {
			w.host.SetInventoryIncomplete(true)
		}},
		{"no row of the incarnation", func(w *world, live agenthost.RunID) {
			w.cfg.Agents = editedInventory{w.host, func(inv agenthost.Inventory) agenthost.Inventory {
				var rows []agenthost.Row
				for _, r := range inv.Rows {
					if r.ID != live {
						rows = append(rows, r)
					}
				}
				inv.Rows = rows
				return inv
			}}
		}},
		{"two rows of the incarnation", func(w *world, live agenthost.RunID) {
			w.cfg.Agents = editedInventory{w.host, func(inv agenthost.Inventory) agenthost.Inventory {
				for _, r := range inv.Rows {
					if r.ID == live {
						twin := r
						twin.ID.Terminal, twin.Pty = "term_fake_twin", "pty_fake_twin"
						inv.Rows = append(inv.Rows, twin)
						break
					}
				}
				return inv
			}}
		}},
		{"the incarnation in another checkout", func(w *world, live agenthost.RunID) {
			w.cfg.Agents = editedInventory{w.host, func(inv agenthost.Inventory) agenthost.Inventory {
				for i, r := range inv.Rows {
					if r.ID == live {
						inv.Rows[i].Checkout = r.Checkout + "-other"
					}
				}
				return inv
			}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			w := newWorld(t)
			w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
			w.rebuild()
			if err := w.s.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			w.tick()
			first := w.s.Coordinator()
			if first.Session == nil {
				t.Fatalf("first coordinator = %+v", first)
			}

			w.host.Restart(false)
			live, ok := w.host.CurrentID(first.Session.ID)
			if !ok || live == first.Session.ID {
				t.Fatalf("fixture: the handle did not move: %+v", live)
			}
			c.doubt(w, live)
			w.rebuild()
			w.tick()
			w.tick()

			got := w.s.Coordinator()
			if got.Session == nil || got.RunID != first.RunID || got.Session.ID != first.Session.ID {
				t.Fatalf("the coordinator after a doubt = %+v; want the stored %+v", got.Session, first.Session)
			}
			if got.Session.Epoch != first.Session.Epoch {
				t.Fatalf("stored epoch = %q, want %q", got.Session.Epoch, first.Session.Epoch)
			}
			if n := len(w.host.Launches()); n != 1 {
				t.Fatalf("launches = %d, want 1: the server started a second coordinator", n)
			}
		})
	}
}

// C-bug-1: a row with the same handle and another incarnation is a fresh
// shell, never the coordinator, so nothing rebinds to it.
func TestCoordinatorNeverRebindsToAnotherIncarnation(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	w.rebuild()
	if err := w.s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	w.tick()
	first := w.s.Coordinator()
	if first.Session == nil {
		t.Fatalf("first coordinator = %+v", first)
	}

	w.host.DaemonDeath()
	cur, ok := w.host.CurrentID(first.Session.ID)
	if !ok || cur.Terminal != first.Session.ID.Terminal || cur.Incarnation == first.Session.ID.Incarnation {
		t.Fatalf("fixture: want the same handle under another incarnation: %+v -> %+v", first.Session.ID, cur)
	}
	w.tick()
	w.tick()

	got := w.s.Coordinator()
	if got.Session == nil || got.RunID != first.RunID || got.Session.ID != first.Session.ID {
		t.Fatalf("the coordinator after a daemon death = %+v; want the stored %+v", got.Session, first.Session)
	}
	if n := len(w.host.Launches()); n != 1 {
		t.Fatalf("launches = %d, want 1", n)
	}
}
