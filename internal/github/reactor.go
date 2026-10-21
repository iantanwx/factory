package github

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// Alert kinds the GitHub actor and reactor raise. U12's inbox reads the
// alerts table.
const (
	// AlertForeignHead: a PR head that the server did not push (KTD14).
	AlertForeignHead = "foreign_head"
	// AlertPRClosed: a PR closed without merge escalated its item (R8).
	AlertPRClosed = "pr_closed"
	// AlertEffectFailed: GitHub kept refusing an outbox effect.
	AlertEffectFailed = "github_effect_failed"
)

// StreamPulls is the reactor's cursor stream of updated PRs.
const StreamPulls = "pulls"

// maxPullPages bounds one tick's walk back through updated PRs.
const maxPullPages = 10

// Reactor reacts to PR and CI changes (KTD16). The devbox has no public
// webhook, so it polls with a cursor in SQLite. A merge into the base
// moves any non-terminal item to done, a closed PR escalates its item, a
// new head after a review pass returns the ticket to review, and red CI
// on a ticket in review sends it to repair. CI cancelled by a newer push
// is superseded, not red.
type Reactor struct {
	s   *Service
	m   *machine.Machine
	log *slog.Logger

	mu sync.Mutex
	// full is true until one full reconcile of every project succeeds. A
	// tick runs the full reconcile while it holds, so a start-up that met
	// a GitHub outage never drops a merge.
	full bool
}

// NewReactor returns the reactor over the service's machine.
func NewReactor(s *Service) *Reactor {
	return &Reactor{s: s, m: s.cfg.Machine, log: s.log, full: true}
}

// Reconcile is the start-up step (a server.Step): it settles every
// pending push, then reads the PR of every non-terminal ticket and epic,
// open or closed, and posts every eligibility a crash left unposted. The
// server runs it after the machine's reconcile and before the socket
// binds. A GitHub that cannot answer does not stop the start: the step
// logs the fault, and the next Tick runs the full reconcile again.
func (r *Reactor) Reconcile(ctx context.Context) error {
	if err := r.reconcileAll(ctx); err != nil {
		r.log.WarnContext(ctx, "github reconcile did not finish; the next tick runs it again", "err", err)
	}
	return nil
}

func (r *Reactor) reconcileAll(ctx context.Context) error {
	if r.m == nil {
		return nil
	}
	ps, err := r.s.cfg.Projects.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range ps {
		if err := r.reconcileProject(ctx, p); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", p.ID, err))
		}
	}
	err = errors.Join(errs...)
	r.mu.Lock()
	r.full = err != nil
	r.mu.Unlock()
	return err
}

