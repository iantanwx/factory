// Package admission decides which item a stage may take (U6): the caps
// from host config's throughput map (KTD30), the retry identity of R10,
// the stage mutexes and the dependency rule. It also classes each run's
// outcome and keeps the failure records that only counted outcomes create
// (KTD8).
//
// Admission reads its snapshot outside every lock: beads, the base branch
// and the dependency reader. It then reserves the run in one SQLite write,
// claims through the machine, and its ClaimGuard revalidates from SQLite
// alone inside the machine's item lock.
package admission

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// HostSource returns the current host config. *config.HostLoader is one.
// Admission calls it at each decision, so a changed cap applies with no
// restart.
type HostSource interface {
	Load(ctx context.Context) (*config.HostConfig, error)
}

// Items is the part of the machine admission uses. *machine.Machine is one.
type Items interface {
	Show(ctx context.Context, project, item string) (machine.Item, *cli.Refusal)
	List(ctx context.Context, project string, f machine.ListFilter) ([]machine.Item, *cli.Refusal)
	Claim(ctx context.Context, c machine.ClaimRequest) (machine.Lease, cli.Result)
}

// Config configures an Admission.
type Config struct {
	Store *store.Store
	Host  HostSource
	// Beads reads the item's title and body for its retry identity.
	Beads machine.BeadsResolver
	// Base reads declared paths on the base branch. A task that declares
	// a path is refused while it is nil.
	Base BaseReader
	// Deps reports open dependencies. Optional.
	Deps DepReader
	// Release is the loop release version. It enters the retry identity.
	Release string
}

// Admission is the admission policy. It implements machine.ClaimGuard.
type Admission struct {
	cfg   Config
	items Items

	mu      sync.Mutex
	pending map[string]snapshot // by run id, between reservation and claim

	draining atomic.Bool // SetDraining; the scheduler's drain sets it (U8)
}

// snapshot is what admission read outside the lock for one claim.
type snapshot struct {
	project, item, stage, inputs string
}

// New returns an Admission. Attach the machine before the first Admit or
// Peek: the machine takes the Admission as its ClaimGuard, so the two are
// built in turn.
func New(cfg Config) (*Admission, error) {
	if cfg.Store == nil || cfg.Host == nil || cfg.Beads == nil {
		return nil, errors.New("admission: an admission needs a store, a host config source and a beads resolver")
	}
	return &Admission{cfg: cfg, pending: map[string]snapshot{}}, nil
}

// Attach sets the machine that admission claims through.
func (a *Admission) Attach(items Items) { a.items = items }

var _ machine.ClaimGuard = (*Admission)(nil)

// Request asks admission to start one run.
type Request struct {
	Project string
	Stage   string
	// Item is the work item the run takes. A stage with no claim row, such
	// as intake or the fixer, takes none.
	Item  string
	RunID string
	// Harness and Model are recorded on the run. Neither enters the retry
	// identity.
	Harness string
	Model   string
}

// Admitted is a run admission accepted. A run with an item holds its lease.
type Admitted struct {
	RunID   string        `json:"run_id"`
	Project string        `json:"project"`
	Stage   string        `json:"stage"`
	Item    string        `json:"item,omitempty"`
	Lease   machine.Lease `json:"lease"`
	// Inputs is the retry identity the attempt is counted against.
	Inputs string `json:"inputs,omitempty"`
	// Attempt is this attempt's number for its inputs, 1 or 2.
	Attempt int    `json:"attempt,omitempty"`
	Release string `json:"release"`
}

// refusalErr carries a refusal out of a store write, which rolls it back.
type refusalErr struct{ r *cli.Refusal }

func (e refusalErr) Error() string { return e.r.Code + ": " + e.r.Hint }

func internal(err error) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeInternal, Rule: "internal", Hint: err.Error()}
}

func asRefusal(err error) *cli.Refusal {
	var re refusalErr
	if errors.As(err, &re) {
		return re.r
	}
	return internal(err)
}

// claimStates returns each kind and state a stage claims from, in table
// order.
func claimStates(stage string) []stateOf {
	var out []stateOf
	for _, t := range machine.Tables() {
		for _, r := range t.Rows {
			if r.Verb != "claim" || r.Stage != stage {
				continue
			}
			for _, f := range r.From {
				out = append(out, stateOf{t.Kind, f})
			}
		}
	}
	return out
}

type stateOf struct {
	kind   machine.Kind
	status string
}

