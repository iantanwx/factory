package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/store"
)

// Applied is the answer of a write that landed.
type Applied struct {
	Project string `json:"project"`
	Item    string `json:"item"`
	Kind    Kind   `json:"kind"`
	Verb    string `json:"verb"`
	From    string `json:"from"`
	Status  string `json:"status"`
	Seq     int64  `json:"seq"`
	Epoch   int64  `json:"epoch"`
}

// Apply runs one intent verb on one item, in the write order of KTD6:
//
//  1. one SQLite transaction checks the lease epoch and the table row, and
//     records the intent with the item's next intent sequence;
//  2. one conditional `bd update` writes the status, the facts, the key,
//     the epoch and the intent sequence, guarded on the status and on the
//     epoch fence;
//  3. SQLite marks the intent applied, records the history row and moves
//     the lease, and stores the answer under the caller's key.
//
// The item's lock is held across all three steps, so an item has at most
// one write in flight, and a retry of an in-flight key joins it.
func (m *Machine) Apply(ctx context.Context, req Request) cli.Result {
	unlock := m.lock(req.Project, req.Item)
	defer unlock()
	return m.apply(ctx, &req)
}

func (m *Machine) apply(ctx context.Context, req *Request) cli.Result {
	bc, err := m.cfg.Beads(req.Project)
	if err != nil {
		return refuse(internalRefusal(err))
	}
	pending, err := m.pendingIntent(ctx, req.Project, req.Item)
	if err != nil {
		return refuse(internalRefusal(err))
	}
	if pending != nil {
		res, settled := m.drive(ctx, bc, *pending, nil)
		if !settled || req.Key != nil && pending.IdemKey == req.Key.Key {
			return res
		}
	}
	if req.Key != nil {
		in, ok, err := m.intentByKey(ctx, req.Key.Key)
		if err != nil {
			return refuse(internalRefusal(err))
		}
		if ok {
			return storedResult(in)
		}
	}

	is, err := bc.Get(ctx, req.Item)
	if err != nil {
		return refuse(beadsRefusal(req.Item, err))
	}
	item, table, err := view(req.Project, is)
	if err != nil {
		return refuse(internalRefusal(err))
	}
	lease, err := m.Lease(ctx, req.Project, req.Item)
	if err != nil {
		return refuse(internalRefusal(err))
	}
	item.Owner = lease.Owner
	epoch := max(item.Epoch, lease.Epoch)

	if req.ExpectedStatus != "" && req.ExpectedStatus != item.Status {
		return refuse(&cli.Refusal{Code: cli.CodeStatusChanged, Rule: "status_changed", Item: item.ID,
			CurrentStatus: item.Status, ExpectedStatus: req.ExpectedStatus, AllowedNext: item.AllowedNext,
			Hint: fmt.Sprintf("%s moved to %s after the caller read %s; read it again", item.ID, item.Status, req.ExpectedStatus)})
	}
	if req.Actor.Fenced && req.Actor.Epoch != epoch {
		return refuse(leaseRefusal(item.ID, item.Status, req.Actor.Epoch, epoch))
	}

	c := &change{ctx: ctx, m: m, req: req, table: table, item: item, set: map[string]string{}, epoch: epoch}
	row := table.match(req.Verb, c)
	if row == nil || req.Command != "" && row.Command != req.Command {
		hint := fmt.Sprintf("a %s in %s has no %s move", item.Kind, item.Status, req.Verb)
		if row != nil {
			hint = fmt.Sprintf("%s does not perform %s; the server does", req.Command, req.Verb)
		}
		return refuse(&cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "no_edge", Item: item.ID,
			CurrentStatus: item.Status, AllowedNext: item.AllowedNext, Hint: hint})
	}
	c.row, c.to = row, row.To
	if c.to == ToSame {
		c.to = item.Status
	}
	if row.guard != nil {
		if r := row.guard(c); r != nil {
			return refuse(r)
		}
	}
	if row.set != nil {
		if r := row.set(c); r != nil {
			return refuse(r)
		}
	}
	if c.to == ToResume {
		return refuse(internalRefusal(fmt.Errorf("machine: %s %s found no resume target", item.Kind, req.Verb)))
	}
	if row.BumpsEpoch {
		c.epoch = epoch + 1
	}
	switch row.Lease {
	case LeaseTake:
		run := c.param("run")
		c.owner = &run
		c.setFact(FactOwner, run)
		c.setFact(FactStage, c.param("stage"))
	case LeaseDrop:
		none := ""
		c.owner = &none
		c.unsetFact(FactOwner, FactStage)
	}
	for _, k := range c.unset {
		delete(c.set, k)
	}
	if r := m.secretCheck(c); r != nil {
		return refuse(r)
	}
	if req.Before != nil {
		if r := req.Before(ctx); r != nil {
			return refuse(r)
		}
	}

	in, err := m.record(ctx, c, epoch, fence(item.Epoch))
	var lr *leaseError
	switch {
	case errors.As(err, &lr):
		return refuse(leaseRefusal(item.ID, item.Status, req.Actor.Epoch, lr.epoch))
	case err != nil:
		return refuse(internalRefusal(err))
	}
	if m.stop(pointRecorded) {
		return refuse(internalRefusal(errStopped))
	}
	res, _ := m.drive(ctx, bc, in, &is)
	return res
}

