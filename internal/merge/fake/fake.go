// Package fake is an in-memory merge mechanism. The contract suite runs
// against it beside the three real mechanisms, and a test of another
// package can use it to drive the hand-off without GitHub.
package fake

import (
	"context"
	"sync"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/merge"
)

// Name is the fake's mechanism name. No project config can name it.
const Name config.MergeMechanism = "fake"

// Mechanism holds PRs in memory. A test sets each PR's next outcome.
type Mechanism struct {
	mu        sync.Mutex
	held      map[int]string // PR number -> handed-off head
	outcome   map[int]merge.Result
	withdrawn map[int]bool
	requeues  map[int]int
	// CheckErr is what Check answers.
	CheckErr error
}

// New returns an empty fake.
func New() *Mechanism {
	return &Mechanism{held: map[int]string{}, outcome: map[int]merge.Result{}, withdrawn: map[int]bool{}, requeues: map[int]int{}}
}

// Name implements merge.Mechanism.
func (m *Mechanism) Name() config.MergeMechanism { return Name }

// Check implements merge.Mechanism.
func (m *Mechanism) Check(context.Context, config.Project) error { return m.CheckErr }

// HandOff implements merge.Mechanism.
func (m *Mechanism) HandOff(_ context.Context, pr merge.PR) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.held[pr.Number] = pr.Head
	delete(m.withdrawn, pr.Number)
	return nil
}

// Requeue implements merge.Mechanism.
func (m *Mechanism) Requeue(ctx context.Context, pr merge.PR) error {
	m.mu.Lock()
	m.requeues[pr.Number]++
	delete(m.outcome, pr.Number)
	m.mu.Unlock()
	return m.HandOff(ctx, pr)
}

// Withdraw implements merge.Mechanism.
func (m *Mechanism) Withdraw(_ context.Context, pr merge.PR) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.held, pr.Number)
	m.withdrawn[pr.Number] = true
	return nil
}

// Poll implements merge.Mechanism: the outcome the test set, or pending.
func (m *Mechanism) Poll(_ context.Context, pr merge.PR) (merge.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.outcome[pr.Number]; ok {
		return r, nil
	}
	return merge.Result{Outcome: merge.Pending}, nil
}

// Set makes r the outcome of PR n's next polls.
func (m *Mechanism) Set(n int, r merge.Result) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outcome[n] = r
}

// HandedOff reports whether the fake holds PR n.
func (m *Mechanism) HandedOff(n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.held[n]
	return ok
}

// Withdrawn reports whether the job withdrew PR n.
func (m *Mechanism) Withdrawn(n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.withdrawn[n]
}

// Requeues counts the re-queues of PR n.
func (m *Mechanism) Requeues(n int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requeues[n]
}
