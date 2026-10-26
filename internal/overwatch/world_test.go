package overwatch

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/store"
)

// fakeWorld serves the reads of the records from maps and a real store.
// Its host runs the default config with an empty temporary checkout root, and
// no project holds a config, so the stalls part reads an idle host. Every read
// a part of a later entry makes answers errFake.
type fakeWorld struct {
	now      time.Time
	root     string
	projects []config.Project
	items    map[string][]machine.Item    // project -> items, no body
	bodies   map[string]map[string]string // project -> item -> body
	st       *store.Store
	itemsErr error
}

var errFake = errors.New("the fake world holds no such read")

func newFakeWorld(t *testing.T) *fakeWorld {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &fakeWorld{
		now:      time.Date(2026, 10, 24, 9, 3, 0, 0, time.UTC),
		root:     t.TempDir(),
		projects: []config.Project{{ID: "factorio", Repo: "slothware-labs/loop"}, {ID: "alpha", Repo: "iantanwx/alpha"}},
		items:    map[string][]machine.Item{},
		bodies:   map[string]map[string]string{},
		st:       st,
	}
}

// add puts one item and its body in a project.
func (w *fakeWorld) add(project string, it machine.Item, body string) {
	it.Project = project
	if it.Facts == nil {
		it.Facts = map[string]string{}
	}
	w.items[project] = append(w.items[project], it)
	if w.bodies[project] == nil {
		w.bodies[project] = map[string]string{}
	}
	w.bodies[project][it.ID] = body
}

func (w *fakeWorld) Now() time.Time { return w.now }
func (w *fakeWorld) Host(context.Context) (*config.HostConfig, error) {
	hc := config.DefaultHost("")
	hc.AgentHost.CheckoutRoot = w.root
	return hc, nil
}
func (w *fakeWorld) Projects(context.Context) ([]config.Project, error) { return w.projects, nil }
func (w *fakeWorld) ProjectConfig(context.Context, string) (*config.ProjectSnapshot, error) {
	return nil, config.ErrConfigMissing
}
func (w *fakeWorld) Items(_ context.Context, project string, f machine.ListFilter) ([]machine.Item, error) {
	if w.itemsErr != nil {
		return nil, w.itemsErr
	}
	var out []machine.Item
	for _, it := range w.items[project] {
		if (f.Kind == "" || it.Kind == f.Kind) && (f.Status == "" || it.Status == f.Status) {
			out = append(out, it)
		}
	}
	return out, nil
}
func (w *fakeWorld) Body(_ context.Context, project, item string) (string, error) {
	b, ok := w.bodies[project][item]
	if !ok {
		return "", errFake
	}
	return b, nil
}
func (w *fakeWorld) Dependencies(context.Context, string, string) ([]beadsx.Dependency, error) {
	return nil, errFake
}
func (w *fakeWorld) Store() *store.Store { return w.st }
func (w *fakeWorld) Intervals() Intervals {
	return Intervals{}
}
func (w *fakeWorld) Jobs() sched.Jobs { return sched.Jobs{} }
func (w *fakeWorld) CheckoutDirs(context.Context) ([]string, error) {
	return agenthost.ListCheckoutDirs(w.root)
}
func (w *fakeWorld) Inventory(context.Context) (agenthost.Inventory, error) {
	return agenthost.Inventory{}, errFake
}
func (w *fakeWorld) Coordinator() sched.CoordinatorState                { return sched.CoordinatorState{} }
func (w *fakeWorld) GitHub() GitHubReads                                { return nil }
func (w *fakeWorld) Doctor(context.Context) ([]github.CheckLine, error) { return nil, errFake }
func (w *fakeWorld) LastRing(ctx context.Context) (RingOutcome, bool, error) {
	return LastRing(ctx, w.st)
}

// Inbox builds the inbox from the fake's projects, items and store, as the
// server's inbox builds it from beads and the store.
func (w *fakeWorld) Inbox(ctx context.Context) ([]inbox.Entry, error) {
	list := func(ctx context.Context, project string, f machine.ListFilter) ([]machine.Item, *cli.Refusal) {
		items, err := w.Items(ctx, project, f)
		if err != nil {
			return nil, cli.InternalRefusal(err)
		}
		return items, nil
	}
	return inbox.New(inbox.Config{Store: w.st, List: list, Projects: func() ([]config.Project, error) { return w.projects, nil },
		Now: func() time.Time { return w.now }}).Entries(ctx)
}

// fault writes one loop fault row in status.
func (w *fakeWorld) fault(t *testing.T, text, status string) string {
	t.Helper()
	var n int64
	err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		r, err := tx.Exec(`INSERT INTO faults (text, reporter_kind, status, reported_at, updated_at) VALUES (?, 'coordinator', ?, ?, ?)`,
			text, status, now, now)
		if err != nil {
			return err
		}
		n, err = r.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return "F-" + itoa(n)
}

// question writes one prepared question, answered or not.
func (w *fakeWorld) question(t *testing.T, body string, answered bool) {
	t.Helper()
	err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		now := tx.Now().UnixMilli()
		var at sql.NullInt64
		if answered {
			at = sql.NullInt64{Int64: now, Valid: true}
		}
		_, err := tx.Exec(`INSERT INTO questions (entry, body, run_id, created_at, answer, answered_at) VALUES ('', ?, '', ?, ?, ?)`,
			body, now, "yes", at)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

var _ World = (*fakeWorld)(nil)