// secretCheck refuses a write whose facts hold a live secret, before the
// intent is recorded (KTD27).
func (m *Machine) secretCheck(c *change) *cli.Refusal {
	texts := slices.Collect(maps.Values(c.set))
	texts = append(texts, c.param("body"))
	var names []string
	for _, t := range texts {
		if hit, n := m.cfg.Redactor.Find(t); hit {
			names = append(names, n...)
		}
	}
	if len(names) == 0 {
		return nil
	}
	slices.Sort(names)
	return &cli.Refusal{Code: cli.CodeSecretInOutput, Rule: "secret_in_facts", Item: c.item.ID,
		Hint: "the write holds the value of " + strings.Join(slices.Compact(names), ", ") + "; remove it and write again"}
}

// leaseError is a stale epoch found inside the record transaction.
type leaseError struct{ epoch int64 }

func (e *leaseError) Error() string { return fmt.Sprintf("machine: the lease is at epoch %d", e.epoch) }

// record is step 1: one transaction checks the lease epoch again and
// records the intent. A keyed write links its key to the intent, so the
// answer that settles the intent is the answer a retry replays (AE4).
func (m *Machine) record(ctx context.Context, c *change, epoch int64, expectFence string) (store.Intent, error) {
	f := facts{Set: c.set, Unset: c.unset, Epoch: c.epoch, Owner: c.owner, Kind: c.item.Kind,
		Command: c.req.Command, Request: c.req.RequestID, Says: c.row.Says, Fence: expectFence}
	raw, err := json.Marshal(f)
	if err != nil {
		return store.Intent{}, err
	}
	var in store.Intent
	err = m.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		lease, err := readLease(tx.QueryRow, c.item.Project, c.item.ID)
		if err != nil {
			return err
		}
		if c.req.Actor.Fenced && lease.Epoch > c.req.Actor.Epoch {
			return &leaseError{epoch: lease.Epoch}
		}
		key := ""
		if c.req.Key != nil {
			key = c.req.Key.Key
		}
		in, err = tx.RecordIntent(store.Intent{
			Project: c.item.Project, Item: c.item.ID, Kind: string(c.item.Kind), Verb: c.req.Verb, IdemKey: key,
			ExpectedStatus: c.item.Status, TargetStatus: c.to, Facts: raw, Epoch: epoch,
			RunID: c.req.Actor.RunID, Actor: c.req.Actor.Name,
		}, nil, nil)
		if err != nil {
			return err
		}
		if c.req.Key != nil {
			return tx.StoreResult(store.IdemRecord{Key: key, Fingerprint: c.req.Key.Fingerprint, IntentID: in.ID})
		}
		return nil
	})
	return in, err
}

