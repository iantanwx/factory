package sched

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/hexid"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// Defaults. Host config's run_budget replaces DefaultRunBudget.
const (
	DefaultTickInterval     = 15 * time.Second
	DefaultLivenessInterval = 15 * time.Second // KTD8
	DefaultRunBudget        = 2 * time.Hour
	DefaultSetupTimeout     = 15 * time.Minute
	DefaultDrainTimeout     = 30 * time.Minute
	DefaultQuotaPause       = time.Hour
	DefaultCleanupInterval  = 5 * time.Minute
	// DefaultBaseBranch is the base of every checkout. A registration names
	// no base branch in v1.
	DefaultBaseBranch = "main"
)

// Alert kinds the scheduler raises. The inbox reads the alerts table.
// The two coordinator-owned kinds ask the coordinator to look and never
// make the server act (KTD9).
const (
	AlertMissingSecret  = "missing_secret"
	AlertConfigConflict = "config_conflict"
	AlertStartFailed    = "run_start_failed"
	AlertRunCrashed     = "run_crashed"
	AlertRunRepeated    = "run_uncounted_repeat"
	AlertRunOverBudget  = "run_over_budget"      // coordinator-owned
	AlertRunUnknown     = "run_liveness_unknown" // coordinator-owned
	// AlertUsageLimit names one usage-limit window of one harness. A person
	// decides whether to wait for the reset or to change the account.
	AlertUsageLimit  = "usage_limit"
	AlertCoordinator = "coordinator_start_failed"
	// AlertRetryLimit names an item that a stage stopped taking after
	// admission.FailureLimit counted failures with unchanged inputs.
	AlertRetryLimit = "retry_limit"
	// AlertRunBlocked names an item a run left blocked. While it is open,
	// it holds the item at that stage (admission.BlockedKey).
	AlertRunBlocked = admission.AlertBlocked
)

// HostSource returns the current host config. *config.HostLoader is one.
type HostSource interface {
	Load(ctx context.Context) (*config.HostConfig, error)
}

// ProjectSource returns a project's config and manifest from its default
// branch, or the last known good one (KTD20).
type ProjectSource func(ctx context.Context, project string) (*config.ProjectSnapshot, error)

// CheckoutHost makes and deletes one project's checkouts. The orca host
// needs the project's repository for a new checkout, so each project may
// have its own.
type CheckoutHost interface {
	CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error)
	RemoveCheckout(ctx context.Context, path string) error
}

// JobFunc is one native job's work for one project (R24).
type JobFunc func(ctx context.Context, project string) error

// InstanceJobFunc is one native job's work for the whole instance.
type InstanceJobFunc func(ctx context.Context) error

// Config configures a Scheduler.
type Config struct {
	Store         *store.Store
	Host          HostSource
	Projects      func() ([]config.Project, error)
	ProjectConfig ProjectSource
	Machine       *machine.Machine
	Admission     *admission.Admission
	// Beads reads an item's body for the brief and the dependency edges of
	// dependency-advance. Optional. With none, dependency-advance is not
	// registered.
	Beads machine.BeadsResolver
	// Pulls lists a project's open pull requests. dependency-advance reads
	// it to learn whether a canceled blocker landed. Optional. With none, a
	// canceled blocker holds its dependants.
	Pulls   admission.PullReader
	Secrets *secrets.Store
	Tokens  *rpc.Tokens
	// Agents is the agent host: start, inventory, liveness, close.
	Agents agenthost.Host
	// Checkouts returns a project's checkout host. Agents when nil.
	Checkouts func(project string) CheckoutHost
	Pack      brief.Pack
	Alerts    config.Alerter
	// Release is the loop release a run starts under. cli.Version when
	// empty.
	Release string
	// Coordinator starts the coordinator session with the service. Nil
	// starts none.
	Coordinator *CoordinatorConfig

	Now              func() time.Time
	TickInterval     time.Duration
	LivenessInterval time.Duration
	SetupTimeout     time.Duration
	DrainTimeout     time.Duration
	QuotaPause       time.Duration
	CleanupInterval  time.Duration
	Logger           *slog.Logger
	// NewRunID names a run. A random id when nil.
	NewRunID func() string
}

