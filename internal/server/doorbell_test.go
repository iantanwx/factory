package server

import (
	"context"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/overwatch"
	"github.com/iantanwx/loop/internal/sched"
)

// inboxRings lists the lines the inbox doorbell typed into the
// coordinator's session.
func (w *humanWorld) inboxRings() []string {
	w.t.Helper()
	var out []string
	for _, n := range w.host.Notifications(w.srv.Sched.Coordinators()["alpha"].Session.ID) {
		if strings.HasPrefix(n, "loop inbox:") {
			out = append(out, n)
		}
	}
	return out
}

// The done-means of spec factorio-1edo work entry 5: with one coordinator
// for every project, a poke over entries of two projects rings that
// session once. alpha is the loop project, and beta has no coordinator,
// so its entries route to alpha too. The ring record of alpha holds the
// ring, and the inbox part reads it.
func TestC13APokeOverTwoProjectsRingsTheOneCoordinatorOnce(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	before := len(w.inboxRings())
	for _, p := range []string{"alpha", "beta"} {
		if err := config.StoreAlerter(w.srv.Store).Raise(ctx, config.Alert{Project: p, Kind: sched.AlertRunUnknown, DedupeKey: "u-" + p,
			Subject: "a run's liveness is unknown"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.srv.Notifier.Poke(ctx); err != nil {
		t.Fatal(err)
	}
	rings := w.inboxRings()[before:]
	if len(rings) != 1 || !strings.Contains(rings[0], "2 new") {
		t.Fatalf("inbox rings = %q, want one ring that names both entries", rings)
	}
	for _, dest := range []string{"alpha"} {
		if o, ok := w.lastRing(dest); !ok || o.Source != overwatch.SourceInbox || o.Result != overwatch.RingAccepted ||
			o.Handle != w.srv.Sched.Coordinators()["alpha"].Session.ID.String() {
			t.Fatalf("the ring record of %q = %+v %v", dest, o, ok)
		}
	}
	got, err := w.srv.overwatch.world.LastRings(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("the world's LastRings = %+v %v, want the ring of alpha", got, err)
	}
}