// drive is steps 2 and 3 for one recorded intent. cur is the bead as the
// caller read it, or nil to read it here. settled is false while beads
// cannot decide the intent: it stays pending, and the answer is exit 4.
func (m *Machine) drive(ctx context.Context, bc beadsx.Client, in store.Intent, cur *beadsx.Issue) (cli.Result, bool) {
	var f facts
	if err := json.Unmarshal(in.Facts, &f); err != nil {
		return m.markRefused(ctx, in, f, internalRefusal(fmt.Errorf("machine: intent %d facts: %w", in.ID, err)))
	}
	if cur == nil {
		is, err := bc.Get(ctx, in.Item)
		if errors.Is(err, beadsx.ErrNotFound) {
			return m.markRefused(ctx, in, f, beadsRefusal(in.Item, err))
		}
		if err != nil {
			return refuse(beadsRefusal(in.Item, err)), false
		}
		cur = &is
	}
	if res, done := m.precheck(ctx, in, f, *cur); done {
		return res, true
	}

	set := cloneSet(f.Set)
	set[beadsx.MetaIntentSeq] = strconv.FormatInt(in.Seq, 10)
	set[beadsx.MetaEpoch] = strconv.FormatInt(f.Epoch, 10)
	if in.IdemKey != "" {
		set[beadsx.MetaIdemKey] = in.IdemKey
	}
	spec := beadsx.UpdateSpec{IfStatus: in.ExpectedStatus, IfAssignee: &f.Fence, SetMetadata: set, UnsetMetadata: f.Unset}
	if in.TargetStatus != in.ExpectedStatus {
		spec.Status = in.TargetStatus
	}
	if next := fence(f.Epoch); next != f.Fence {
		spec.Assignee = &next
	}
	_, werr := bc.Update(ctx, in.Item, spec)
	if werr == nil {
		if m.stop(pointWritten) {
			return refuse(internalRefusal(errStopped)), false
		}
		return m.markApplied(ctx, in, f)
	}

	// The write missed or its result is unclear. The bead's intent
	// sequence decides, never the error (KTD6).
	is, rerr := bc.Get(ctx, in.Item)
	if rerr != nil {
		return refuse(beadsRefusal(in.Item, rerr)), false
	}
	if res, done := m.precheck(ctx, in, f, is); done {
		return res, true
	}
	if errors.Is(werr, beadsx.ErrUnavailable) {
		return refuse(beadsRefusal(in.Item, werr)), false
	}
	return m.markRefused(ctx, in, f, internalRefusal(werr))
}

// precheck settles an intent the bead already decides: applied when the
// bead's sequence reached the intent's, LEASE_NOT_HELD when the epoch
// moved, STATUS_CHANGED when the status moved. done is false when the
// write may go ahead.
func (m *Machine) precheck(ctx context.Context, in store.Intent, f facts, is beadsx.Issue) (cli.Result, bool) {
	seq, err := is.IntentSeq()
	if err != nil {
		return refuse(internalRefusal(err)), false
	}
	if seq >= in.Seq {
		return m.markApplied(ctx, in, f)
	}
	be, err := beadEpoch(is)
	if err != nil {
		return refuse(internalRefusal(err)), false
	}
	if be > in.Epoch || is.Assignee != f.Fence {
		if be <= in.Epoch {
			m.divergence(ctx, in.Project, in.Item, fmt.Sprintf("the assignee of %s is %q, and the server wrote %q", in.Item, is.Assignee, f.Fence))
		}
		return m.markRefused(ctx, in, f, leaseRefusal(in.Item, is.Status, in.Epoch, be))
	}
	if is.Status != in.ExpectedStatus {
		m.divergence(ctx, in.Project, in.Item,
			fmt.Sprintf("%s is %s in beads, and the server last wrote %s; nothing in the server made that move", in.Item, is.Status, in.ExpectedStatus))
		t, _ := kindOfType(is.Type)
		var next []string
		if t != nil {
			next = t.AllowedNext(is.Status)
		}
		return m.markRefused(ctx, in, f, &cli.Refusal{Code: cli.CodeStatusChanged, Rule: "status_changed", Item: in.Item,
			CurrentStatus: is.Status, ExpectedStatus: in.ExpectedStatus, AllowedNext: next,
			Hint: fmt.Sprintf("%s moved to %s before this write landed", in.Item, is.Status)})
	}
	return cli.Result{}, false
}

// markApplied is step 3.
func (m *Machine) markApplied(ctx context.Context, in store.Intent, f facts) (cli.Result, bool) {
	res := cli.Found(Applied{Project: in.Project, Item: in.Item, Kind: f.Kind, Verb: in.Verb, From: in.ExpectedStatus,
		Status: in.TargetStatus, Seq: in.Seq, Epoch: f.Epoch})
	env, err := m.seal(f, res)
	if err != nil {
		return refuse(internalRefusal(err)), false
	}
	err = m.cfg.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error {
		if err := tx.MarkApplied(in.ID, env); err != nil {
			return err
		}
		if err := insertHistory(tx, in, f); err != nil {
			return err
		}
		return moveLease(tx, in, f)
	})
	if errors.Is(err, store.ErrIntentSettled) {
		return m.settledResult(ctx, in.ID)
	}
	if err != nil {
		return refuse(internalRefusal(err)), false
	}
	if m.stop(pointMarked) {
		return refuse(internalRefusal(errStopped)), true
	}
	return res, true
}

