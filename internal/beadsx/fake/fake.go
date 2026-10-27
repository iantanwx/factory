// Package fake is an in-memory beadsx.Client with the semantics of the
// pinned bd: one conditional update writes everything or nothing, a miss is
// beadsx.ErrCASMiss, metadata merges and unsets, and the events journal has a
// gapless, increasing seq. The contract suite runs it beside the real client.
package fake

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
)

// Client is the fake. The zero value is not usable; call New.
type Client struct {
	mu       sync.Mutex
	prefix   string
	next     int
	issues   map[string]*beadsx.Issue
	deps     map[string][]dep
	statuses map[string]beadsx.StatusCategory
	types    map[string]bool
	journal  []beadsx.Event
	now      func() time.Time

	unavailable bool
	unclear     int
}

type dep struct {
	to   string
	kind beadsx.DepKind
}

var _ beadsx.Client = (*Client)(nil)

// New returns an empty fake whose issue ids start with prefix.
func New(prefix string) *Client {
	return &Client{
		prefix:   prefix,
		issues:   map[string]*beadsx.Issue{},
		deps:     map[string][]dep{},
		statuses: map[string]beadsx.StatusCategory{},
		types:    map[string]bool{},
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetUnavailable makes every call fail with beadsx.ErrUnavailable, as a
// stopped Dolt server does. Nothing is written while it is set.
func (c *Client) SetUnavailable(down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unavailable = down
}

// InjectUnclear makes the next n successful updates apply their write and
// then return beadsx.ErrUnavailable, as a connection that drops after the
// commit does. Tests of KTD6 use it to prove a re-read decides "applied".
func (c *Client) InjectUnclear(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unclear = n
}

func (c *Client) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", beadsx.ErrUnavailable, err)
	}
	if c.unavailable {
		return fmt.Errorf("%w: fake is down", beadsx.ErrUnavailable)
	}
	return nil
}

func clone(is *beadsx.Issue) beadsx.Issue {
	out := *is
	out.Metadata = maps.Clone(is.Metadata)
	if out.Metadata == nil {
		out.Metadata = map[string]string{}
	}
	return out
}

func (c *Client) record(op string, is *beadsx.Issue) {
	snap := clone(is)
	c.journal = append(c.journal, beadsx.Event{
		Seq: int64(len(c.journal) + 1), TS: c.now(), Op: op, IssueID: is.ID, Actor: "loop", Issue: &snap,
	})
}

func (c *Client) knownStatus(s string) bool {
	if beadsx.IsBuiltinStatus(s) {
		return true
	}
	_, ok := c.statuses[s]
	return ok
}

// Version implements beadsx.Client.
func (c *Client) Version(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return "", err
	}
	return beadsx.PinnedBD, nil
}

// ConfigureStatuses implements beadsx.Client.
func (c *Client) ConfigureStatuses(ctx context.Context, defs []beadsx.StatusDef) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	if err := beadsx.ValidateStatuses(defs); err != nil {
		return err
	}
	c.statuses = map[string]beadsx.StatusCategory{}
	for _, d := range defs {
		c.statuses[d.Name] = d.Category
	}
	return nil
}

// ConfigureTypes implements beadsx.Client.
func (c *Client) ConfigureTypes(ctx context.Context, types []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	if err := beadsx.ValidateTypes(types); err != nil {
		return err
	}
	c.types = map[string]bool{}
	for _, t := range types {
		c.types[t] = true
	}
	return nil
}

// Create implements beadsx.Client.
func (c *Client) Create(ctx context.Context, spec beadsx.CreateSpec) (beadsx.Issue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return beadsx.Issue{}, err
	}
	if err := spec.Validate(); err != nil {
		return beadsx.Issue{}, err
	}
	typ := spec.Type
	if typ == "" {
		typ = "task"
	}
	if !beadsx.IsBuiltinType(typ) && !c.types[typ] {
		return beadsx.Issue{}, fmt.Errorf("%w: invalid issue type: %s", beadsx.ErrInvalid, typ)
	}
	c.next++
	now := c.now()
	is := &beadsx.Issue{
		ID: c.prefix + "-" + strconv.FormatInt(int64(c.next), 36), Title: spec.Title,
		Description: spec.Description, Status: "open", Type: typ, Priority: spec.Priority,
		Metadata: maps.Clone(spec.Metadata), CreatedAt: now, UpdatedAt: now,
	}
	if is.Metadata == nil {
		is.Metadata = map[string]string{}
	}
	c.issues[is.ID] = is
	c.record("create", is)
	return clone(is), nil
}

// Get implements beadsx.Client.
func (c *Client) Get(ctx context.Context, id string) (beadsx.Issue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return beadsx.Issue{}, err
	}
	is, ok := c.issues[id]
	if !ok {
		return beadsx.Issue{}, fmt.Errorf("%w: %s", beadsx.ErrNotFound, id)
	}
	return clone(is), nil
}