// limitsFor reads the current host config and returns the stage's limits.
func (a *Admission) limitsFor(ctx context.Context, stage string) ([]config.Limit, *cli.Refusal) {
	cfg, err := a.cfg.Host.Load(ctx)
	if err != nil {
		return nil, internal(fmt.Errorf("admission: load host config: %w", err))
	}
	limits := cfg.Limits(stage)
	if len(limits) == 0 {
		return nil, &cli.Refusal{Code: cli.CodeUsage, Rule: "unknown_stage",
			Hint: fmt.Sprintf("%q is no stage with a throughput entry in host config", stage)}
	}
	return limits, nil
}

// task reads the item's title and body from beads.
func (a *Admission) task(ctx context.Context, project, item string) (Task, error) {
	bc, err := a.cfg.Beads(project)
	if err != nil {
		return Task{}, err
	}
	is, err := bc.Get(ctx, item)
	if err != nil {
		return Task{}, err
	}
	return Task{Title: is.Title, Body: is.Description}, nil
}

func (a *Admission) inputsOf(ctx context.Context, project, item string) (string, error) {
	t, err := a.task(ctx, project, item)
	if err != nil {
		return "", fmt.Errorf("admission: read %s: %w", item, err)
	}
	return computeInputs(ctx, a.cfg.Base, project, t, a.cfg.Release)
}

