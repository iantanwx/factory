package server

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/sched"
)

// countingHost counts each Notify the server sends to the agent host.
type countingHost struct {
	agenthost.Host
	notifies atomic.Int64
}

func (h *countingHost) Notify(ctx context.Context, id agenthost.RunID, text string) error {
	h.notifies.Add(1)
	return h.Host.Notify(ctx, id, text)
}

// C-bug-1: a ring that the host refuses again and again to the same stored
// coordinator session raises one open alert that names the session and the
// refusal, and the server stops ringing that session on every tick.
func TestCBug1ARefusedRingAlertsOnceAndBacksOff(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	counter := &countingHost{Host: w.host}
	w.srv.opts.Agents = counter
	w.srv.setOverwatchParts(workParts())
	j := w.srv.overwatchJob()

	session := w.srv.Sched.Coordinator().Session.ID
	w.host.RefuseNotify(session, true)
	if err := config.StoreAlerter(w.srv.Store).Raise(ctx, config.Alert{Project: "alpha", Kind: sched.AlertRunUnknown, DedupeKey: "u",
		Subject: "a run's liveness is unknown"}); err != nil {
		t.Fatal(err)
	}

	// 20 ticks of 15s, as F-30 saw: each tick the inbox doorbell and the
	// overwatch doorbell ring the same refused session.
	const ticks = 20
	for i := 0; i < ticks; i++ {
		_ = w.srv.Notifier.Poke(ctx)
		_ = j.Tick(ctx)
		w.clock.Advance(15 * time.Second)
	}

	var n int
	var subject, body string
	if err := w.srv.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT subject, body FROM alerts WHERE state = 'open' AND dedupe_key != 'u'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s, b string
			if err := rows.Scan(&s, &b); err != nil {
				return err
			}
			if strings.Contains(s+" "+b, session.Terminal) {
				n, subject, body = n+1, s, b
			}
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 || !strings.Contains(subject+" "+body, "refused") {
		t.Errorf("open alerts that name the refused session %s = %d (subject %q, body %q), want 1 that quotes the refusal", session.Terminal, n, subject, body)
	}
	if got := counter.notifies.Load(); got >= 2*ticks {
		t.Errorf("the server rang the refused session %s %d times in %d ticks, want a backoff", session.Terminal, got, ticks)
	}
}