// Scheduler is the server's one scheduler.
type Scheduler struct {
	cfg Config
	log *slog.Logger

	jobsMu       sync.RWMutex
	jobs         map[string]JobFunc
	instanceJobs map[string]InstanceJobFunc

	mu        sync.Mutex
	inflight  map[string]int
	draining  bool
	drainEnd  time.Time
	lastCall  map[string]time.Time
	lastClean time.Time

	settleMu sync.Mutex // one settlement at a time
	// checkoutMu orders checkout creation: two git worktree adds on one
	// repository race for its config lock.
	checkoutMu sync.Mutex
	tickMu     sync.Mutex // one tick at a time

	wg     sync.WaitGroup // dispatches and jobs in flight
	loops  sync.WaitGroup
	cancel context.CancelFunc
}

// New checks cfg and fills its defaults. It starts nothing.
func New(cfg Config) (*Scheduler, error) {
	if cfg.Store == nil || cfg.Host == nil || cfg.Projects == nil || cfg.ProjectConfig == nil || cfg.Machine == nil ||
		cfg.Admission == nil || cfg.Secrets == nil || cfg.Tokens == nil || cfg.Agents == nil || cfg.Alerts == nil {
		return nil, errors.New("sched: a scheduler needs a store, host and project config, the machine, admission, secrets, tokens, an agent host and an alerter")
	}
	if cfg.Checkouts == nil {
		agents := cfg.Agents
		cfg.Checkouts = func(string) CheckoutHost { return agents }
	}
	if cfg.Release == "" {
		cfg.Release = cli.Version
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&cfg.TickInterval, DefaultTickInterval)
	def(&cfg.LivenessInterval, DefaultLivenessInterval)
	def(&cfg.SetupTimeout, DefaultSetupTimeout)
	def(&cfg.DrainTimeout, DefaultDrainTimeout)
	def(&cfg.QuotaPause, DefaultQuotaPause)
	def(&cfg.CleanupInterval, DefaultCleanupInterval)
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.NewRunID == nil {
		cfg.NewRunID = randomRunID
	}
	s := &Scheduler{
		cfg: cfg, log: cfg.Logger,
		jobs: map[string]JobFunc{}, instanceJobs: map[string]InstanceJobFunc{},
		inflight: map[string]int{}, lastCall: map[string]time.Time{}, lastClean: cfg.Now(),
	}
	if cfg.Beads != nil {
		if err := s.RegisterJob(DependencyAdvanceJob, s.advanceDependencies); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func randomRunID() string { return "r-" + hexid.Random(6) }

func (s *Scheduler) now() time.Time { return s.cfg.Now() }

// RegisterJob sets the work of one native job for each project. The job
// ticks on its throughput schedule, or the project's override (KTD30). The
// server registers merge-handoff and decision-timers here. New registers
// dependency-advance itself.
func (s *Scheduler) RegisterJob(name string, fn JobFunc) error {
	return s.register(name, func() { s.jobs[name] = fn })
}

// RegisterInstanceJob sets the work of one native job that covers every
// project in one call, such as the GitHub reactor. It ticks on host
// config's schedule only.
func (s *Scheduler) RegisterInstanceJob(name string, fn InstanceJobFunc) error {
	return s.register(name, func() { s.instanceJobs[name] = fn })
}

// register runs add under the jobs lock when name is a native job that
// neither kind of job holds yet.
func (s *Scheduler) register(name string, add func()) error {
	if !slices.Contains(config.NativeJobs(), name) {
		return fmt.Errorf("sched: %q is no native job; config.NativeJobs lists them", name)
	}
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	_, dup := s.jobs[name]
	_, dupInstance := s.instanceJobs[name]
	if dup || dupInstance {
		return fmt.Errorf("sched: native job %q is registered twice", name)
	}
	add()
	return nil
}

func (s *Scheduler) job(name string) JobFunc {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	return s.jobs[name]
}

func (s *Scheduler) instanceJob(name string) InstanceJobFunc {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()
	return s.instanceJobs[name]
}

// Start reconciles (R17), then starts the tick and liveness loops. The
// server runs it as its last start-up step, before the socket binds. The
// loops end with ctx or Stop.
func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.Reconcile(ctx); err != nil {
		return err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.loops.Add(2)
	go func() {
		defer s.loops.Done()
		every(loopCtx, s.cfg.LivenessInterval, func(c context.Context) {
			if err := s.Heartbeat(c); err != nil && c.Err() == nil {
				s.log.ErrorContext(c, "liveness tick", "err", err)
			}
		})
	}()
	go func() {
		defer s.loops.Done()
		every(loopCtx, s.cfg.TickInterval, func(c context.Context) {
			if err := s.Tick(c); err != nil && c.Err() == nil {
				s.log.ErrorContext(c, "scheduler tick", "err", err)
			}
		})
	}()
	return nil
}

// every runs f at once and then each interval until ctx ends. A slow f
// drops the ticks it missed rather than queueing them.
func every(ctx context.Context, d time.Duration, f func(context.Context)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		f(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Stop ends the loops, waits for dispatches and jobs in flight while ctx
// allows, and writes a last liveness tick, so the next start's outage span
// begins at the stop (R16).
func (s *Scheduler) Stop(ctx context.Context) error {
	if s.cancel != nil {
		s.cancel()
	}
	done := make(chan struct{})
	go func() {
		s.loops.Wait()
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return s.Heartbeat(context.WithoutCancel(ctx))
}

// Wait waits for every dispatch and job a tick launched.
func (s *Scheduler) Wait() { s.wg.Wait() }

// Tick is one pass: it watches every live run, closes the sessions of
// finished runs, frees leases a settlement could not release, keeps the
// coordinator session up, fires each due stage and job, and cleans
// checkouts and expired idempotency keys up. One step's fault does not
// stop the others.
func (s *Scheduler) Tick(ctx context.Context) error {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()
	hc, err := s.cfg.Host.Load(ctx)
	if err != nil {
		return fmt.Errorf("sched: load host config: %w", err)
	}
	if err := s.watch(ctx, hc, false); err != nil {
		s.log.ErrorContext(ctx, "watch runs", "err", err)
	}
	if err := s.closeEnded(ctx); err != nil {
		s.log.ErrorContext(ctx, "close ended sessions", "err", err)
	}
	if err := s.sweepLeases(ctx); err != nil {
		s.log.ErrorContext(ctx, "sweep leases", "err", err)
	}
	s.ensureCoordinator(ctx)
	if err := s.fireDue(ctx, hc); err != nil {
		s.log.ErrorContext(ctx, "fire due stages", "err", err)
	}
	s.pruneCalls()
	s.mu.Lock()
	clean := s.now().Sub(s.lastClean) >= s.cfg.CleanupInterval
	if clean {
		s.lastClean = s.now()
	}
	s.mu.Unlock()
	if clean {
		s.CleanCheckouts(ctx)
		s.purgeKeys(ctx)
	}
	return nil
}

// purgeKeys deletes each idempotency key whose wait budget passed after
// its run settled (KTD13). A key whose write is in flight stays.
func (s *Scheduler) purgeKeys(ctx context.Context) {
	var n int64
	err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		n, err = tx.PurgeExpiredKeys(tx.Now())
		return err
	})
	if err != nil {
		s.log.ErrorContext(ctx, "purge expired idempotency keys", "err", err)
		return
	}
	if n > 0 {
		s.log.InfoContext(ctx, "purged expired idempotency keys", "keys", n)
	}
}

// stageKey names one project's stage or job.
func stageKey(project, stage string) string { return project + "/" + stage }

type cronRow struct {
	expr string
	due  sql.NullInt64
	ok   bool
}

// fireDue fires each stage and job whose deadline passed. Each deadline
// lives in the schedule table, so a deadline missed while the server was
// down fires once on the next start.
func (s *Scheduler) fireDue(ctx context.Context, hc *config.HostConfig) error {
	now := s.now()
	rows := map[string]cronRow{}
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT project, stage, cron_expr, due_at FROM schedule WHERE kind = 'cron'`)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var p, st string
			var r cronRow
			if err := rs.Scan(&p, &st, &r.expr, &r.due); err != nil {
				return err
			}
			r.ok = true
			rows[stageKey(p, st)] = r
		}
		return rs.Err()
	})
	if err != nil {
		return err
	}
	for _, name := range config.NativeJobs() {
		if s.instanceJob(name) == nil {
			continue
		}
		if s.advance(ctx, rows, "", name, hc.ThroughputFor(name).Schedule, now) {
			s.launch(ctx, "", name, kindInstanceJob, hc)
		}
	}
	projects, err := s.cfg.Projects()
	if err != nil {
		return err
	}
	for _, p := range projects {
		snap, err := s.cfg.ProjectConfig(ctx, p.ID)
		if err != nil {
			if !errors.Is(err, config.ErrConfigMissing) {
				s.log.WarnContext(ctx, "project config", "project", p.ID, "err", err)
			}
			snap = nil
		}
		// An agent job is a run stage that host config defines, so it fires
		// as one: only where the project lists it.
		stages := append(append(config.RunStages(), hc.AgentJobNames()...), config.NativeJobs()...)
		for _, stage := range stages {
			kind := kindRun
			if slices.Contains(config.NativeJobs(), stage) {
				kind = kindJob
				if s.job(stage) == nil {
					continue
				}
			} else if snap == nil || !snap.Config.StageEnabled(stage) || stage == config.StageIntake {
				continue
			}
			expr := hc.ThroughputFor(stage).Schedule
			if j, ok := hc.AgentJob(stage); ok {
				expr = j.Schedule
			}
			if snap != nil {
				if sc, ok := snap.Config.Stages[stage]; ok && sc.Schedule != "" {
					expr = sc.Schedule
				}
			}
			if s.advance(ctx, rows, p.ID, stage, expr, now) {
				s.launch(ctx, p.ID, stage, kind, hc)
			}
		}
	}
	return nil
}

// advance moves one deadline and reports whether it fired. A new or
// changed cron sets the deadline after now and does not fire.
func (s *Scheduler) advance(ctx context.Context, rows map[string]cronRow, project, stage, expr string, now time.Time) bool {
	r := rows[stageKey(project, stage)]
	fire := false
	var due time.Time
	switch {
	case !r.ok || r.expr != expr || !r.due.Valid:
		next, err := NextDue(expr, now)
		if err != nil {
			s.log.ErrorContext(ctx, "bad schedule", "project", project, "stage", stage, "cron", expr, "err", err)
			return false
		}
		due = next
	default:
		c := cronState{expr: expr, due: time.UnixMilli(r.due.Int64)}
		var err error
		if fire, err = c.advance(now); err != nil {
			s.log.ErrorContext(ctx, "bad schedule", "project", project, "stage", stage, "cron", expr, "err", err)
			return false
		}
		if !fire {
			return false
		}
		due = c.due
	}
	err := s.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		ms := tx.Now().UnixMilli()
		var fired sql.NullInt64
		if fire {
			fired = sql.NullInt64{Int64: now.UnixMilli(), Valid: true}
		}
		res, err := tx.Exec(`UPDATE schedule SET cron_expr = ?, due_at = ?, last_fired_at = coalesce(?, last_fired_at), updated_at = ?
			WHERE kind = 'cron' AND project = ? AND stage = ?`, expr, due.UnixMilli(), fired, ms, project, stage)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
		_, err = tx.Exec(`INSERT INTO schedule (project, kind, stage, due_at, cron_expr, last_fired_at, created_at, updated_at)
			VALUES (?, 'cron', ?, ?, ?, ?, ?, ?)`, project, stage, due.UnixMilli(), expr, fired, ms, ms)
		return err
	})
	if err != nil {
		s.log.ErrorContext(ctx, "store a deadline", "project", project, "stage", stage, "err", err)
		return false
	}
	return fire
}

type launchKind int

const (
	kindRun launchKind = iota
	kindJob
	kindInstanceJob
)

// launch starts one firing in its own goroutine. A stage starts at most one
// firing at a time for each project, and a job at most its cap of
// concurrent calls. A run stage's firing fills its free cap.
func (s *Scheduler) launch(ctx context.Context, project, stage string, kind launchKind, hc *config.HostConfig) {
	key := stageKey(project, stage)
	limit := 1
	if kind != kindRun {
		limit = max(1, hc.ThroughputFor(stage).Cap)
	}
	s.mu.Lock()
	if s.inflight[key] >= limit || kind == kindRun && s.draining {
		s.mu.Unlock()
		return
	}
	s.inflight[key]++
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			s.inflight[key]--
			s.mu.Unlock()
		}()
		switch kind {
		case kindRun:
			s.fill(ctx, project, stage, max(1, hc.Throughput[stage].Cap))
		case kindJob:
			if err := s.job(stage)(ctx, project); err != nil {
				s.log.WarnContext(ctx, "native job", "project", project, "job", stage, "err", err)
			}
		case kindInstanceJob:
			if err := s.instanceJob(stage)(ctx); err != nil {
				s.log.WarnContext(ctx, "native job", "job", stage, "err", err)
			}
		}
	}()
}

// fill starts runs of stage on project until admission refuses, no
// candidate is left, or limit runs started. Admission holds the stage's
// cap, so a firing fills the free cap. Tickertown runs four implementer
// rows that each hold a window of runs, so 16 starts an hour reach its cap
// of 8, shared by new and repair work (scripts/sdlc/timer-implementer-rows.ts,
// alpha). A stage that claims no item starts one run a firing: its
// candidate names no work that a second run would not repeat.
func (s *Scheduler) fill(ctx context.Context, project, stage string, limit int) {
	for range limit {
		st, ref := s.Dispatch(ctx, project, stage)
		if ref != nil {
			s.log.InfoContext(ctx, "dispatch refused", "project", project, "stage", stage, "code", ref.Code, "hint", ref.Hint)
			return
		}
		if st == nil {
			return
		}
		s.log.InfoContext(ctx, "run started", "project", project, "stage", stage, "run", st.RunID, "item", st.Item)
		if st.Item == "" {
			return
		}
	}
}

// raise raises an alert, or counts up the open one with the same key.
func (s *Scheduler) raise(ctx context.Context, a config.Alert) {
	if err := s.cfg.Alerts.Raise(context.WithoutCancel(ctx), a); err != nil {
		s.log.ErrorContext(ctx, "raise an alert", "kind", a.Kind, "err", err)
		return
	}
	s.log.WarnContext(ctx, "alert", "kind", a.Kind, "project", a.Project, "subject", a.Subject)
}

// raiseOnce raises an alert only when no alert with its key exists, open or
// closed. A condition that holds on every tick then raises one item.
func (s *Scheduler) raiseOnce(ctx context.Context, a config.Alert) {
	var n int
	err := s.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM alerts WHERE dedupe_key = ?`, a.DedupeKey).Scan(&n)
	})
	if err != nil {
		s.log.ErrorContext(ctx, "read alerts", "err", err)
		return
	}
	if n == 0 {
		s.raise(ctx, a)
	}
}