// Admit admits one run: the dependency rule, the stage mutex, the caps and
// the retry limit, in that order. It reserves the run in the runs table,
// then claims the item through the machine. A refused claim drops the
// reservation. A crash between the two leaves a run row with no lease,
// which the scheduler's reconcile settles as not-ready.
func (a *Admission) Admit(ctx context.Context, r Request) (Admitted, *cli.Refusal) {
	if r.Project == "" || r.Stage == "" || r.RunID == "" {
		return Admitted{}, &cli.Refusal{Code: cli.CodeUsage, Rule: "incomplete_request", Hint: "admission needs a project, a stage and a run id"}
	}
	if a.items == nil {
		return Admitted{}, internal(errors.New("admission: no machine attached"))
	}
	if a.draining.Load() {
		return Admitted{}, drainingRefusal()
	}
	limits, ref := a.limitsFor(ctx, r.Stage)
	if ref != nil {
		return Admitted{}, ref
	}
	states := claimStates(r.Stage)
	switch {
	case len(states) > 0 && r.Item == "":
		return Admitted{}, &cli.Refusal{Code: cli.CodeUsage, Rule: "item_required", Hint: r.Stage + " claims an item; name it"}
	case len(states) == 0 && r.Item != "":
		return Admitted{}, &cli.Refusal{Code: cli.CodeUsage, Rule: "no_item_stage", Hint: r.Stage + " claims no item"}
	}

	// The snapshot: every outside read happens here, before any lock.
	snap := snapshot{project: r.Project, item: r.Item, stage: r.Stage}
	var status string
	epics := 0
	if r.Item != "" {
		it, ref := a.items.Show(ctx, r.Project, r.Item)
		if ref != nil {
			return Admitted{}, ref
		}
		if ref, err := dependencyRefusal(ctx, a.cfg.Deps, it); err != nil || ref != nil {
			if err != nil {
				return Admitted{}, internal(err)
			}
			return Admitted{}, ref
		}
		if !slices.Contains(states, stateOf{it.Kind, it.Status}) {
			t, _ := machine.TableFor(it.Kind)
			return Admitted{}, &cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "no_claim", Item: it.ID,
				CurrentStatus: it.Status, AllowedNext: t.AllowedNext(it.Status),
				Hint: fmt.Sprintf("%s does not take a %s in %s", r.Stage, it.Kind, it.Status)}
		}
		if it.Owner != "" {
			return Admitted{}, &cli.Refusal{Code: cli.CodeLeaseHeld, Rule: "lease_held", Item: it.ID, CurrentStatus: it.Status,
				Hint: fmt.Sprintf("%s belongs to run %s until that run settles", it.ID, it.Owner)}
		}
		status = it.Status
		var err error
		if snap.inputs, err = a.inputsOf(ctx, r.Project, r.Item); err != nil {
			return Admitted{}, internal(err)
		}
	} else if r.Stage == stageIntake {
		all, ref := a.items.List(ctx, r.Project, machine.ListFilter{Kind: machine.KindEpic})
		if ref != nil {
			return Admitted{}, ref
		}
		epics = countIntakeEpics(all)
	}

	out := Admitted{RunID: r.RunID, Project: r.Project, Stage: r.Stage, Item: r.Item, Inputs: snap.inputs, Release: a.cfg.Release}
	err := a.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if isMutexStage(r.Stage) {
			holder, err := mutexHolder(tx, r.Project, r.Stage)
			if err != nil {
				return err
			}
			if holder != "" {
				return refusalErr{mutexHeld(r.Project, r.Stage, holder)}
			}
		}
		if r.Stage == stageIntake {
			if l := limits[0]; epics >= l.Cap {
				return refusalErr{&cli.Refusal{Code: cli.CodeCapFull, Rule: "cap_full",
					Hint: fmt.Sprintf("%d epics in counted document states fill the cap of %d in host config throughput.%s", epics, l.Cap, l.Name)}}
			}
		} else if ref, err := capRefusal(tx, r.Stage, limits, 0); err != nil || ref != nil {
			if err != nil {
				return err
			}
			return refusalErr{ref}
		}
		if r.Item != "" {
			n, err := failuresFor(tx, r.Project, r.Item, r.Stage, snap.inputs)
			if err != nil {
				return err
			}
			if n >= FailureLimit {
				return refusalErr{retryLimit(r.Item, status, n)}
			}
			out.Attempt = n + 1
		}
		now := tx.Now().UnixMilli()
		if _, err := tx.Exec(`INSERT INTO runs (run_id, project, stage, item, harness, model, state, started_at, release)
			VALUES (?, ?, ?, ?, ?, ?, 'admitted', ?, ?)`,
			r.RunID, r.Project, r.Stage, r.Item, r.Harness, r.Model, now, a.cfg.Release); err != nil {
			return fmt.Errorf("admission: reserve run %s: %w", r.RunID, err)
		}
		if r.Item != "" {
			if _, err := tx.Exec(`INSERT INTO failures (run_id, project, item, stage, inputs_hash, release_version, class, recorded_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				r.RunID, r.Project, r.Item, r.Stage, snap.inputs, a.cfg.Release, attemptPending, now); err != nil {
				return err
			}
		}
		if isMutexStage(r.Stage) {
			return takeMutex(tx, r.Project, r.Stage, r.RunID)
		}
		return nil
	})
	if err != nil {
		return Admitted{}, asRefusal(err)
	}
	if r.Item == "" {
		return out, nil
	}

	a.mu.Lock()
	a.pending[r.RunID] = snap
	a.mu.Unlock()
	lease, res := a.items.Claim(ctx, machine.ClaimRequest{
		Project: r.Project, Item: r.Item, Stage: r.Stage, RunID: r.RunID, ExpectedStatus: status,
	})
	a.mu.Lock()
	delete(a.pending, r.RunID)
	a.mu.Unlock()
	if res.Refusal != nil {
		if err := a.unreserve(ctx, r.RunID); err != nil {
			return Admitted{}, internal(errors.Join(fmt.Errorf("admission: %s: %s", res.Refusal.Code, res.Refusal.Hint), err))
		}
		return Admitted{}, res.Refusal
	}
	out.Lease = lease
	return out, nil
}

func retryLimit(item, status string, n int) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeRetryLimit, Rule: "retry_limit", Item: item, CurrentStatus: status,
		Hint: fmt.Sprintf("%s: %d failed attempts for current inputs; edit the item, change a declared file or cited document on the base, or install a new release", item, n)}
}

// unreserve drops a reservation whose claim the machine refused.
func (a *Admission) unreserve(ctx context.Context, run string) error {
	return a.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(`DELETE FROM failures WHERE run_id = ? AND class = ?`, run, attemptPending); err != nil {
			return err
		}
		if err := releaseMutexes(tx, run); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM runs WHERE run_id = ? AND state = 'admitted'`, run)
		return err
	})
}

