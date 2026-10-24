package sched

import (
	"context"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
)

// ResumeNudge is the text the server types into a session whose last turn
// ended on a transient model API error (F-29). It is the nudge that
// resumed every run of that outage.
const ResumeNudge = "Your last turn ended on a transient model API error. Continue the run from where you stopped."

// ResumeBackoff is the wait before each resume nudge, from the newer of
// the newest error and the last nudge. The first nudge goes out a minute
// after the error, far inside the run budget. After the last one, a
// session still on the error settles when its quiet period passes.
var ResumeBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// resume handles a live run whose last turn ended on a transient model API
// error. Claude Code does not retry such a turn once it ended, so the
// session idles at its prompt until something types into it. resume sends
// ResumeNudge at each backoff step and records each nudge in the run
// record. It returns true when it decided the tick for the run: a nudge
// went out, or one is still due. With every step spent it returns false,
// and the run settles once the host reads it ended.
func (s *Scheduler) resume(ctx context.Context, hc *config.HostConfig, r runRow, rep agenthost.EndReport, inv agenthost.Inventory) bool {
	e := rep.APIError
	if e == nil || rep.Settlement == nil || rep.Settlement.Liveness != agenthost.Live {
		return false
	}
	rec := r.Rec
	first := e.First.UnixMilli()
	// A copy, so keepResume can tell the record changed.
	rr := resumeRecord{First: first}
	if old := r.Rec.Resume; old != nil && old.First == first {
		rr.Nudges = slices.Clone(old.Nudges)
	}
	rec.Resume = &rr
	n := len(rec.Resume.Nudges)
	if n >= len(ResumeBackoff) {
		return false
	}
	from := e.At
	if n > 0 {
		if last := time.UnixMilli(rec.Resume.Nudges[n-1]); last.After(from) {
			from = last
		}
	}
	if s.now().Before(from.Add(ResumeBackoff[n])) {
		s.keepResume(ctx, hc, r, rec, inv)
		return true
	}
	// A nudge the host refused still counts, so a session that takes no
	// input cannot hold its slot past the last step.
	err := s.cfg.Agents.Notify(ctx, r.Rec.Session.ID, ResumeNudge)
	rec.Resume.Nudges = append(rec.Resume.Nudges, s.now64())
	if err != nil {
		s.log.WarnContext(ctx, "resume a run on a transient API error", "run", r.ID, "nudge", n+1, "of", len(ResumeBackoff), "err", err)
	} else {
		s.log.InfoContext(ctx, "resume a run on a transient API error", "run", r.ID, "nudge", n+1, "of", len(ResumeBackoff), "error", e.Message)
	}
	s.keepResume(ctx, hc, r, rec, inv)
	return true
}

// keepResume writes a changed resume record, then keeps the run live.
func (s *Scheduler) keepResume(ctx context.Context, hc *config.HostConfig, r runRow, rec hostRecord, inv agenthost.Inventory) {
	if rec.encode() != r.Rec.encode() {
		if err := s.setRun(ctx, r.ID, RunRunning, rec); err != nil {
			s.log.ErrorContext(ctx, "record a resume", "run", r.ID, "err", err)
		}
		r.Rec, r.State = rec, RunRunning
	}
	s.markLive(ctx, hc, r, inv)
}
