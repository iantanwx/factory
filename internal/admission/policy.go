// Package admission decides which item a stage may take: the caps
// from host config's throughput map and the project's own caps over it
// (KTD30), the retry identity of R10,
// the stage mutexes and the dependency rule. It also classes each run's
// outcome and keeps the failure records that only counted outcomes create
// (KTD8).
//
// Admission reads its snapshot outside every lock: beads, the base branch,
// the project's config and the dependency reader. It then reserves the run in one SQLite write,
// claims through the machine, and its ClaimGuard revalidates from SQLite
// alone inside the machine's item lock.
package admission

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

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
	// Pulls lists open pull requests for the `## Depends on` hold.
	// Optional.
	Pulls PullReader
	// Project reads a project's committed config. Its stage and pool caps
	// replace host config's for that project. Nil, or a project with no
	// config yet, takes the host caps.
	Project func(ctx context.Context, project string) (*config.ProjectSnapshot, error)
	// Release is the loop release version. It enters the retry identity.
	Release string
	// Now is the clock of the implementer's rank. time.Now when nil.
	Now func() time.Time
}

// CIState is the CI state of one head. The zero value is CIPending, so an
// unknown state never reads as green.
type CIState int

// CI states of one head.
const (
	CIPending CIState = iota // a check runs or waits, or no check has reported
	CIGreen                  // every required check passed
	CIRed                    // a check failed
)

// CIReader reads the CI state of a head of a project. The server wires it
// to github.Service.Checks.
type CIReader func(ctx context.Context, project, head string) (CIState, error)

