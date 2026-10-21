package tracker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
)

// Defaults of Options.
const (
	DefaultBatchSize      = 100
	DefaultCallTimeout    = time.Minute
	DefaultCooldown       = 30 * time.Second
	DefaultInterval       = 5 * time.Second
	DefaultBatchesPerTick = 50
)

// Project is one project that delivery knows. Adapter is nil when the
// project has no tracker (R28): delivery then never reads its journal.
type Project struct {
	ID string
	// Journal is the beads client of the project's workspace. Delivery calls
	// only Tail and List on it.
	Journal beadsx.Client
	// AdapterName keys the checkpoint and the shared rate-limit cooldown.
	// Projects that bind one tracker account use one name, so they share
	// one cooldown.
	AdapterName string
	Adapter     Adapter
}

// Options configures a Delivery. A zero field takes its default.
type Options struct {
	// BatchSize is the most journal records one Apply receives.
	BatchSize int
	// CallTimeout bounds one Apply or Reconcile. A call that runs past it
	// has an unknown result, so the checkpoint stays.
	CallTimeout time.Duration
	// DefaultCooldown is the cooldown after a rate limit that names no
	// retry-after.
	DefaultCooldown time.Duration
	// Interval is the tick period of Run when Ticks is nil.
	Interval time.Duration
	// Ticks, when set, drives Run instead of a time.Ticker. Tests use it.
	Ticks <-chan time.Time
	// BatchesPerTick caps the batches one project delivers on one tick.
	BatchesPerTick int
	// Now is the clock of the cooldown. Tests inject a manual clock.
	Now func() time.Time
	// Logger receives the errors of Run.
	Logger *slog.Logger
}

// Report says what one DeliverOnce did.
type Report struct {
	// Delivered is the number of journal records applied.
	Delivered int
	// Checkpoint is the adapter's checkpoint after the step.
	Checkpoint int64
	// Reconciled is true when the step ran Reconcile instead of Apply.
	Reconciled bool
	// More is true when the journal may hold records after Checkpoint.
	More bool
}

// Delivery is the server-owned delivery from the beads events journal to
// each project's tracker adapter (R30, KTD25). Delivery is at least once:
// the checkpoint advances only after Apply succeeds, and an error, a
// timeout or a cancelled call leaves it where it was.
type Delivery struct {
	cp   CheckpointStore
	opts Options

	mu        sync.Mutex
	projects  map[string]*binding
	cooldowns map[string]time.Time // adapter name -> end of its cooldown
}

type binding struct {
	Project
	mu sync.Mutex // one delivery step at a time for the project
}

// New returns a Delivery that keeps its checkpoints in cp.
func New(cp CheckpointStore, opts Options) *Delivery {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = DefaultCallTimeout
	}
	if opts.DefaultCooldown <= 0 {
		opts.DefaultCooldown = DefaultCooldown
	}
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.BatchesPerTick <= 0 {
		opts.BatchesPerTick = DefaultBatchesPerTick
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Delivery{cp: cp, opts: opts, projects: map[string]*binding{}, cooldowns: map[string]time.Time{}}
}

