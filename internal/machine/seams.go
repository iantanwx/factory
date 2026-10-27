package machine

import (
	"context"
	"fmt"
	"slices"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
)

// Show reads one item: beads holds its status and facts, SQLite its lease.
func (m *Machine) Show(ctx context.Context, project, item string) (Item, *cli.Refusal) {
	it, ref := m.Read(ctx, project, item)
	if ref != nil {
		return Item{}, ref
	}
	l, err := m.Lease(ctx, project, item)
	if err != nil {
		return Item{}, cli.InternalRefusal(err)
	}
	it.Owner, it.Epoch = l.Owner, max(it.Epoch, l.Epoch)
	return it, nil
}

// Read reads one item from beads alone: its status and facts, and no
// lease. It makes no SQLite read, so a caller inside a store transaction
// may use it.
func (m *Machine) Read(ctx context.Context, project, item string) (Item, *cli.Refusal) {
	bc, err := m.cfg.Beads(project)
	if err != nil {
		return Item{}, cli.InternalRefusal(err)
	}
	is, err := bc.Get(ctx, item)
	if err != nil {
		return Item{}, beadsRefusal(item, err)
	}
	it, _, err := view(project, is)
	if err != nil {
		return Item{}, &cli.Refusal{Code: cli.CodeNotFound, Rule: "not_a_work_item", Item: item, Hint: err.Error()}
	}
	return it, nil
}

// Body reads the body that work edit replaces: the body an edit wrote, or
// the bead's description. It leaves out the amendment an answer appended,
// which TaskText adds.
func (m *Machine) Body(ctx context.Context, project, item string) (string, *cli.Refusal) {
	bc, err := m.cfg.Beads(project)
	if err != nil {
		return "", cli.InternalRefusal(err)
	}
	is, err := bc.Get(ctx, item)
	if err != nil {
		return "", beadsRefusal(item, err)
	}
	if b := is.Metadata[factPrefix+FactBody]; b != "" {
		return b, nil
	}
	return is.Description, nil
}

// ListFilter selects items. Empty fields select everything.
type ListFilter struct {
	Kind   Kind
	Status string
	// Facts selects items whose facts hold every value, for example the
	// create key work entry wrote.
	Facts map[string]string
}

// List reads the work items of one project in id order, with no body. A
// bead of a type no kind uses is skipped.
func (m *Machine) List(ctx context.Context, project string, f ListFilter) ([]Item, *cli.Refusal) {
	bc, err := m.cfg.Beads(project)
	if err != nil {
		return nil, cli.InternalRefusal(err)
	}
	bf := beadsx.Filter{}
	for k, v := range f.Facts {
		if bf.Metadata == nil {
			bf.Metadata = map[string]string{}
		}
		bf.Metadata[factPrefix+k] = v
	}
	if f.Status != "" {
		bf.Statuses = []string{f.Status}
	}
	if f.Kind != "" {
		t, ok := TableFor(f.Kind)
		if !ok {
			return nil, &cli.Refusal{Code: cli.CodeUsage, Rule: "unknown_kind", Hint: fmt.Sprintf("no kind %q", f.Kind)}
		}
		bf.Type = t.BeadType
	}
	issues, err := bc.List(ctx, bf)
	if err != nil {
		return nil, beadsRefusal("", err)
	}
	out := []Item{}
	for _, is := range issues {
		it, _, err := view(project, is)
		if err != nil {
			continue
		}
		it.Body = ""
		out = append(out, it)
	}
	return out, nil
}

// CreateRequest makes one item and applies its create verb.
type CreateRequest struct {
	Project     string
	Kind        Kind
	Title       string
	Description string
	// Verb is the table's create row, for example "create", "file",
	// "prd_submit" or "create_architecture".
	Verb   string
	Params map[string]string
	Actor  Actor
	// Facts are written with the new bead, for example "epic" on a child.
	Facts map[string]string
}

// Create makes the bead, then applies the create verb from Initial. The
// bead and the first transition are two writes: a crash between them
// leaves a bead in Initial, which no table row but create moves. Work entry
// makes creation idempotent by the epic and child key: it calls
// CreateBead with a create key, and Apply with the create verb.
func (m *Machine) Create(ctx context.Context, cr CreateRequest) (string, cli.Result) {
	t, ok := TableFor(cr.Kind)
	if !ok {
		return "", cli.Reject(cli.CodeUsage, "unknown_kind", fmt.Sprintf("no kind %q", cr.Kind))
	}
	if !slices.ContainsFunc(t.Rows, func(r Row) bool { return r.Verb == cr.Verb && slices.Contains(r.From, Initial) }) {
		return "", cli.Refuse(cli.Refusal{Code: cli.CodeTransitionNotAllowed, Rule: "no_edge", CurrentStatus: Initial,
			AllowedNext: t.AllowedNext(Initial), Hint: fmt.Sprintf("a new %s has no %s move", cr.Kind, cr.Verb)})
	}
	id, ref := m.CreateBead(ctx, cr)
	if ref != nil {
		return "", ref.Result()
	}
	return id, m.Apply(ctx, Request{Project: cr.Project, Item: id, Verb: cr.Verb, Params: cr.Params, Actor: cr.Actor})
}