// AdmitClaim is the machine's ClaimGuard. The machine calls it inside the
// item's lock, so it makes no outside read: it revalidates the snapshot
// Admit took against SQLite alone. A claim that did not come through Admit
// is refused.
func (a *Admission) AdmitClaim(ctx context.Context, c machine.ClaimCheck) *cli.Refusal {
	a.mu.Lock()
	snap, ok := a.pending[c.RunID]
	a.mu.Unlock()
	if !ok || snap.project != c.Project || snap.item != c.Item || snap.stage != c.Stage {
		return &cli.Refusal{Code: cli.CodeInternal, Rule: "not_admitted", Item: c.Item, CurrentStatus: c.Status,
			Hint: fmt.Sprintf("run %s has no admission for %s at stage %s; a claim goes through admission", c.RunID, c.Item, c.Stage)}
	}
	var ref *cli.Refusal
	err := a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		q := readTx{ctx, tx}
		var live int
		if err := q.QueryRow(`SELECT count(*) FROM runs WHERE run_id = ? AND project = ? AND item = ? AND stage = ? AND ended_at IS NULL`,
			c.RunID, c.Project, c.Item, c.Stage).Scan(&live); err != nil {
			return err
		}
		if live == 0 {
			ref = &cli.Refusal{Code: cli.CodeInternal, Rule: "not_admitted", Item: c.Item, CurrentStatus: c.Status,
				Hint: fmt.Sprintf("run %s holds no reservation", c.RunID)}
			return nil
		}
		n, err := failuresFor(q, c.Project, c.Item, c.Stage, snap.inputs)
		if err != nil {
			return err
		}
		if n >= FailureLimit {
			ref = retryLimit(c.Item, c.Status, n)
		}
		return nil
	})
	if err != nil {
		return internal(err)
	}
	return ref
}

// Candidate is the item a stage's work query would admit now.
type Candidate struct {
	Project string       `json:"project"`
	Stage   string       `json:"stage"`
	Item    string       `json:"item,omitempty"`
	Kind    machine.Kind `json:"kind,omitempty"`
	Status  string       `json:"status,omitempty"`
}

// Peek runs the stage's work query (R20): the same policy as Admit, with
// no claim and no write. It returns nil when the stage would admit
// nothing now. A stage that claims no item returns a candidate with no
// item when it may start.
func (a *Admission) Peek(ctx context.Context, project, stage string) (*Candidate, *cli.Refusal) {
	if a.items == nil {
		return nil, internal(errors.New("admission: no machine attached"))
	}
	if a.draining.Load() {
		return nil, nil
	}
	limits, ref := a.limitsFor(ctx, stage)
	if ref != nil {
		return nil, ref
	}
	if stage == stageIntake {
		all, ref := a.items.List(ctx, project, machine.ListFilter{Kind: machine.KindEpic})
		if ref != nil {
			return nil, ref
		}
		if countIntakeEpics(all) >= limits[0].Cap {
			return nil, nil
		}
	}
	full := false
	err := a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		q := readTx{ctx, tx}
		if isMutexStage(stage) {
			holder, err := mutexHolder(q, project, stage)
			if err != nil || holder != "" {
				full = holder != ""
				return err
			}
		}
		if stage == stageIntake {
			return nil
		}
		ref, err := capRefusal(q, stage, limits, 0)
		full = ref != nil
		return err
	})
	if err != nil {
		return nil, internal(err)
	}
	if full {
		return nil, nil
	}
	states := claimStates(stage)
	if len(states) == 0 {
		return &Candidate{Project: project, Stage: stage}, nil
	}
	for _, s := range states {
		items, ref := a.items.List(ctx, project, machine.ListFilter{Kind: s.kind, Status: s.status})
		if ref != nil {
			return nil, ref
		}
		for _, it := range items {
			ok, err := a.admissible(ctx, stage, it)
			if err != nil {
				return nil, internal(err)
			}
			if ok {
				return &Candidate{Project: project, Stage: stage, Item: it.ID, Kind: it.Kind, Status: it.Status}, nil
			}
		}
	}
	return nil, nil
}

// admissible reports whether Admit would take the item now, apart from the
// caps that Peek checked once.
func (a *Admission) admissible(ctx context.Context, stage string, it machine.Item) (bool, error) {
	if it.Fact(machine.FactOwner) != "" {
		return false, nil
	}
	if ref, err := dependencyRefusal(ctx, a.cfg.Deps, it); err != nil || ref != nil {
		return false, err
	}
	var held bool
	if err := a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		held, err = mayBeHeld(readTx{ctx, tx}, it.Project, it.ID, stage)
		return err
	}); err != nil || !held {
		return err == nil, err
	}
	inputs, err := a.inputsOf(ctx, it.Project, it.ID)
	if err != nil {
		return false, err
	}
	var n int
	err = a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		n, err = failuresFor(readTx{ctx, tx}, it.Project, it.ID, stage, inputs)
		return err
	})
	return err == nil && n < FailureLimit, err
}
