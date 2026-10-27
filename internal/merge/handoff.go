package merge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// JobName is the native job of the hand-off in host config's throughput.
const JobName = "merge-handoff"

// Hand-off states in the merge_tracking table, one row for each PR.
const (
	stateHanded    = "handed"    // the mechanism holds the PR at head_sha
	stateRequeued  = "requeued"  // handed off again after a dequeue at head_sha
	stateWithdrawn = "withdrawn" // taken back after the ticket left queued
	stateMerged    = "merged"
	stateDequeued  = "dequeued" // a conflict or red CI moved the ticket
	stateClosed    = "closed"   // the reactor escalates the ticket (R8)
	stateEscalated = "escalated"
)

// Config configures the hand-off job.
type Config struct {
	GitHub   *github.Service
	Machine  *machine.Machine
	Store    *store.Store
	Projects github.Projects
	// Source returns a project's config from its default branch, or the
	// last known good config when the branch holds an invalid one (KTD20).
	// It takes the registration, because `project add` asks before the
	// project is registered.
	Source func(ctx context.Context, p config.Project) (*config.ProjectSnapshot, error)
	// Mechanisms holds one mechanism for each name a config may hold.
	Mechanisms map[config.MergeMechanism]Mechanism
	// Settings lands the project's settings changes on each tick, before
	// its tickets. Nil lands no settings change.
	Settings Lander
	Logger   *slog.Logger
}

// Lander moves a project's settings change through the mechanism:
// settings.Service.
type Lander interface {
	// Land moves the project's live change one step.
	Land(ctx context.Context, p config.Project, m Mechanism) error
	// Eligible reports whether PR n is a handed settings change at its
	// recorded head.
	Eligible(ctx context.Context, p config.Project, n int) (bool, error)
}

// HandOff is the merge-handoff native job. Each tick of a project moves
// its settings change one step, withdraws every PR whose ticket left queued, reads the outcome of every
// queued ticket's PR, hands off every ready_to_merge ticket whose
// loop/eligible is posted at its PR's current head, and runs the
// mechanism's sweep.
type HandOff struct {
	cfg Config
	log *slog.Logger
}