// List implements beadsx.Client.
func (c *Client) List(ctx context.Context, f beadsx.Filter) ([]beadsx.Issue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	out := []beadsx.Issue{}
	for _, is := range c.issues {
		if f.Match(*is) {
			out = append(out, clone(is))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Update implements beadsx.Client. Like bd, it resolves the id first, so an
// unknown id is ErrNotFound and not a miss.
func (c *Client) Update(ctx context.Context, id string, spec beadsx.UpdateSpec) (beadsx.Issue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return beadsx.Issue{}, err
	}
	if err := spec.Validate(); err != nil {
		return beadsx.Issue{}, err
	}
	is, ok := c.issues[id]
	if !ok {
		return beadsx.Issue{}, fmt.Errorf("%w: %s", beadsx.ErrNotFound, id)
	}
	if spec.Status != "" && !c.knownStatus(spec.Status) {
		return beadsx.Issue{}, fmt.Errorf("%w: invalid status %q", beadsx.ErrInvalid, spec.Status)
	}
	if is.Status != spec.IfStatus {
		return beadsx.Issue{}, fmt.Errorf("%w: %s has status %q, expected %q", beadsx.ErrCASMiss, id, is.Status, spec.IfStatus)
	}
	if spec.IfAssignee != nil && is.Assignee != *spec.IfAssignee {
		return beadsx.Issue{}, fmt.Errorf("%w: %s is held by %q, expected %q", beadsx.ErrCASMiss, id, is.Assignee, *spec.IfAssignee)
	}
	if spec.Status != "" {
		is.Status = spec.Status
	}
	if spec.Assignee != nil {
		is.Assignee = *spec.Assignee
	}
	for k, v := range spec.SetMetadata {
		is.Metadata[k] = v
	}
	for _, k := range spec.UnsetMetadata {
		delete(is.Metadata, k)
	}
	is.UpdatedAt = c.now()
	c.record("update", is)
	if c.unclear > 0 {
		c.unclear--
		return beadsx.Issue{}, fmt.Errorf("%w: connection dropped after the write", beadsx.ErrUnavailable)
	}
	return clone(is), nil
}

// Events implements beadsx.Client.
func (c *Client) Events(ctx context.Context, id string) ([]beadsx.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	var out []beadsx.Event
	for _, e := range c.journal {
		if e.IssueID == id {
			out = append(out, copyEvent(e))
		}
	}
	return out, nil
}

// Tail implements beadsx.Client.
func (c *Client) Tail(ctx context.Context, since int64, limit int) ([]beadsx.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	var out []beadsx.Event
	for _, e := range c.journal {
		if e.Seq > since {
			out = append(out, copyEvent(e))
			if limit > 0 && len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// Head implements beadsx.Client.
func (c *Client) Head(ctx context.Context) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return 0, err
	}
	return int64(len(c.journal)), nil
}

func copyEvent(e beadsx.Event) beadsx.Event {
	if e.Issue != nil {
		is := clone(e.Issue)
		e.Issue = &is
	}
	return e
}

// AddDependency implements beadsx.Client. Like bd 1.3.1, a second add of
// the same pair keeps the one edge, succeeds and journals dep_add again.
func (c *Client) AddDependency(ctx context.Context, from, to string, kind beadsx.DepKind) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	for _, id := range []string{from, to} {
		if _, ok := c.issues[id]; !ok {
			return fmt.Errorf("%w: %s", beadsx.ErrNotFound, id)
		}
	}
	if kind == "" {
		kind = beadsx.DepBlocks
	}
	if !slices.ContainsFunc(c.deps[from], func(d dep) bool { return d.to == to }) {
		c.deps[from] = append(c.deps[from], dep{to: to, kind: kind})
	}
	c.record("dep_add", c.issues[from])
	return nil
}

// RemoveDependency implements beadsx.Client. Like bd 1.3.1, it journals
// dep_remove only when it deletes an edge.
func (c *Client) RemoveDependency(ctx context.Context, from, to string, kind beadsx.DepKind) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return err
	}
	for _, id := range []string{from, to} {
		if _, ok := c.issues[id]; !ok {
			return fmt.Errorf("%w: %s", beadsx.ErrNotFound, id)
		}
	}
	if kind == "" {
		kind = beadsx.DepBlocks
	}
	n := len(c.deps[from])
	c.deps[from] = slices.DeleteFunc(c.deps[from], func(d dep) bool { return d.to == to && d.kind == kind })
	if len(c.deps[from]) < n {
		c.record("dep_remove", c.issues[from])
	}
	return nil
}

// Dependencies implements beadsx.Client.
func (c *Client) Dependencies(ctx context.Context, id string) ([]beadsx.Dependency, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.check(ctx); err != nil {
		return nil, err
	}
	if _, ok := c.issues[id]; !ok {
		return nil, fmt.Errorf("%w: %s", beadsx.ErrNotFound, id)
	}
	out := make([]beadsx.Dependency, 0, len(c.deps[id]))
	for _, d := range c.deps[id] {
		out = append(out, beadsx.Dependency{ID: d.to, Kind: d.kind, Status: c.issues[d.to].Status})
	}
	return out, nil
}
