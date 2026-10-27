package github

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/shape"
	"github.com/iantanwx/loop/internal/store"
)

// Alert kinds the GitHub actor and reactor raise. The inbox reads the
// alerts table.
const (
	// AlertForeignHead: a PR head that the server did not push (KTD14).
	AlertForeignHead = "foreign_head"
	// AlertPRClosed: a PR closed without merge escalated its item (R8).
	AlertPRClosed = "pr_closed"
	// AlertEffectFailed: GitHub kept refusing an outbox effect.
	AlertEffectFailed = "github_effect_failed"
	// AlertCINotStarted: GitHub failed a check whose job it never
	// started, such as for billing (F-18). One alert per cause.
	AlertCINotStarted = "ci_not_started"
)

// StreamPulls is the reactor's cursor stream of updated PRs.
const StreamPulls = "pulls"

// maxPullPages bounds one tick's walk back through updated PRs.
const maxPullPages = 10

// Reactor reacts to PR and CI changes (KTD16). The devbox has no public
// webhook, so it polls with a cursor in SQLite. A merge into the base
// moves any non-terminal item to done, a closed PR escalates its item, a
// new head after a review pass returns the ticket to review, and red CI
// on a ticket in review sends it to repair, as does a conflict with the
// base. CI cancelled by a newer push
// is superseded, not red. An ADR on the base whose Ticket line names an
// architecture epic moves the epic to spec_needed. A person's unanswered
// comment on a document in review or sign-off moves its epic to the amender.
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
			pr, err := r.s.FindPR(ctx, p, BranchOf(it))
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
// the shared cooldown holds, a tick reads nothing. The scheduler calls it
// as the github-reactor native job.
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

