package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// EffectStatus is the outbox effect kind of a commit status. Its natural
// key is "<project>:<sha>:<context>" (KTD6).
const EffectStatus = "status"

// StatusPayload is the payload of one status effect.
type StatusPayload struct {
	Project     string `json:"project"`
	Item        string `json:"item"`
	SHA         string `json:"sha"`
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
}

// statusKey is the natural key of one status effect.
func statusKey(project, sha, context string) string { return project + ":" + sha + ":" + context }

// PostStatus posts one commit status as the App.
func (s *Service) PostStatus(ctx context.Context, p config.Project, sha, context, state, description string) error {
	if !shaRe.MatchString(sha) {
		return fmt.Errorf("github: %q is not a full sha", sha)
	}
	_, err := s.repoDo(ctx, p, ScopeStatuses, Call{Method: http.MethodPost, Path: repoPath(p, "/statuses/"+sha),
		Body: map[string]string{"state": state, "context": context, "description": description}}, nil)
	return err
}

// EnqueueEligibility makes sure every ticket that passed review holds an
// outbox effect that posts loop/eligible on its reviewed head (KTD15). A
// review pass writes reviewed_head with the state change. The effect
// joins the intent of that pass, and it is idempotent by its natural key,
// so a crash between the pass and the post leaves nothing behind: the
// next tick or start-up finds the pass and enqueues the post again. It
// posts on the reviewed sha only, never on a later head.
func (s *Service) EnqueueEligibility(ctx context.Context, p config.Project) (int, error) {
	m := s.cfg.Machine
	if m == nil {
		return 0, nil
	}
	var want []machine.Item
	for _, status := range []string{machine.ReadyToMerge, machine.Queued} {
		items, ref := m.List(ctx, p.ID, machine.ListFilter{Kind: machine.KindTicket, Status: status})
		if ref != nil {
			return 0, fmt.Errorf("github: list %s tickets: %s", status, ref.Hint)
		}
		want = append(want, items...)
	}
	added := 0
	for _, it := range want {
		sha := it.Fact(machine.FactReviewedHead)
		if !shaRe.MatchString(sha) {
			continue
		}
		payload, err := json.Marshal(StatusPayload{Project: p.ID, Item: it.ID, SHA: sha, Context: EligibleContext,
			State: "success", Description: "the loop's review passed at this head"})
		if err != nil {
			return added, err
		}
		err = s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			var intent int64
			err := tx.QueryRow(`SELECT intent_id FROM transitions WHERE project = ? AND item = ? AND verb = 'review_pass'
				ORDER BY seq DESC LIMIT 1`, p.ID, it.ID).Scan(&intent)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			now := tx.Now().UnixMilli()
			res, err := tx.Exec(`INSERT OR IGNORE INTO outbox (intent_id, effect_kind, natural_key, payload, state, created_at, updated_at)
				VALUES (?, ?, ?, ?, 'runnable', ?, ?)`, intent, EffectStatus, statusKey(p.ID, sha, EligibleContext), string(payload), now, now)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			added += int(n)
			return nil
		})
		if err != nil {
			return added, err
		}
	}
	return added, nil
}

// maxEffectAttempts bounds the retries of one effect GitHub keeps
// refusing with a 4xx. A rate limit or an unavailable GitHub retries
// without end, and never fails the effect.
const maxEffectAttempts = 8

type effect struct {
	id       int64
	attempts int
	payload  StatusPayload
}

// RunStatusEffects posts every due status effect. An effect is done only
// when GitHub answered success. An unknown answer (a timeout, a 5xx, a
// rate limit) leaves it runnable for a later attempt, because a status
// post is idempotent by sha and context. A 4xx after maxEffectAttempts
// fails the effect and raises an alert.
func (s *Service) RunStatusEffects(ctx context.Context) error {
	var due []effect
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, attempts, payload FROM outbox
			WHERE state = 'runnable' AND effect_kind = ? AND next_attempt_at <= ? ORDER BY id LIMIT 100`,
			EffectStatus, s.cfg.Now().UnixMilli())
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var e effect
			var raw string
			if err := rows.Scan(&e.id, &e.attempts, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(raw), &e.payload); err != nil {
				return fmt.Errorf("github: outbox %d payload: %w", e.id, err)
			}
			due = append(due, e)
		}
		return rows.Err()
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range due {
		if err := s.runStatus(ctx, e); err != nil {
			var rl *RateLimitError
			if errors.As(err, &rl) {
				return nil // every later call waits for the same cooldown
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) runStatus(ctx context.Context, e effect) error {
	pl := e.payload
	p, err := s.project(pl.Project)
	if err != nil {
		return s.cfg.Store.Write(ctx, func(tx *store.Tx) error { return tx.MarkEffectFailed(e.id, err.Error()) })
	}
	perr := s.PostStatus(ctx, p, pl.SHA, pl.Context, pl.State, pl.Description)
	var (
		rl  *RateLimitError
		api *APIError
	)
	switch {
	case perr == nil:
		return s.cfg.Store.Write(ctx, func(tx *store.Tx) error { return tx.MarkEffectDone(e.id) })
	case errors.As(perr, &rl):
		_ = s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			return tx.MarkEffectRetry(e.id, rl.Until, "rate limited")
		})
		return perr
	case errors.As(perr, &api) && !api.Transient() && e.attempts+1 >= maxEffectAttempts:
		err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error { return tx.MarkEffectFailed(e.id, s.cfg.Redactor.Redact(perr.Error())) })
		if err != nil {
			return err
		}
		return rpc.WriteAuditAlert(ctx, s.cfg.Store, s.cfg.Redactor, rpc.AuditAlert{Actor: "server:github", Project: pl.Project,
			Item: pl.Item, Kind: AlertEffectFailed, Subject: pl.Project + "/" + pl.Item + "@" + pl.SHA,
			Body: fmt.Sprintf("the server could not post %s on %s: %v", pl.Context, short(pl.SHA), perr)})
	default:
		next := s.cfg.Now().Add(backoff(e.attempts))
		if err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
			return tx.MarkEffectRetry(e.id, next, s.cfg.Redactor.Redact(perr.Error()))
		}); err != nil {
			return err
		}
		return perr
	}
}

// backoff is the wait before attempt n+1 of an effect.
func backoff(attempts int) time.Duration {
	d := 30 * time.Second << min(attempts, 6)
	return min(d, 30*time.Minute)
}