func (r *Reactor) reconcileProject(ctx context.Context, p config.Project) error {
	if err := r.s.ReconcilePushes(ctx, p); err != nil {
		return err
	}
	base, err := r.s.DefaultBranch(ctx, p)
	if err != nil {
		return err
	}
	var errs []error
	for _, kind := range []machine.Kind{machine.KindTicket, machine.KindEpic} {
		items, ref := r.m.List(ctx, p.ID, machine.ListFilter{Kind: kind})
		if ref != nil {
			return fmt.Errorf("list %s items: %s", kind, ref.Hint)
		}
		t, _ := machine.TableFor(kind)
		for _, it := range items {
			if t.Terminal(it.Status) {
				continue
			}
			pr, err := r.s.FindPR(ctx, p, BranchFor(it.ID))
			if err != nil {
				return err // a rate limit stops every later read too
			}
			if pr == nil {
				continue
			}
			if err := r.handle(ctx, p, base, *pr); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if err := r.eligibility(ctx, p); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Tick runs one poll: the full reconcile while one is owed, then for each
// project the PRs updated since the cursor, the CI of each ticket in
// review, the base branch into the mirror, and the status effects. While
// the shared cooldown holds, a tick reads nothing. U8's scheduler calls
// it.
func (r *Reactor) Tick(ctx context.Context) error {
	if r.m == nil {
		return nil
	}
	if r.s.cool.Until().After(time.Time{}) {
		return nil
	}
	r.mu.Lock()
	full := r.full
	r.mu.Unlock()
	if full {
		if err := r.reconcileAll(ctx); err != nil {
			return r.quiet(err)
		}
	}
	ps, err := r.s.cfg.Projects.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range ps {
		if err := r.tickProject(ctx, p); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", p.ID, err))
		}
	}
	return r.quiet(errors.Join(errs...))
}

// quiet drops a rate limit: the cooldown already holds every caller.
func (r *Reactor) quiet(err error) error {
	var rl *RateLimitError
	if errors.As(err, &rl) {
		r.log.Info("github cooldown", "until", rl.Until)
		return nil
	}
	return err
}

// Run ticks every interval until ctx ends. U8's scheduler may call Tick
// instead.
func (r *Reactor) Run(ctx context.Context, every time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := r.Tick(ctx); err != nil && ctx.Err() == nil {
			r.log.WarnContext(ctx, "github tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (r *Reactor) tickProject(ctx context.Context, p config.Project) error {
	base, _, err := r.s.SyncBase(ctx, p)
	if err != nil {
		return err
	}
	var errs []error
	if err := r.pollPulls(ctx, p, base); err != nil {
		errs = append(errs, err)
	}
	if err := r.pollCI(ctx, p); err != nil {
		errs = append(errs, err)
	}
	if err := r.eligibility(ctx, p); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// eligibility enqueues and posts loop/eligible for every passed review.
func (r *Reactor) eligibility(ctx context.Context, p config.Project) error {
	if _, err := r.s.EnqueueEligibility(ctx, p); err != nil {
		return err
	}
	return r.s.RunStatusEffects(ctx)
}

// cursor reads one stream's cursor, or the zero time.
func (r *Reactor) cursor(ctx context.Context, project string) (time.Time, error) {
	var raw string
	err := r.s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT cursor FROM github_cursors WHERE project = ? AND stream = ?`, project, StreamPulls).Scan(&raw)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339Nano, raw)
}

func (r *Reactor) advance(ctx context.Context, project string, t time.Time) error {
	return r.s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO github_cursors (project, stream, cursor, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (project, stream) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at`,
			project, StreamPulls, t.UTC().Format(time.RFC3339Nano), tx.Now().UnixMilli())
		return err
	})
}

// pollPulls reads the PRs updated at or after the cursor, newest first,
// and handles each one. The cursor advances only when every PR read was
// handled, so a failed read or move is read again next tick and an
// unknown result is never taken as done. With no cursor yet, one page
// sets it: the start-up reconcile already read every item's PR.
func (r *Reactor) pollPulls(ctx context.Context, p config.Project, base string) error {
	since, err := r.cursor(ctx, p.ID)
	if err != nil {
		return err
	}
	newest := since
	var errs []error
	for page := 1; page <= maxPullPages; page++ {
		q := url.Values{"state": {"all"}, "sort": {"updated"}, "direction": {"desc"}, "per_page": {"50"},
			"page": {strconv.Itoa(page)}}
		var prs []PR
		if err := r.s.repoGet(ctx, p, ScopeRead, repoPath(p, "/pulls?"+q.Encode()), &prs); err != nil {
			return err
		}
		older := false
		for _, pr := range prs {
			if pr.UpdatedAt.Before(since) {
				older = true
				break
			}
			if pr.UpdatedAt.After(newest) {
				newest = pr.UpdatedAt
			}
			if err := r.handle(ctx, p, base, pr); err != nil {
				errs = append(errs, err)
			}
		}
		if older || len(prs) < 50 || since.IsZero() {
			break
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if newest.After(since) {
		return r.advance(ctx, p.ID, newest)
	}
	return nil
}

// pollCI reads the CI of each ticket in review. A check run's end does not
// change the PR's update time, so the PR cursor cannot see it.
func (r *Reactor) pollCI(ctx context.Context, p config.Project) error {
	items, ref := r.m.List(ctx, p.ID, machine.ListFilter{Kind: machine.KindTicket, Status: machine.InReview})
	if ref != nil {
		return fmt.Errorf("list tickets in review: %s", ref.Hint)
	}
	var errs []error
	for _, it := range items {
		pr, err := r.s.FindPR(ctx, p, BranchFor(it.ID))
		if err != nil {
			return err
		}
		if pr == nil || pr.State != "open" {
			continue
		}
		rep, err := r.s.Checks(ctx, p, pr.Head.SHA)
		if err != nil {
			return err
		}
		if rep.State != CIRed {
			continue
		}
		res := r.m.Apply(ctx, machine.Request{Project: p.ID, Item: it.ID, Verb: "ci_red", ExpectedStatus: machine.InReview,
			Params: map[string]string{"head": pr.Head.SHA}, Actor: machine.Server("reactor")})
		if err := settled(res); err != nil {
			errs = append(errs, fmt.Errorf("%s ci_red: %w", it.ID, err))
		}
	}
	return errors.Join(errs...)
}

// settled maps a machine answer to an error. A status the item already
// left, or a move the table does not hold, is no fault: the reactor read
// an old view, and the next read decides again.
func settled(res cli.Result) error {
	if res.Refusal == nil {
		return nil
	}
	switch res.Refusal.Code {
	case cli.CodeStatusChanged, cli.CodeTransitionNotAllowed, cli.CodeNotFound:
		return nil
	}
	return fmt.Errorf("%s: %s", res.Refusal.Code, res.Refusal.Hint)
}

// handle reacts to one PR of the project.
func (r *Reactor) handle(ctx context.Context, p config.Project, base string, pr PR) error {
	item := ItemOf(pr.Head.Ref)
	if item == "" || !pr.fromRepo(p) {
		return nil
	}
	it, ref := r.m.Show(ctx, p.ID, item)
	if ref != nil {
		if ref.Code == cli.CodeNotFound {
			return nil
		}
		return fmt.Errorf("%s: %s", item, ref.Hint)
	}
	t, ok := machine.TableFor(it.Kind)
	if !ok || (it.Kind != machine.KindTicket && it.Kind != machine.KindEpic) || t.Terminal(it.Status) {
		return nil
	}
	// An epic's branch carries its PRD, then its spec, each in its own PR.
	// Only the current document's PR moves the epic (U13).
	if it.Kind == machine.KindEpic && it.Fact(machine.FactDocPR) != strconv.Itoa(pr.Number) {
		return nil
	}
	switch {
	case pr.Merged():
		// A merge wins from any state (KTD16). MergeObserved retries a
		// status miss and answers Nothing for a terminal item. Any other
		// refusal keeps the cursor, so the merge is read again.
		ev := machine.MergeEvent{Project: p.ID, Item: item, Base: base, Into: pr.Base.Ref, SHA: pr.MergeCommitSHA}
		res := r.m.MergeObserved(ctx, ev)
		if it.Kind == machine.KindEpic && res.Refusal == nil && res.Exit == cli.ExitDone {
			// A document that outran its review landed on sign-off. The
			// merge is the operator's sign-off too, so the sign-off row
			// carries the epic on (document-states.ts:141-145, alpha).
			res = r.m.MergeObserved(ctx, ev)
		}
		if res.Refusal != nil && res.Refusal.Code != cli.CodeNotFound {
			return fmt.Errorf("%s merged as #%d: %s: %s", item, pr.Number, res.Refusal.Code, res.Refusal.Hint)
		}
		return nil
	case pr.State == "closed":
		return r.closed(ctx, p, it, pr)
	}
	return r.head(ctx, p, it, pr)
}

// closed escalates an item whose PR closed without merge (R8). The loop
// never reopens a PR.
func (r *Reactor) closed(ctx context.Context, p config.Project, it machine.Item, pr PR) error {
	t, _ := machine.TableFor(it.Kind)
	if !t.Allows("pr_closed", it.Status) {
		return nil
	}
	res := r.m.Apply(ctx, machine.Request{Project: p.ID, Item: it.ID, Verb: "pr_closed", ExpectedStatus: it.Status,
		Params: map[string]string{"pr": strconv.Itoa(pr.Number)}, Actor: machine.Server("reactor")})
	if res.Refusal != nil {
		return settled(res)
	}
	return r.alert(ctx, rpc.AuditAlert{Actor: "server:reactor", Project: p.ID, Item: it.ID, Kind: AlertPRClosed,
		Subject: fmt.Sprintf("%s/%s#%d", p.ID, it.ID, pr.Number),
		Body: fmt.Sprintf("PR #%d of %s closed without a merge, so %s moved from %s to escalated. The loop never reopens a PR. %s",
			pr.Number, it.ID, it.ID, it.Status, pr.URL),
		Detail: map[string]any{"pr": pr.Number, "from": it.Status, "head": pr.Head.SHA}})
}

// head reacts to an open PR's head. A head the server did not push raises
// an audit alert (KTD14). A head that differs from the reviewed head
// returns a ticket that passed review to in_review (AE1), whoever pushed
// it: a review room's push is a head change too (KTD23).
func (r *Reactor) head(ctx context.Context, p config.Project, it machine.Item, pr PR) error {
	sha := pr.Head.SHA
	if sha != it.Fact(machine.FactHead) && sha != it.Fact(machine.FactReviewedHead) {
		own, err := r.s.ownPush(ctx, p.ID, pr.Head.Ref, sha)
		if err != nil {
			return err
		}
		if !own {
			if err := r.alert(ctx, rpc.AuditAlert{Actor: "server:reactor", Project: p.ID, Item: it.ID, Kind: AlertForeignHead,
				Subject: fmt.Sprintf("%s/%s@%s", p.ID, it.ID, sha),
				Body: fmt.Sprintf("PR #%d of %s moved to %s, a head the server did not push. The ticket returns to review if a review had passed.",
					pr.Number, it.ID, short(sha)),
				Detail: map[string]any{"pr": pr.Number, "head": sha, "status": it.Status}}); err != nil {
				return err
			}
		}
	}
	if it.Kind != machine.KindTicket || !slices.Contains([]string{machine.ReadyToMerge, machine.Queued}, it.Status) ||
		sha == it.Fact(machine.FactReviewedHead) {
		return nil
	}
	return settled(r.m.HeadChanged(ctx, p.ID, it.ID, sha))
}

// alert raises one alert unless the same alert is already open, so a
// repeated read of one fact adds no audit row.
func (r *Reactor) alert(ctx context.Context, a rpc.AuditAlert) error {
	var n int
	err := r.s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM alerts WHERE dedupe_key = ? AND state = 'open'`,
			a.Kind+":"+a.Subject).Scan(&n)
	})
	if err != nil || n > 0 {
		return err
	}
	return rpc.WriteAuditAlert(ctx, r.s.cfg.Store, r.s.cfg.Redactor, a)
}