// CreateBead makes the bead of a new item in Initial, with its facts, and
// applies no verb. Verb and Params are unused.
func (m *Machine) CreateBead(ctx context.Context, cr CreateRequest) (string, *cli.Refusal) {
	t, ok := TableFor(cr.Kind)
	if !ok {
		return "", &cli.Refusal{Code: cli.CodeUsage, Rule: "unknown_kind", Hint: fmt.Sprintf("no kind %q", cr.Kind)}
	}
	md := map[string]string{}
	for k, v := range cr.Facts {
		md[factPrefix+k] = v
	}
	if r := m.cfg.Redactor.Refusal("secret_in_facts", "new item", append([]string{cr.Title, cr.Description}, valuesOf(md)...)...); r != nil {
		return "", r
	}
	bc, err := m.cfg.Beads(cr.Project)
	if err != nil {
		return "", cli.InternalRefusal(err)
	}
	is, err := bc.Create(ctx, beadsx.CreateSpec{Title: cr.Title, Description: cr.Description, Type: t.BeadType, Priority: 2, Metadata: md})
	if err != nil {
		return "", beadsRefusal("", err)
	}
	return is.ID, nil
}

func valuesOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// ClaimRequest is a lease grant at dispatch (KTD7).
type ClaimRequest struct {
	Project string
	Item    string
	Stage   string
	RunID   string
	// ExpectedStatus is the status admission read. Empty means current.
	ExpectedStatus string
}

// Claim grants the item's lease to a run: the table's claim row for the
// stage, the ClaimGuard, and an epoch bump. The scheduler issues
// the run's token with the returned epoch.
func (m *Machine) Claim(ctx context.Context, c ClaimRequest) (Lease, cli.Result) {
	res := m.Apply(ctx, Request{
		Project: c.Project, Item: c.Item, Verb: "claim", ExpectedStatus: c.ExpectedStatus,
		Params: map[string]string{"stage": c.Stage, "run": c.RunID},
		Actor:  Actor{Name: "server:dispatch", RunID: c.RunID},
	})
	if res.Refusal != nil {
		return Lease{}, res
	}
	l, err := m.Lease(ctx, c.Project, c.Item)
	if err != nil {
		return Lease{}, cli.Internal(err)
	}
	return l, res
}

// MergeEvent is a merge the GitHub reactor observed.
type MergeEvent struct {
	Project string
	Item    string
	Base    string // the project's base branch
	Into    string // the branch the PR merged into
	SHA     string
	// SpecPath is the merged spec's path. It is "" for a PRD, a ticket,
	// or a spec PR with no single spec file.
	SpecPath string
}

// mergeRetries bounds the re-reads after a status miss. Under the item's
// lock only a writer outside the server can move the item meanwhile.
const mergeRetries = 5

// MergeObserved moves the item for a merge into the base (KTD16): a ticket
// moves to done from any state that is not terminal, and the write bumps
// the epoch, so the old run's next write gets LEASE_NOT_HELD. An epic's
// document merge moves it one state. A merge into another branch changes
// nothing, and an item that is already terminal, or an epic with no
// document row from its state, answers Nothing.
func (m *Machine) MergeObserved(ctx context.Context, ev MergeEvent) cli.Result {
	if ev.Into != ev.Base {
		return cli.Nothing()
	}
	it, ref := m.Show(ctx, ev.Project, ev.Item)
	if ref != nil {
		return ref.Result()
	}
	verb := "merged"
	if it.Kind == KindEpic {
		verb = "doc_merged"
	}
	var res cli.Result
	for range mergeRetries {
		res = m.Apply(ctx, Request{Project: ev.Project, Item: ev.Item, Verb: verb, Params: map[string]string{"sha": ev.SHA, "spec_path": ev.SpecPath},
			Actor: Server("reactor")})
		if res.Refusal == nil || res.Refusal.Code != cli.CodeStatusChanged {
			break
		}
	}
	if r := res.Refusal; r != nil && r.Code == cli.CodeTransitionNotAllowed {
		// A terminal item is finished. An epic in a state with no document
		// row already counted its document's merge.
		if t, ok := TableFor(it.Kind); ok && (t.Terminal(r.CurrentStatus) || it.Kind == KindEpic) {
			return cli.Nothing()
		}
	}
	return res
}

// HeadChanged returns a ticket whose PR head moved after a review pass to
// in_review, and clears reviewed_head in the same write (AE1, R8). Any
// other item, or a head equal to the reviewed head, answers Nothing.
func (m *Machine) HeadChanged(ctx context.Context, project, item, sha string) cli.Result {
	it, ref := m.Show(ctx, project, item)
	if ref != nil {
		return ref.Result()
	}
	if it.Kind != KindTicket || !slices.Contains([]string{ReadyToMerge, Queued}, it.Status) || it.Fact(FactReviewedHead) == sha {
		return cli.Nothing()
	}
	return m.Apply(ctx, Request{Project: project, Item: item, Verb: "head_changed", ExpectedStatus: it.Status,
		Params: map[string]string{"head": sha}, Actor: Server("reactor")})
}