// New returns the job.
func New(cfg Config) (*HandOff, error) {
	if cfg.GitHub == nil || cfg.Machine == nil || cfg.Store == nil || cfg.Projects == nil || cfg.Source == nil {
		return nil, errors.New("merge: the hand-off needs GitHub, a machine, a store, the projects and a config source")
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &HandOff{cfg: cfg, log: log}, nil
}

// actor is the server actor of every move the job makes.
var actor = machine.Server("merge")

// Tick runs one tick of every project.
func (h *HandOff) Tick(ctx context.Context) error {
	ps, err := h.cfg.Projects.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, p := range ps {
		if err := h.TickProject(ctx, p.ID); err != nil {
			errs = append(errs, fmt.Errorf("project %s: %w", p.ID, err))
		}
	}
	return errors.Join(errs...)
}

// mechanism resolves a project's mechanism from its config.
func (h *HandOff) mechanism(ctx context.Context, p config.Project) (Mechanism, error) {
	snap, err := h.cfg.Source(ctx, p)
	if err != nil {
		return nil, err
	}
	name := snap.Config.Merge.Mechanism
	m, ok := h.cfg.Mechanisms[name]
	if !ok {
		return nil, fmt.Errorf("merge: project %s names merge mechanism %q, which this server does not hold", p.ID, name)
	}
	return m, nil
}

// AddHook is the config.AddHook of `project add`: it loads the project's
// config from the default branch and runs its mechanism's check. Add it
// after the GitHub hook, which fetches the default branch. A project with
// no config yet passes: loop-init adds it, and each tick checks again.
func (h *HandOff) AddHook(ctx context.Context, p config.Project) error {
	m, err := h.mechanism(ctx, p)
	if errors.Is(err, config.ErrConfigMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	return m.Check(ctx, p)
}

// TickProject runs one tick of one project. It is the scheduler's JobFunc.
// A rate limit ends the tick quietly: the shared cooldown holds every
// later call too.
func (h *HandOff) TickProject(ctx context.Context, project string) error {
	p, err := h.cfg.Projects.Show(project)
	if err != nil {
		return err
	}
	mech, err := h.mechanism(ctx, p)
	if errors.Is(err, config.ErrConfigMissing) {
		return nil // nothing to merge before the project has config
	}
	if err != nil {
		return err
	}
	var errs []error
	if h.cfg.Settings != nil {
		if err := h.cfg.Settings.Land(ctx, p, mech); err != nil {
			if rateLimited(err) {
				return nil
			}
			errs = append(errs, fmt.Errorf("settings: %w", err))
		}
	}
	if err := h.withdrawStale(ctx, p, mech); err != nil {
		errs = append(errs, err)
	}
	for _, step := range []struct {
		status string
		fn     func(context.Context, config.Project, Mechanism, machine.Item) error
	}{{machine.Queued, h.advance}, {machine.ReadyToMerge, h.handOff}} {
		items, ref := h.cfg.Machine.List(ctx, p.ID, machine.ListFilter{Kind: machine.KindTicket, Status: step.status})
		if ref != nil {
			return fmt.Errorf("merge: list %s tickets: %s", step.status, ref.Hint)
		}
		for _, it := range items {
			if err := step.fn(ctx, p, mech, it); err != nil {
				if rateLimited(err) {
					return nil
				}
				errs = append(errs, fmt.Errorf("%s: %w", it.ID, err))
			}
		}
	}
	if sw, ok := mech.(Sweeper); ok {
		if err := sw.Sweep(ctx, p, func(ctx context.Context, n int) (bool, error) {
			ok, err := h.eligible(ctx, p, n)
			if ok || err != nil || h.cfg.Settings == nil {
				return ok, err
			}
			return h.cfg.Settings.Eligible(ctx, p, n)
		}); err != nil {
			errs = append(errs, err)
		}
	}
	err = errors.Join(errs...)
	if rateLimited(err) {
		return nil
	}
	return err
}

func rateLimited(err error) bool {
	var rl *github.RateLimitError
	return errors.As(err, &rl)
}

// findPR finds the ticket's open PR, or nil.
func (h *HandOff) findPR(ctx context.Context, p config.Project, item string) (*github.PR, error) {
	return h.cfg.GitHub.FindPR(ctx, p, github.BranchFor(item))
}

// handOff hands one ready_to_merge ticket to the mechanism: only when its
// PR is open at the reviewed head, ready for review, aimed at the default
// branch, and loop/eligible is posted on that head. A draft or a PR on
// another base waits: no mechanism merges a draft, and a merge into
// another branch is no merge of the ticket (alpha premerge.test.ts). The
// enqueue row's guard checks the head again inside the write.
// The hand-off follows the move, so a crash between the two leaves a
// queued ticket with no hand-off, which the next tick's advance repairs.
// The machine sends a conflicted PR to rework from queued only, so a PR
// that conflicts at the hand-off is enqueued and then dequeued.
func (h *HandOff) handOff(ctx context.Context, p config.Project, mech Mechanism, it machine.Item) error {
	reviewed := it.Fact(machine.FactReviewedHead)
	pr, err := h.findPR(ctx, p, it.ID)
	if err != nil || pr == nil || pr.State != "open" || pr.Merged() || pr.Head.SHA != reviewed {
		return err // the reactor owns a merge, a close and a moved head
	}
	if pr.Draft {
		return nil
	}
	base, err := h.cfg.GitHub.DefaultBranch(ctx, p)
	if err != nil || pr.Base.Ref != base {
		return err
	}
	ok, err := h.cfg.GitHub.EligiblePosted(ctx, p.ID, reviewed)
	if err != nil || !ok {
		return err
	}
	res := h.cfg.Machine.Apply(ctx, machine.Request{Project: p.ID, Item: it.ID, Verb: machine.VerbEnqueue, ExpectedStatus: machine.ReadyToMerge,
		Params: map[string]string{"head": reviewed}, Actor: actor})
	if res.Refusal != nil {
		return machine.Settled(res)
	}
	return h.start(ctx, mech, h.prOf(p, it, pr))
}

func (h *HandOff) prOf(p config.Project, it machine.Item, pr *github.PR) PR {
	return PR{Project: p, Item: it.ID, Number: pr.Number, Head: it.Fact(machine.FactReviewedHead), Base: pr.Base.Ref}
}

// start hands a queued ticket's PR to the mechanism, or sends the ticket
// to rework when the PR conflicts with its base: no mechanism merges a
// conflicted PR, and the github-queue mechanism cannot even enqueue one.
// The PR list that findPR reads carries no mergeable state, so start
// reads the PR itself. While GitHub still computes the state, the ticket
// waits in queued with no hand-off, and the next tick's advance asks
// again.
func (h *HandOff) start(ctx context.Context, mech Mechanism, pr PR) error {
	cur, err := h.cfg.GitHub.GetPR(ctx, pr.Project, pr.Number)
	if err != nil || cur.MergeableState == MergeableUnknown {
		return err
	}
	if res, ok := Conflicting(cur); ok {
		return h.dequeued(ctx, mech, pr, nil, res)
	}
	return h.give(ctx, mech, pr)
}

// give hands one PR off and records it.
func (h *HandOff) give(ctx context.Context, mech Mechanism, pr PR) error {
	if err := mech.HandOff(ctx, pr); err != nil {
		return fmt.Errorf("hand off #%d to %s: %w", pr.Number, mech.Name(), err)
	}
	return h.record(ctx, pr, mech.Name(), stateHanded, "")
}

// advance reads the outcome of one queued ticket's PR and makes the move
// it owns.
func (h *HandOff) advance(ctx context.Context, p config.Project, mech Mechanism, it machine.Item) error {
	pr, err := h.findPR(ctx, p, it.ID)
	if err != nil || pr == nil {
		return err
	}
	mp := h.prOf(p, it, pr)
	switch {
	case pr.Merged():
		return h.merged(ctx, mp, mech.Name(), pr.MergeCommitSHA)
	case pr.State != "open":
		// The reactor escalates the ticket and raises the alert (R8).
		return h.recordIf(ctx, mp, stateClosed)
	case pr.Head.SHA != mp.Head:
		// A push after the hand-off: the ticket returns to review, and
		// the mechanism lets the PR go (AE1, R8).
		if err := machine.Settled(h.cfg.Machine.HeadChanged(ctx, p.ID, it.ID, pr.Head.SHA)); err != nil {
			return err
		}
		return h.withdraw(ctx, mech, mp)
	}
	t, err := h.tracked(ctx, p.ID, pr.Number)
	if err != nil {
		return err
	}
	if t == nil || t.head != mp.Head || (t.state != stateHanded && t.state != stateRequeued) {
		return h.start(ctx, mech, mp) // the hand-off a crash or an unknown mergeable state left undone
	}
	r, err := mech.Poll(ctx, mp)
	if err != nil {
		return fmt.Errorf("poll #%d in %s: %w", pr.Number, mech.Name(), err)
	}
	switch r.Outcome {
	case Merged:
		return h.merged(ctx, mp, mech.Name(), r.SHA)
	case Closed:
		return h.recordIf(ctx, mp, stateClosed)
	case Dequeued:
		return h.dequeued(ctx, mech, mp, t, r)
	}
	return nil
}

// dequeued makes the move of one dequeue reason. A broken merge config
// escalates at once: only the operator repairs it. Any other dequeue that
// is neither a conflict nor red CI re-queues once for each head, then
// escalates. t is nil only for a conflict found before the hand-off.
func (h *HandOff) dequeued(ctx context.Context, mech Mechanism, pr PR, t *track, r Result) error {
	verb := ""
	switch r.Reason {
	case Conflict:
		verb = "dequeue_conflict"
	case Red:
		verb = "dequeue_red"
	case MergeConfig:
		verb = "escalate"
	default:
		if t == nil || t.state != stateRequeued {
			if err := mech.Requeue(ctx, pr); err != nil {
				return fmt.Errorf("requeue #%d in %s: %w", pr.Number, mech.Name(), err)
			}
			return h.record(ctx, pr, mech.Name(), stateRequeued, r.Reason)
		}
		verb = "escalate"
	}
	res := h.cfg.Machine.Apply(ctx, machine.Request{Project: pr.Project.ID, Item: pr.Item, Verb: verb, ExpectedStatus: machine.Queued,
		Params: map[string]string{"reason": string(r.Reason)}, Actor: actor})
	if res.Refusal != nil {
		return machine.Settled(res)
	}
	state := stateDequeued
	if verb == "escalate" {
		state = stateEscalated
	}
	return h.record(ctx, pr, mech.Name(), state, r.Reason)
}

// merged moves the ticket for a merge the mechanism reports. The reactor
// may see the same merge: MergeObserved answers Nothing for a ticket that
// is already done, so done is written once.
func (h *HandOff) merged(ctx context.Context, pr PR, mech config.MergeMechanism, sha string) error {
	base, err := h.cfg.GitHub.DefaultBranch(ctx, pr.Project)
	if err != nil {
		return err
	}
	res := h.cfg.Machine.MergeObserved(ctx, machine.MergeEvent{Project: pr.Project.ID, Item: pr.Item, Base: base, Into: pr.Base, SHA: sha})
	if res.Refusal != nil && res.Refusal.Code != cli.CodeNotFound {
		return fmt.Errorf("#%d merged: %s: %s", pr.Number, res.Refusal.Code, res.Refusal.Hint)
	}
	return h.record(ctx, pr, mech, stateMerged, "")
}

// withdrawStale lets go of every PR whose ticket left queued since its
// hand-off, for example when the reactor saw a push first.
func (h *HandOff) withdrawStale(ctx context.Context, p config.Project, cur Mechanism) error {
	tracks, err := h.open(ctx, p.ID)
	if err != nil {
		return err
	}
	var errs []error
	for _, t := range tracks {
		it, ref := h.cfg.Machine.Show(ctx, p.ID, t.item)
		switch {
		case ref != nil && ref.Code == cli.CodeNotFound:
			continue
		case ref != nil:
			errs = append(errs, fmt.Errorf("%s: %s", t.item, ref.Hint))
			continue
		case it.Status == machine.Queued && it.Fact(machine.FactReviewedHead) == t.head:
			continue
		}
		pr := PR{Project: p, Item: t.item, Number: t.pr, Head: t.head}
		if it.Status == machine.Done {
			errs = append(errs, h.record(ctx, pr, config.MergeMechanism(t.mechanism), stateMerged, ""))
			continue
		}
		mech := cur
		if m, ok := h.cfg.Mechanisms[config.MergeMechanism(t.mechanism)]; ok {
			mech = m // the mechanism that holds the PR, even after a config change
		}
		errs = append(errs, h.withdraw(ctx, mech, pr))
	}
	return errors.Join(errs...)
}

func (h *HandOff) withdraw(ctx context.Context, mech Mechanism, pr PR) error {
	if err := mech.Withdraw(ctx, pr); err != nil {
		return fmt.Errorf("withdraw #%d from %s: %w", pr.Number, mech.Name(), err)
	}
	return h.recordIf(ctx, pr, stateWithdrawn)
}

// eligible reports whether PR n is eligible at its queued head: its
// ticket is queued at the head a review passed, the job handed off that
// head, the PR still holds it, and loop/eligible is posted on it.
func (h *HandOff) eligible(ctx context.Context, p config.Project, n int) (bool, error) {
	pr, err := h.cfg.GitHub.GetPR(ctx, p, n)
	if err != nil {
		return false, err
	}
	item := github.ItemOf(pr.Head.Ref)
	if item == "" || !pr.FromRepo(p) || pr.State != "open" {
		return false, nil
	}
	it, ref := h.cfg.Machine.Show(ctx, p.ID, item)
	if ref != nil {
		if ref.Code == cli.CodeNotFound {
			return false, nil
		}
		return false, fmt.Errorf("%s: %s", item, ref.Hint)
	}
	head := it.Fact(machine.FactReviewedHead)
	if it.Kind != machine.KindTicket || it.Status != machine.Queued || head == "" || pr.Head.SHA != head {
		return false, nil
	}
	t, err := h.tracked(ctx, p.ID, n)
	if err != nil || t == nil || t.head != head || (t.state != stateHanded && t.state != stateRequeued) {
		return false, err
	}
	return h.cfg.GitHub.EligiblePosted(ctx, p.ID, head)
}

// track is one merge_tracking row.
type track struct {
	pr                           int
	item, head, mechanism, state string
}

func (h *HandOff) tracked(ctx context.Context, project string, pr int) (*track, error) {
	t := track{pr: pr}
	err := h.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT item, head_sha, mechanism, state FROM merge_tracking WHERE project = ? AND pr_number = ?`,
			project, pr).Scan(&t.item, &t.head, &t.mechanism, &t.state)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// open lists the PRs a mechanism holds.
func (h *HandOff) open(ctx context.Context, project string) ([]track, error) {
	var out []track
	err := h.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT pr_number, item, head_sha, mechanism, state FROM merge_tracking
			WHERE project = ? AND state IN (?, ?) ORDER BY pr_number`, project, stateHanded, stateRequeued)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var t track
			if err := rows.Scan(&t.pr, &t.item, &t.head, &t.mechanism, &t.state); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// record writes the PR's hand-off state. A hand-off sets enqueued_at,
// and a hand-off or a re-queue counts one attempt. A dequeue reason
// stays until the next dequeue replaces it, so `merge status` still
// names why a PR came back after it is handed off again.
func (h *HandOff) record(ctx context.Context, pr PR, mech config.MergeMechanism, state string, reason Reason) error {
	return h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		var enq any
		if state == stateHanded {
			enq = now
		}
		attempts := 0
		if state == stateHanded || state == stateRequeued {
			attempts = 1
		}
		_, err := tx.Exec(`INSERT INTO merge_tracking (project, pr_number, item, head_sha, mechanism, state, reason, attempts,
				enqueued_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (project, pr_number) DO UPDATE SET item = excluded.item, head_sha = excluded.head_sha,
				mechanism = excluded.mechanism, state = excluded.state,
				reason = CASE excluded.reason WHEN '' THEN merge_tracking.reason ELSE excluded.reason END,
				attempts = merge_tracking.attempts + excluded.attempts,
				enqueued_at = coalesce(excluded.enqueued_at, merge_tracking.enqueued_at), updated_at = excluded.updated_at`,
			pr.Project.ID, pr.Number, pr.Item, pr.Head, string(mech), state, string(reason), attempts, enq, now)
		return err
	})
}

// recordIf moves the state of a PR the job tracks, and records nothing
// for a PR it never handed off.
func (h *HandOff) recordIf(ctx context.Context, pr PR, state string) error {
	return h.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE merge_tracking SET state = ?, updated_at = ? WHERE project = ? AND pr_number = ? AND state IN (?, ?)`,
			state, tx.Now().UnixMilli(), pr.Project.ID, pr.Number, stateHanded, stateRequeued)
		return err
	})
}