// AddProject registers a project. A project with an adapter needs a
// journal and an adapter name. An id registers once.
func (d *Delivery) AddProject(p Project) error {
	switch {
	case p.ID == "":
		return errors.New("tracker: project has no id")
	case p.Adapter != nil && p.AdapterName == "":
		return fmt.Errorf("tracker: project %s: adapter has no name", p.ID)
	case p.Adapter != nil && p.Journal == nil:
		return fmt.Errorf("tracker: project %s: adapter has no journal", p.ID)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.projects[p.ID]; ok {
		return fmt.Errorf("tracker: project %s is already registered", p.ID)
	}
	d.projects[p.ID] = &binding{Project: p}
	return nil
}

// DeliverOnce runs one delivery step for project. It reads at most one
// batch after the adapter's checkpoint, applies it, and then saves the seq
// of its last record. When the checkpoint fell below the journal's
// retention, it reconciles the current state of every item instead, and it
// saves the journal head that it found before it read that state.
//
// A project with no adapter returns an empty report and reads nothing.
// During the adapter's cooldown, DeliverOnce reads nothing, calls nothing
// and returns ErrCoolingDown.
func (d *Delivery) DeliverOnce(ctx context.Context, project string) (Report, error) {
	d.mu.Lock()
	b, ok := d.projects[project]
	d.mu.Unlock()
	if !ok {
		return Report{}, fmt.Errorf("%w: %s", ErrUnknownProject, project)
	}
	if b.Adapter == nil {
		return Report{}, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := d.coolingDown(b.AdapterName); err != nil {
		return Report{}, err
	}
	cp, err := d.cp.Load(ctx, b.ID, b.AdapterName)
	if err != nil {
		return Report{}, err
	}
	evs, err := b.Journal.Tail(ctx, cp, d.opts.BatchSize)
	if errors.Is(err, beadsx.ErrJournalTruncated) {
		return d.reconcile(ctx, b, cp)
	}
	if err != nil {
		return Report{Checkpoint: cp}, fmt.Errorf("tracker: %s: read journal after %d: %w", b.ID, cp, err)
	}
	if len(evs) == 0 {
		return Report{Checkpoint: cp}, nil
	}
	batch := make([]Event, 0, len(evs))
	prev := cp
	for _, e := range evs {
		if e.Seq <= prev {
			return Report{Checkpoint: cp}, fmt.Errorf("%w: %s: seq %d after %d", ErrJournalOrder, b.ID, e.Seq, prev)
		}
		prev = e.Seq
		batch = append(batch, EventFromJournal(e))
	}

	err = d.call(ctx, b.AdapterName, func(ctx context.Context) error { return b.Adapter.Apply(ctx, b.ID, batch) })
	if err != nil {
		return Report{Checkpoint: cp}, fmt.Errorf("tracker: %s: %s apply of seq %d..%d: %w", b.ID, b.AdapterName, batch[0].Seq, prev, err)
	}
	if err := d.cp.Save(ctx, b.ID, b.AdapterName, prev); err != nil {
		return Report{Checkpoint: cp}, err
	}
	return Report{Delivered: len(batch), Checkpoint: prev, More: len(evs) >= d.opts.BatchSize}, nil
}

// reconcile recovers an adapter whose checkpoint cp fell below the
// journal's retention. It finds the head first and lists the items second,
// so the listed state holds every record up to the head. Records after the
// head are delivered again later, which an idempotent adapter allows.
func (d *Delivery) reconcile(ctx context.Context, b *binding, cp int64) (Report, error) {
	head, err := findHead(ctx, b.Journal, cp)
	if err != nil {
		return Report{Checkpoint: cp}, fmt.Errorf("tracker: %s: find journal head: %w", b.ID, err)
	}
	issues, err := b.Journal.List(ctx, beadsx.Filter{})
	if err != nil {
		return Report{Checkpoint: cp}, fmt.Errorf("tracker: %s: list items: %w", b.ID, err)
	}
	items := make([]Item, 0, len(issues))
	for _, is := range issues {
		items = append(items, ItemFromIssue(is))
	}
	err = d.call(ctx, b.AdapterName, func(ctx context.Context) error { return b.Adapter.Reconcile(ctx, b.ID, items) })
	if err != nil {
		return Report{Checkpoint: cp}, fmt.Errorf("tracker: %s: %s reconcile: %w", b.ID, b.AdapterName, err)
	}
	if err := d.cp.Save(ctx, b.ID, b.AdapterName, head); err != nil {
		return Report{Checkpoint: cp}, err
	}
	return Report{Checkpoint: head, Reconciled: true, More: true}, nil
}

// findHead returns a journal seq h at or above cp such that every record up
// to h exists, and the journal held no record after h when it was read. The
// journal head is the highest seq ever assigned, so a prune never lowers it.
func findHead(ctx context.Context, j beadsx.Client, cp int64) (int64, error) {
	head, err := j.Head(ctx)
	if err != nil {
		return 0, err
	}
	return max(head, cp), nil
}

// coolingDown returns ErrCoolingDown while the adapter's cooldown runs.
func (d *Delivery) coolingDown(adapter string) error {
	d.mu.Lock()
	until := d.cooldowns[adapter]
	d.mu.Unlock()
	if now := d.opts.Now(); now.Before(until) {
		return fmt.Errorf("%w: %s until %s", ErrCoolingDown, adapter, until.UTC().Format(time.RFC3339))
	}
	return nil
}

// call runs one adapter call under the call timeout. A rate limit starts
// the adapter's shared cooldown, or extends it.
func (d *Delivery) call(ctx context.Context, adapter string, fn func(context.Context) error) error {
	cctx, cancel := context.WithTimeout(ctx, d.opts.CallTimeout)
	defer cancel()
	err := fn(cctx)
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrRateLimited) {
		wait := d.opts.DefaultCooldown
		var rl *RateLimitError
		if errors.As(err, &rl) && rl.RetryAfter > 0 {
			wait = rl.RetryAfter
		}
		until := d.opts.Now().Add(wait)
		d.mu.Lock()
		if until.After(d.cooldowns[adapter]) {
			d.cooldowns[adapter] = until
		}
		d.mu.Unlock()
	}
	return err
}

// Run delivers every project that has an adapter on each tick, until ctx
// ends. A tick drains each project's journal, up to BatchesPerTick batches.
// An error stops that project for the tick, and the next tick retries it.
// Run returns the context's error.
func (d *Delivery) Run(ctx context.Context) error {
	ticks := d.opts.Ticks
	if ticks == nil {
		t := time.NewTicker(d.opts.Interval)
		defer t.Stop()
		ticks = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticks:
			d.deliverAll(ctx)
		}
	}
}

func (d *Delivery) deliverAll(ctx context.Context) {
	d.mu.Lock()
	ids := make([]string, 0, len(d.projects))
	for id, b := range d.projects {
		if b.Adapter != nil {
			ids = append(ids, id)
		}
	}
	d.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		for range d.opts.BatchesPerTick {
			if ctx.Err() != nil {
				return
			}
			rep, err := d.DeliverOnce(ctx, id)
			if errors.Is(err, ErrCoolingDown) {
				d.opts.Logger.Debug("tracker delivery waits for a cooldown", "project", id, "err", err)
				break
			}
			if err != nil {
				d.opts.Logger.Warn("tracker delivery failed", "project", id, "err", err)
				break
			}
			if !rep.More {
				break
			}
		}
	}
}