// Run ticks every interval until ctx ends. The scheduler may call Tick
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
	base, sha, err := r.s.SyncBase(ctx, p)
	if err != nil {
		return err
	}
	var errs []error
	if err := r.pollPulls(ctx, p, base); err != nil {
		errs = append(errs, err)
	}
	if err := r.adrs(ctx, p, sha); err != nil {
		errs = append(errs, err)
	}
	if err := r.pollCI(ctx, p); err != nil {
		errs = append(errs, err)
	}
	if err := r.amendments(ctx, p); err != nil {
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

// ADRDir is where the reactor looks for an architecture decision record:
// every Markdown file under it on the base branch. Project config names no
// other place in v1.
const ADRDir = "docs/adr/"

// adrTicket is an ADR's Ticket line, the pattern of the adr gate
// (internal/gates/adr.go): the id of the architecture epic the ADR
// decides, or none.
var adrTicket = regexp.MustCompile(`(?m)^\*\*Ticket:\*\*\s*(\S+)`)

// ticketOf is the id on an ADR's Ticket line, with the trailing
// punctuation the gate allows trimmed. An ADR with no Ticket line gives "".
func ticketOf(text string) string {
	m := adrTicket.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return strings.TrimRight(m[1], ".,;")
}

// adrs applies adr_landed to each architecture epic whose id the Ticket
// line of an ADR on the base branch names (R46): a merge that adds the ADR
// to the base is the operator's sign-off. An epic the ADR names anywhere
// else stays where it is. It reads the ADR's text in the mirror at sha,
// which SyncBase just fetched, and never a GitHub answer. A read finds an
// ADR that landed while the server was down too. The first ADR in path
// order whose Ticket line names the epic is the one the epic records.
//
// It also returns to architecture each spec_needed epic whose adr fact
// names an ADR on the base whose Ticket line names another epic, or none:
// the reactor once joined an ADR to every epic its text named (F-10).
func (r *Reactor) adrs(ctx context.Context, p config.Project, sha string) error {
	epics, ref := r.m.List(ctx, p.ID, machine.ListFilter{Kind: machine.KindEpic, Status: machine.Architecture})
	if ref != nil {
		return fmt.Errorf("list architecture epics: %s", ref.Hint)
	}
	landed, ref := r.m.List(ctx, p.ID, machine.ListFilter{Kind: machine.KindEpic, Status: machine.SpecNeeded})
	if ref != nil {
		return fmt.Errorf("list spec_needed epics: %s", ref.Hint)
	}
	landed = slices.DeleteFunc(landed, func(ep machine.Item) bool { return ep.Fact(machine.FactADR) == "" })
	if len(epics) == 0 && len(landed) == 0 || sha == "" {
		return nil
	}
	files, err := r.s.Files(ctx, p.ID, []string{ADRDir})
	if err != nil {
		return err
	}
	var paths []string
	for path := range files {
		if strings.HasSuffix(path, ".md") {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	tickets := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := r.s.Mirror(p).Show(ctx, sha, path)
		if err != nil {
			return err
		}
		if id := ticketOf(string(data)); id != "" {
			tickets[path] = id
		}
	}
	var errs []error
	for _, ep := range landed {
		path := ep.Fact(machine.FactADR)
		if id, ok := tickets[path]; !ok || id == ep.ID {
			continue
		}
		res := r.m.Apply(ctx, machine.Request{Project: p.ID, Item: ep.ID, Verb: machine.VerbADRMisjoined, ExpectedStatus: machine.SpecNeeded,
			Params: map[string]string{"path": path}, Actor: machine.Server("reactor")})
		if err := machine.Settled(res); err != nil {
			errs = append(errs, fmt.Errorf("%s adr_misjoined: %w", ep.ID, err))
		} else if res.Refusal == nil {
			r.log.InfoContext(ctx, "returned to architecture an epic that its ADR's Ticket line does not name",
				"project", p.ID, "item", ep.ID, "adr", path, "ticket", tickets[path])
		}
	}
	for _, ep := range epics {
		i := slices.IndexFunc(paths, func(path string) bool { return tickets[path] == ep.ID })
		if i < 0 {
			continue
		}
		res := r.m.Apply(ctx, machine.Request{Project: p.ID, Item: ep.ID, Verb: machine.VerbADRLanded, ExpectedStatus: machine.Architecture,
			Params: map[string]string{"path": paths[i], "sha": sha}, Actor: machine.Server("reactor")})
		if err := machine.Settled(res); err != nil {
			errs = append(errs, fmt.Errorf("%s adr_landed: %w", ep.ID, err))
		}
	}
	return errors.Join(errs...)
}

// pollCI reads the CI of each ticket in review. A check run's end does not
// change the PR's update time, so the PR cursor cannot see it. A check
// that GitHub never started built nothing: the ticket stays in review,
// and the project gets one alert for the cause, not one per ticket.
//
// A PR that conflicts with its base is red too, whatever its checks say
// (F-11). GitHub starts no pull_request workflow on a conflicting PR, so
// its CI never turns green and no review of it can pass; only a merge of
// the base repairs it. The PR list carries no mergeable state, so this
// reads the PR by number. An unknown state is no conflict: the read asks
// GitHub to compute it, and the next tick reads it again. Ported from
// health in scripts/sdlc/pr-health.ts (alpha).
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
		red := rep.State == CIRed
		if !red {
			cur, err := r.s.GetPR(ctx, p, pr.Number)
			if err != nil {
				return err
			}
			red = cur.State == "open" && cur.MergeableState == MergeableDirty
		}
		if u := rep.Unstarted; u != nil && !red {
			if err := r.alert(ctx, rpc.AuditAlert{Actor: "server:reactor", Project: p.ID, Item: it.ID, Kind: AlertCINotStarted,
				Subject: p.ID + "/" + u.Cause,
				Body: fmt.Sprintf("GitHub never started a CI job on %s, so tickets in review hold there and no repair starts. GitHub said: %q. Fix the cause, then rerun CI.",
					p.ID, u.Message),
				Detail: map[string]any{"cause": u.Cause, "head": pr.Head.SHA, "pr": pr.Number}}); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if !red {
			continue
		}
		if rep.State != CIRed {
			r.log.InfoContext(ctx, "a ticket in review goes to repair: its PR conflicts with its base",
				"project", p.ID, "item", it.ID, "pr", pr.Number)
		}
		res := r.m.Apply(ctx, machine.Request{Project: p.ID, Item: it.ID, Verb: machine.VerbCIRed, ExpectedStatus: machine.InReview,
			Params: map[string]string{"head": pr.Head.SHA}, Actor: machine.Server("reactor")})
		if err := machine.Settled(res); err != nil {
			errs = append(errs, fmt.Errorf("%s ci_red: %w", it.ID, err))
		}
	}
	return errors.Join(errs...)
}

// amendments moves to the amender each epic whose open document PR holds
// a person's comment that the loop has not answered (1.12): a PRD or a
// spec in review or in sign-off. The machine refuses to move an epic a
// run holds, so a held epic waits, and loses nothing: only a reply
// answers the comment, so the next tick after the run ends reads it
// again. A comment does not change a PR's head or its CI, and a held
// epic leaves the PR cursor behind, so this reads the threads of each
// such epic on every tick, as pollCI reads the CI. Ported from amendQueue in scripts/sdlc/pr-amend.ts (alpha).
func (r *Reactor) amendments(ctx context.Context, p config.Project) error {
	var errs []error
	for _, state := range machine.AmendableStates() {
		epics, ref := r.m.List(ctx, p.ID, machine.ListFilter{Kind: machine.KindEpic, Status: state})
		if ref != nil {
			return fmt.Errorf("list epics in %s: %s", state, ref.Hint)
		}
		for _, ep := range epics {
			n, err := strconv.Atoi(ep.Fact(machine.FactDocPR))
			if err != nil {
				continue
			}
			// awaitsAnswer reads authors and order alone, so the poll
			// reads REST and needs no thread state from GraphQL.
			cs, err := r.s.allComments(ctx, p, n)
			if err != nil {
				return err
			}
			if !awaitsAnswer(cs) {
				continue
			}
			// A merged or closed PR is the PR poll's to read.
			pr, err := r.s.GetPR(ctx, p, n)
			if err != nil {
				return err
			}
			if pr.State != "open" {
				continue
			}
			res := r.m.Apply(ctx, machine.Request{Project: p.ID, Item: ep.ID, Verb: machine.VerbPersonComment, ExpectedStatus: state,
				Params: map[string]string{"head": pr.Head.SHA, "pr": strconv.Itoa(n)}, Actor: machine.Server("reactor")})
			if res.Refusal != nil && res.Refusal.Code == cli.CodeLeaseHeld {
				continue
			}
			if err := machine.Settled(res); err != nil {
				errs = append(errs, fmt.Errorf("%s person_comment: %w", ep.ID, err))
			}
		}
	}
	return errors.Join(errs...)
}

// handle reacts to one PR of the project.
func (r *Reactor) handle(ctx context.Context, p config.Project, base string, pr PR) error {
	item := ItemOf(pr.Head.Ref)
	if item == "" || !pr.fromRepo(p) {
		return nil
	}
	it, ref := r.m.Show(ctx, p.ID, item)
	if ref != nil && ref.Code == cli.CodeNotFound {
		// A spec branch names its epic with a suffix (SpecBranchFor).
		if epic := SpecEpicOf(pr.Head.Ref); epic != "" {
			item = epic
			it, ref = r.m.Show(ctx, p.ID, item)
		}
	}
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
	// An epic's PRD and its spec each have a branch and a PR. Only the
	// current document's PR moves the epic.
	if it.Kind == machine.KindEpic && it.Fact(machine.FactDocPR) != strconv.Itoa(pr.Number) {
		return nil
	}
	switch {
	case pr.Merged():
		// A merge wins from any state (KTD16). MergeObserved retries a
		// status miss and answers Nothing for a terminal item. Any other
		// refusal keeps the cursor, so the merge is read again.
		ev := machine.MergeEvent{Project: p.ID, Item: item, Base: base, Into: pr.Base.Ref, SHA: pr.MergeCommitSHA}
		if it.Kind == machine.KindEpic && SpecEpicOf(pr.Head.Ref) == item && slices.Contains(specDocStates, it.Status) {
			path, err := r.specPath(ctx, p, item, pr.Number)
			if err != nil {
				return fmt.Errorf("%s merged as #%d: %w", item, pr.Number, err)
			}
			ev.SpecPath = path
		}
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

// specDocStates are the epic states whose document PR is the spec's.
var specDocStates = []string{machine.SpecReview, machine.SpecAmending, machine.SpecSignoff}

// specPath is the path of epic's spec among the changed files of PR n, or ""
// when no single file is the spec.
func (r *Reactor) specPath(ctx context.Context, p config.Project, epic string, n int) (string, error) {
	files, err := r.s.PRFiles(ctx, p, n)
	if err != nil {
		return "", err
	}
	changed := make([]shape.ChangedFile, len(files))
	for i, f := range files {
		changed[i] = shape.ChangedFile{Path: f.Path, Removed: f.Status == "removed"}
	}
	path, _ := shape.SpecFile(epic, changed)
	return path, nil
}

// closed escalates an item whose PR closed without merge (R8). The loop
// never reopens a PR.
func (r *Reactor) closed(ctx context.Context, p config.Project, it machine.Item, pr PR) error {
	t, _ := machine.TableFor(it.Kind)
	if !t.Allows("pr_closed", it.Status) {
		return nil
	}
	res := r.m.Apply(ctx, machine.Request{Project: p.ID, Item: it.ID, Verb: machine.VerbPRClosed, ExpectedStatus: it.Status,
		Params: map[string]string{"pr": strconv.Itoa(pr.Number)}, Actor: machine.Server("reactor")})
	if res.Refusal != nil {
		return machine.Settled(res)
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
	return machine.Settled(r.m.HeadChanged(ctx, p.ID, it.ID, sha))
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
