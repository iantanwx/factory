package sched

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/store"
)

// Profile is the harness and model one run takes.
type Profile struct {
	Harness agenthost.Harness `json:"harness"`
	Model   string            `json:"model"`
	Effort  string            `json:"effort,omitempty"`
}

// DefaultModels is the model a harness runs when project config names
// none for the stage. "opus" is Claude Code's model alias.
var DefaultModels = map[agenthost.Harness]string{
	agenthost.Claude: "opus",
	agenthost.Codex:  "gpt-5-codex",
}

// QuotaLimit is the used percent at which the chooser skips a harness,
// ported from quota.maxUsedPercent in alpha's .slothbot. A run started
// into the last tenth of a window can spend it before it finishes.
const QuotaLimit = 90.0

// QuotaSpent is the used percent at which a window counts as spent, so a
// run that ended with no finish then hit a usage limit.
const QuotaSpent = 100.0

// profiles lists the profiles a stage may take, in order: the project's
// stage config (claude when it names no harness), then each other harness
// that host config lists a login for, at its default model.
func profiles(stage string, pc *config.ProjectConfig, hc *config.HostConfig) []Profile {
	sc := pc.Stages[stage]
	h := agenthost.Harness(sc.Harness)
	if h == "" {
		h = agenthost.Claude
	}
	m := sc.Model
	if m == "" {
		m = DefaultModels[h]
	}
	out := []Profile{{Harness: h, Model: m}}
	for _, l := range hc.Harnesses {
		lh := agenthost.Harness(l.Name)
		if DefaultModels[lh] == "" || slices.ContainsFunc(out, func(p Profile) bool { return p.Harness == lh }) {
			continue
		}
		out = append(out, Profile{Harness: lh, Model: DefaultModels[lh]})
	}
	return out
}

// choose returns the first profile whose harness is neither paused nor out
// of quota, ported from choose in slothbot's wake/choose.ts (alpha). With
// none it returns the reason each was skipped. A harness with no quota
// reading is not skipped: the loop cannot tell, and a hit pauses it.
func (s *Scheduler) choose(ctx context.Context, ps []Profile) (*Profile, map[string]string) {
	paused := s.pauses(ctx)
	why := map[string]string{}
	now := s.now()
	for _, p := range ps {
		if until, ok := paused[p.Harness]; ok && until.After(now) {
			why[string(p.Harness)] = "paused after a usage limit until " + until.UTC().Format(time.RFC3339)
			continue
		}
		if note := s.quotaFull(ctx, p.Harness, QuotaLimit); note != "" {
			why[string(p.Harness)] = note
			continue
		}
		return &p, nil
	}
	return nil, why
}

// quotaFull returns why the harness's reading shows a window at or above
// limit, or "" when it shows none or the host has no reading.
func (s *Scheduler) quotaFull(ctx context.Context, h agenthost.Harness, limit float64) string {
	qr, ok := s.cfg.Agents.(agenthost.QuotaReader)
	if !ok {
		return ""
	}
	q, err := qr.Quota(ctx, h)
	if err != nil || !q.OK {
		return ""
	}
	for _, w := range q.Windows {
		if w.UsedPercent >= limit {
			return fmt.Sprintf("%s window %s is at %.0f%%", h, w.Name, w.UsedPercent)
		}
	}
	return ""
}

// pauseHarness pauses a harness that hit a usage limit until its reset
// (KTD8). It returns the limit's window and the end of the pause. A reset
// ahead names the window. With none, a pause that still holds names it,
// since the hit falls in the same window, and otherwise the window lasts
// QuotaPause. The pause never ends before one that still holds. It lives
// in the schedule table, so a restart keeps it.
func (s *Scheduler) pauseHarness(ctx context.Context, h agenthost.Harness, note string, reset time.Time) (window, until time.Time) {
	now := s.now()
	cur, held := s.pauses(ctx)[h]
	held = held && cur.After(now)
	switch {
	case reset.After(now):
		window = reset
	case held:
		window = cur
	default:
		window = now.Add(s.cfg.QuotaPause)
	}
	until = window
	if held && cur.After(until) {
		until = cur
	}
	err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(`DELETE FROM schedule WHERE kind = 'quota_pause' AND stage = ?`, string(h)); err != nil {
			return err
		}
		ms := tx.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO schedule (project, kind, stage, due_at, payload, created_at, updated_at)
			VALUES ('', 'quota_pause', ?, ?, json_object('note', ?), ?, ?)`, string(h), until.UnixMilli(), note, ms, ms)
		return err
	})
	if err != nil {
		s.log.ErrorContext(ctx, "pause a harness", "harness", h, "err", err)
		return window, until
	}
	s.log.WarnContext(ctx, "harness paused", "harness", h, "until", until, "note", note)
	return window, until
}

// alertLimit raises one inbox line for each usage-limit window of a
// harness, however many runs hit it, ported from limit-summary.ts in
// alpha. lim is the limit the run's agent wrote, or nil when the quota
// reading showed the spent window.
func (s *Scheduler) alertLimit(ctx context.Context, r runRow, note string, lim *agenthost.Limit, window, until time.Time) {
	h := r.Harness
	what := fmt.Sprintf("%s run %s on %s ended while %s", r.Stage, r.ID, r.Item, note)
	reset := "The reading names no reset time"
	if lim != nil {
		what = fmt.Sprintf("%s run %s on %s wrote only an account usage limit and no work: %q", r.Stage, r.ID, r.Item, lim.Message)
		reset = "The message names no reset time"
		if !lim.ResetAt.IsZero() {
			reset = "It resets " + lim.ResetAt.UTC().Format(time.RFC3339)
		}
	}
	s.raiseOnce(ctx, config.Alert{Project: r.Project, Kind: AlertUsageLimit,
		DedupeKey: fmt.Sprintf("%s:%s:%d", AlertUsageLimit, h, window.UnixMilli()),
		Subject:   fmt.Sprintf("%s hit an account usage limit, and runs on it wait until %s", h, until.UTC().Format(time.RFC3339)),
		Body: what + ". " + reset + ". The run does not count against the failure limit. " +
			"A person decides whether to wait for the reset or to change the account."})
}

// pauses reads every harness pause.
func (s *Scheduler) pauses(ctx context.Context) map[agenthost.Harness]time.Time {
	out := map[agenthost.Harness]time.Time{}
	_ = s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT stage, due_at FROM schedule WHERE kind = 'quota_pause'`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var h string
			var due int64
			if err := rs.Scan(&h, &due); err != nil {
				return err
			}
			out[agenthost.Harness(h)] = time.UnixMilli(due)
		}
		return rs.Err()
	})
	return out
}