// Admission is the admission policy. It implements machine.ClaimGuard.
type Admission struct {
	cfg   Config
	items Items
	heads machine.PRHeadReader
	ci    CIReader

	mu      sync.Mutex
	pending map[string]snapshot // by run id, between reservation and claim

	draining atomic.Bool // SetDraining; the scheduler's drain sets it
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

// SetPRHeadReader sets the reader of live pull request heads. The GitHub
// setup sets it before the socket binds.
func (a *Admission) SetPRHeadReader(f machine.PRHeadReader) { a.heads = f }

// SetCIReader sets the reader of CI at a pull request head. The GitHub
// setup sets it before the socket binds.
func (a *Admission) SetCIReader(f CIReader) { a.ci = f }

var _ machine.ClaimGuard = (*Admission)(nil)

// Request asks admission to start one run.
type Request struct {
	Project string
	Stage   string
	// Item is the work item the run takes. A stage with no claim row, such
	// as brainstorm or triage, takes none.
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

func asRefusal(err error) *cli.Refusal {
	var re refusalErr
	if errors.As(err, &re) {
		return re.r
	}
	return cli.InternalRefusal(err)
}

// claimStates returns each kind and state a stage claims from, in machine
// table order. The implementer's work query ranks its tickets by
// rankImplementer, so no state of a stage goes first.
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

// ClaimState is one kind and status that a stage claims from.
type ClaimState struct {
	Kind   machine.Kind
	Status string
}

// ClaimStates returns each kind and status that stage claims from, in
// claim order. It is the view of the claim states for a reader outside
// admission, such as overwatch's stalls part, so that reader lists no state
// of its own. A stage that claims no item returns none.
func ClaimStates(stage string) []ClaimState {
	var out []ClaimState
	for _, s := range claimStates(stage) {
		out = append(out, ClaimState{Kind: s.kind, Status: s.status})
	}
	return out
}

// limitsFor reads the current host config and the project's config, and
// returns the stage's limits in force for the project.
func (a *Admission) limitsFor(ctx context.Context, project, stage string) ([]config.Limit, *cli.Refusal) {
	cfg, err := a.cfg.Host.Load(ctx)
	if err != nil {
		return nil, cli.InternalRefusal(fmt.Errorf("admission: load host config: %w", err))
	}
	var pc *config.ProjectConfig
	if a.cfg.Project != nil {
		snap, err := a.cfg.Project(ctx, project)
		switch {
		case err == nil:
			pc = snap.Config
		case !errors.Is(err, config.ErrConfigMissing):
			return nil, cli.InternalRefusal(fmt.Errorf("admission: load the config of %s: %w", project, err))
		}
	}
	limits := config.Effective(cfg, pc).Limits(project, stage)
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
	// TaskText holds work edit's text and an answer that amends the task,
	// so either re-admits a twice-failed item (R10).
	title, body := machine.TaskText(is)
	return Task{Title: title, Body: body}, nil
}

// inputsOf returns the item's retry identity at stage. A code review and a
// QA run read the live head of the item's pull request, so a new head is
// new input. That is an outside read, and no caller holds a lock. A head
// that does not read refuses, as a base that does not read does.
func (a *Admission) inputsOf(ctx context.Context, project, item, stage string) (string, error) {
	t, err := a.task(ctx, project, item)
	if err != nil {
		return "", fmt.Errorf("admission: read %s: %w", item, err)
	}
	if (stage == config.StageCodeReviewer || stage == config.StageQA) && a.heads != nil {
		head, ref := a.heads(ctx, project, item)
		switch {
		case ref == nil:
			t.Head = head
		case ref.Code != cli.CodeNotFound:
			return "", refusalErr{ref}
		}
	}
	return computeInputs(ctx, a.cfg.Base, project, t, a.cfg.Release)
}

// ciWait refuses a code review of a ticket whose live head differs from a
// set simplify_from until CI at that head is green. The simplify run made
// that head, and only CI runs every gate there. A red head waits too: the
// reactor's pollCI sends it to repair. The head at simplify_from, the
// implementer's own, never waits here. It makes outside reads, so no
// caller holds a lock.
func (a *Admission) ciWait(ctx context.Context, stage string, it machine.Item) (*cli.Refusal, error) {
	from := it.Fact(machine.FactSimplifyFrom)
	if stage != config.StageCodeReviewer || from == "" || a.heads == nil {
		return nil, nil
	}
	head, ref := a.heads(ctx, it.Project, it.ID)
	switch {
	case ref != nil && ref.Code == cli.CodeNotFound:
		return nil, nil
	case ref != nil:
		return nil, refusalErr{ref}
	case head == from:
		return nil, nil
	}
	state := CIPending
	if a.ci != nil {
		var err error
		if state, err = a.ci(ctx, it.Project, head); err != nil {
			return nil, fmt.Errorf("admission: read CI at %s of %s: %w", head, it.ID, err)
		}
	}
	if state == CIGreen {
		return nil, nil
	}
	return &cli.Refusal{Code: cli.CodeCINotGreen, Rule: "simplify_head_ci", Item: it.ID, CurrentStatus: it.Status,
		Hint: fmt.Sprintf("%s waits for green CI at %s, the head its simplify run left, before its code review", it.ID, head)}, nil
}

// Admit admits one run: the stage pause, the dependency rule, the stage
// mutex, the holds, the caps and the retry limit, in that order. It
// reserves the run in the runs table, then claims the item through the
// machine. A refused claim drops the reservation. A crash between the two leaves a run row with no lease,
// which the scheduler's reconcile settles as not-ready.
func (a *Admission) Admit(ctx context.Context, r Request) (Admitted, *cli.Refusal) {
	if r.Project == "" || r.Stage == "" || r.RunID == "" {
		return Admitted{}, &cli.Refusal{Code: cli.CodeUsage, Rule: "incomplete_request", Hint: "admission needs a project, a stage and a run id"}
	}
	if a.items == nil {
		return Admitted{}, cli.InternalRefusal(errors.New("admission: no machine attached"))
	}
	if a.draining.Load() {
		return Admitted{}, drainingRefusal()
	}
	if paused, err := a.Paused(ctx, r.Project, r.Stage); err != nil || paused {
		if err != nil {
			return Admitted{}, cli.InternalRefusal(err)
		}
		return Admitted{}, pausedRefusal(r.Project, r.Stage)
	}
	limits, ref := a.limitsFor(ctx, r.Project, r.Stage)
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
		if ref, err := a.dependencyRefusal(ctx, it); err != nil || ref != nil {
			if err != nil {
				return Admitted{}, cli.InternalRefusal(err)
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
		if ref, err := a.ciWait(ctx, r.Stage, it); err != nil || ref != nil {
			if err != nil {
				return Admitted{}, asRefusal(err)
			}
			return Admitted{}, ref
		}
		status = it.Status
		var err error
		if snap.inputs, err = a.inputsOf(ctx, r.Project, r.Item, r.Stage); err != nil {
			return Admitted{}, asRefusal(err)
		}
	} else if r.Stage == config.StageBrainstorm {
		if epics, ref = a.brainstormEpics(ctx, r.Project); ref != nil {
			return Admitted{}, ref
		}
	}

	out := Admitted{RunID: r.RunID, Project: r.Project, Stage: r.Stage, Item: r.Item, Inputs: snap.inputs, Release: a.cfg.Release}
	err := a.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if MutexStage(r.Stage) {
			holder, err := mutexHolder(tx, r.Project, r.Stage)
			if err != nil {
				return err
			}
			if holder != "" {
				return refusalErr{mutexHeld(r.Project, r.Stage, holder)}
			}
		}
		if r.Item != "" {
			ref, err := holdRefusal(tx, r.Project, r.Item)
			if err == nil && ref == nil {
				ref, err = blockedRefusal(tx, r.Project, r.Item, r.Stage, snap.inputs)
			}
			if err != nil {
				return err
			}
			if ref != nil {
				return refusalErr{ref}
			}
		}
		if r.Stage == config.StageBrainstorm {
			if ref := brainstormFull(epics, limits[0]); ref != nil {
				return refusalErr{ref}
			}
		} else if ref, err := capRefusal(tx, r.Stage, limits, ""); err != nil || ref != nil {
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
		if MutexStage(r.Stage) {
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
		if err := a.unreserve(ctx, r.Project, r.Item, r.RunID); err != nil {
			return Admitted{}, cli.InternalRefusal(errors.Join(fmt.Errorf("admission: %s: %s", res.Refusal.Code, res.Refusal.Hint), err))
		}
		return Admitted{}, res.Refusal
	}
	out.Lease = lease
	return out, nil
}

func retryLimit(item, status string, n int) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeRetryLimit, Rule: "retry_limit", Item: item, CurrentStatus: status,
		Hint: fmt.Sprintf("%s: %d failed attempts for current inputs; edit the item, change a declared file or cited document on the base, push to the pull request of a code review or a QA run, or install a new release", item, n)}
}

// unreserve drops a reservation whose claim the machine refused. A claim
// whose intent stayed recorded, such as one that met a beads outage, may
// still land and give the lease to this run. That run's row is kept and
// settled not-ready instead, so the lease sweep frees the lease once the
// claim lands.
func (a *Admission) unreserve(ctx context.Context, project, item, run string) error {
	return a.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		if _, err := tx.Exec(`DELETE FROM failures WHERE run_id = ? AND class = ?`, run, attemptPending); err != nil {
			return err
		}
		if err := releaseMutexes(tx, run); err != nil {
			return err
		}
		var recorded int
		if err := tx.QueryRow(`SELECT count(*) FROM intents WHERE project = ? AND item = ? AND run_id = ? AND state <> 'refused'`,
			project, item, run).Scan(&recorded); err != nil {
			return err
		}
		if recorded > 0 {
			_, err := tx.Exec(`UPDATE runs SET state = 'settled', outcome_class = ?, ended_at = ? WHERE run_id = ? AND ended_at IS NULL`,
				string(ClassNotReady), tx.Now().UnixMilli(), run)
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
		if held, err := holdRefusal(q, c.Project, c.Item); err != nil || held != nil {
			ref = held
			return err
		}
		if held, err := blockedRefusal(q, c.Project, c.Item, c.Stage, snap.inputs); err != nil || held != nil {
			ref = held
			return err
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
		return cli.InternalRefusal(err)
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
	// ModelTier is the item's model_tier fact: escalated after two
	// code-review rejects.
	ModelTier string `json:"model_tier,omitempty"`
}

// Peek runs the stage's work query (R20): the same policy as Admit, with
// no claim and no write. It returns nil when the stage would admit
// nothing now. A stage that claims no item returns a candidate with no
// item when it may start.
func (a *Admission) Peek(ctx context.Context, project, stage string) (*Candidate, *cli.Refusal) {
	return a.PeekAs(ctx, project, stage, "")
}

// PeekAs is Peek asked from inside a run. The caps leave that run out of
// their count, so a run that fills its stage's cap still finds the work it
// was started for, such as the triage stage's waiting escalations. Every
// other live run still counts.
//
// The query lists the items of every claim state of the stage, orders
// them, and returns the first admissible item. An item that admission would
// refuse never stops it from taking the next. The implementer orders its
// tickets by rankImplementer (D10), so a ticket in repair comes before a
// new ticket. Every other stage queues its states as one: it takes the item
// that entered its state first, whichever state that is, so the
// doc-reviewer takes a spec review that waited longer than a PRD review
// (parity 2.15). It never takes the item whose bead id sorts first (F-20).
// waitOrder states the order, and how it breaks a tie.
func (a *Admission) PeekAs(ctx context.Context, project, stage, run string) (*Candidate, *cli.Refusal) {
	if a.items == nil {
		return nil, cli.InternalRefusal(errors.New("admission: no machine attached"))
	}
	if a.draining.Load() {
		return nil, nil
	}
	if paused, err := a.Paused(ctx, project, stage); err != nil || paused {
		if err != nil {
			return nil, cli.InternalRefusal(err)
		}
		return nil, nil
	}
	limits, ref := a.limitsFor(ctx, project, stage)
	if ref != nil {
		return nil, ref
	}
	if stage == config.StageBrainstorm {
		epics, ref := a.brainstormEpics(ctx, project)
		if ref != nil {
			return nil, ref
		}
		if brainstormFull(epics, limits[0]) != nil {
			return nil, nil
		}
	}
	full := false
	err := a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		q := readTx{ctx, tx}
		if MutexStage(stage) {
			holder, err := mutexHolder(q, project, stage)
			if err != nil || holder != "" {
				full = holder != ""
				return err
			}
		}
		if stage == config.StageBrainstorm {
			return nil
		}
		ref, err := capRefusal(q, stage, limits, run)
		full = ref != nil
		has, ok := waiting[stage]
		if err != nil || full || !ok {
			return err
		}
		work, err := has(q, project)
		full = !work
		return err
	})
	if err != nil {
		return nil, cli.InternalRefusal(err)
	}
	if full {
		return nil, nil
	}
	states := claimStates(stage)
	if len(states) == 0 {
		return &Candidate{Project: project, Stage: stage}, nil
	}
	var items []machine.Item
	for _, st := range states {
		got, ref := a.items.List(ctx, project, machine.ListFilter{Kind: st.kind, Status: st.status})
		if ref != nil {
			return nil, ref
		}
		items = append(items, got...)
	}
	if stage == config.StageImplementer {
		if items, err = a.rankImplementer(ctx, project, items, a.now()); err != nil {
			return nil, asRefusal(err)
		}
	} else {
		entered, err := a.enteredAt(ctx, project, items)
		if err != nil {
			return nil, cli.InternalRefusal(err)
		}
		waitOrder(items, entered)
	}
	for _, it := range items {
		ok, err := a.admissible(ctx, stage, it)
		if err != nil {
			return nil, asRefusal(err)
		}
		if ok {
			return &Candidate{Project: project, Stage: stage, Item: it.ID, Kind: it.Kind, Status: it.Status,
				ModelTier: it.Fact(machine.FactModelTier)}, nil
		}
	}
	return nil, nil
}

// enteredAt returns, for each item whose history records its entry into
// its current status, the time of that entry in Unix milliseconds. The entry
// is the item's last transition that changed its status. When that
// transition led elsewhere, something outside the machine moved the item,
// and the history does not date its status.
func (a *Admission) enteredAt(ctx context.Context, project string, items []machine.Item) (map[string]int64, error) {
	out := map[string]int64{}
	if len(items) < 2 {
		return out, nil
	}
	status := make(map[string]string, len(items))
	ids := make([]string, 0, len(items))
	for _, it := range items {
		status[it.ID] = it.Status
		ids = append(ids, it.ID)
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	err = a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT t.item, t.to_status, t.at FROM transitions t
			JOIN json_each(?) j ON j.value = t.item
			WHERE t.project = ? AND t.from_status <> t.to_status
			AND t.seq = (SELECT max(u.seq) FROM transitions u WHERE u.project = t.project AND u.item = t.item
				AND u.from_status <> u.to_status)`, string(raw), project)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var (
				item, to string
				at       int64
			)
			if err := rows.Scan(&item, &to, &at); err != nil {
				return err
			}
			if status[item] == to {
				out[item] = at
			}
		}
		return rows.Err()
	})
	return out, err
}

// waitOrder sorts the items of one claim tier so the item that has waited
// in its state longest comes first. An item entered its state at the time entered holds
// for it, or else at its last bead write. Two items that entered in the same
// millisecond keep the order of their last bead writes, and then of their
// bead ids, so the order is stable.
func waitOrder(items []machine.Item, entered map[string]int64) {
	since := func(it machine.Item) int64 {
		if at, ok := entered[it.ID]; ok {
			return at
		}
		return it.Updated.UnixMilli()
	}
	slices.SortStableFunc(items, func(x, y machine.Item) int {
		return cmp.Or(cmp.Compare(since(x), since(y)), x.Updated.Compare(y.Updated), cmp.Compare(x.ID, y.ID))
	})
}

// admissible reports whether Admit would take the item now, apart from the
// caps that Peek checked once.
func (a *Admission) admissible(ctx context.Context, stage string, it machine.Item) (bool, error) {
	if it.Fact(machine.FactOwner) != "" {
		return false, nil
	}
	if ref, err := a.dependencyRefusal(ctx, it); err != nil || ref != nil {
		return false, err
	}
	if ref, err := a.ciWait(ctx, stage, it); err != nil || ref != nil {
		return false, err
	}
	var held, blocked, holds bool
	if err := a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		ref, err := holdRefusal(readTx{ctx, tx}, it.Project, it.ID)
		if err != nil || ref != nil {
			holds = ref != nil
			return err
		}
		if held, err = mayBeHeld(readTx{ctx, tx}, it.Project, it.ID, stage); err != nil {
			return err
		}
		blocked, err = mayBeBlocked(readTx{ctx, tx}, it.Project, it.ID, stage)
		return err
	}); err != nil || holds || !held && !blocked {
		return err == nil && !holds, err
	}
	inputs, err := a.inputsOf(ctx, it.Project, it.ID, stage)
	if err != nil {
		return false, err
	}
	var n int
	var ref *cli.Refusal
	err = a.cfg.Store.Read(ctx, func(tx *sql.Tx) error {
		var err error
		if n, err = failuresFor(readTx{ctx, tx}, it.Project, it.ID, stage, inputs); err != nil {
			return err
		}
		ref, err = blockedRefusal(readTx{ctx, tx}, it.Project, it.ID, stage, inputs)
		return err
	})
	return err == nil && n < FailureLimit && ref == nil, err
}

// now reads the clock of the implementer's rank.
func (a *Admission) now() time.Time {
	if a.cfg.Now != nil {
		return a.cfg.Now()
	}
	return time.Now()
}

// Takes reports whether the stage claims an item of its kind in its status.
func Takes(stage string, it machine.Item) bool {
	return slices.Contains(claimStates(stage), stateOf{it.Kind, it.Status})
}