func (m *Machine) markRefused(ctx context.Context, in store.Intent, f facts, ref *cli.Refusal) (cli.Result, bool) {
	res := refuse(ref)
	env, err := m.seal(f, res)
	if err != nil {
		return refuse(internalRefusal(err)), false
	}
	err = m.cfg.Store.Write(context.WithoutCancel(ctx), func(tx *store.Tx) error { return tx.MarkRefused(in.ID, env) })
	if errors.Is(err, store.ErrIntentSettled) {
		return m.settledResult(ctx, in.ID)
	}
	if err != nil {
		return refuse(internalRefusal(err)), false
	}
	return res, true
}

func (m *Machine) settledResult(ctx context.Context, id int64) (cli.Result, bool) {
	var in store.Intent
	err := m.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		var err error
		in, err = tx.GetIntent(id)
		return err
	})
	if err != nil {
		return refuse(internalRefusal(err)), false
	}
	return storedResult(in), true
}

// seal turns an answer into the envelope stored under the intent and its
// key. A replay restamps the request id and the server version.
func (m *Machine) seal(f facts, res cli.Result) (json.RawMessage, error) {
	verb := f.Command
	env := m.cfg.Registry.Seal(verb, f.Request, res)
	return json.Marshal(env)
}

// storedResult is the answer a settled intent stored.
func storedResult(in store.Intent) cli.Result {
	env, err := cli.EnvelopeFromJSON(in.Result)
	if err != nil {
		return refuse(internalRefusal(fmt.Errorf("machine: intent %d stored no answer: %w", in.ID, err)))
	}
	if env.Refusal != nil {
		return cli.Result{Refusal: env.Refusal, Replayed: true}
	}
	return cli.Result{Exit: env.Exit, Data: env.Data, Replayed: true}
}

func (m *Machine) pendingIntent(ctx context.Context, project, item string) (*store.Intent, error) {
	return m.findIntent(ctx, `SELECT id FROM intents WHERE project = ? AND item = ? AND state = 'pending'`, project, item)
}

func (m *Machine) intentByKey(ctx context.Context, key string) (store.Intent, bool, error) {
	in, err := m.findIntent(ctx, `SELECT id FROM intents WHERE idem_key = ? AND state <> 'pending'`, key)
	if err != nil || in == nil {
		return store.Intent{}, false, err
	}
	return *in, true, nil
}

func (m *Machine) findIntent(ctx context.Context, query string, args ...any) (*store.Intent, error) {
	var out *store.Intent
	err := m.cfg.Store.Write(ctx, func(tx *store.Tx) error {
		var id int64
		err := tx.QueryRow(query, args...).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		in, err := tx.GetIntent(id)
		out = &in
		return err
	})
	return out, err
}

func refuse(r *cli.Refusal) cli.Result { return cli.Refuse(*r) }

func internalRefusal(err error) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeInternal, Rule: "internal", Hint: err.Error()}
}

func leaseRefusal(item, status string, held, current int64) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeLeaseNotHeld, Rule: "stale_epoch", Item: item, CurrentStatus: status,
		Hint: fmt.Sprintf("the caller holds epoch %d of %s, and the lease is at epoch %d", held, item, current)}
}

// beadsRefusal maps a beads error. An unknown item is NOT_FOUND. An
// unreachable or unclear beads answer is exit 4: the caller runs the same
// command again, and its key joins the recorded intent.
func beadsRefusal(item string, err error) *cli.Refusal {
	switch {
	case errors.Is(err, beadsx.ErrNotFound):
		return &cli.Refusal{Code: cli.CodeNotFound, Rule: "no_item", Item: item, Hint: fmt.Sprintf("no work item %s", item)}
	case errors.Is(err, beadsx.ErrUnavailable):
		return &cli.Refusal{Code: cli.CodeBeadsUnavailable, Rule: "beads_unavailable", Item: item, RetryAfterSeconds: 5,
			Hint: "beads did not answer; run the same command again"}
	}
	return internalRefusal(err)
}
