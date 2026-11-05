package inbox

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// servedWorld is an inbox of three projects, a, b and loop, the loop
// project. Each project has its own coordinator session. The beads of b
// hold one fired decision, b-1.
type servedWorld struct {
	*routeWorld
}

func newServedWorld(t *testing.T) *servedWorld {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	projects := []config.Project{{ID: "a"}, {ID: "b"}, {ID: "loop"}}
	in := New(Config{Store: st,
		Projects: func() ([]config.Project, error) { return projects, nil },
		List: func(_ context.Context, project string, f machine.ListFilter) ([]machine.Item, *cli.Refusal) {
			if project == "b" && f.Kind == machine.KindDecision && f.Status == machine.NeedsHuman {
				return []machine.Item{{Project: "b", ID: "b-1", Kind: machine.KindDecision, Status: machine.NeedsHuman, Title: "which?"}}, nil
			}
			return nil, nil
		},
		Topology: func() coord.Topology {
			return coord.Topology{Loop: "loop", Homes: map[string]bool{"a": true, "b": true, "loop": true}}
		},
		Sessions: func(project string) (agenthost.RunID, bool) {
			if project == "" {
				return agenthost.RunID{}, false
			}
			return agenthost.RunID{Host: "fake", Terminal: "coordinator-" + project, Incarnation: "1"}, true
		},
	})
	return &servedWorld{&routeWorld{t: t, ctx: ctx, st: st, in: in}}
}

// coordinator is the principal of project's coordinator.
func coordinatorOf(project string) rpc.Principal {
	return rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: "coordinator-" + project, Project: project}
}

func (w *servedWorld) call(verb string, p rpc.Principal, flags map[string]string) cli.Result {
	w.t.Helper()
	inv, bad := cli.Surface().Validate(verb, nil, flags, "req-"+verb)
	if bad != nil {
		w.t.Fatalf("validate %s: %+v", verb, bad)
	}
	c := &rpc.Call{Inv: inv, Principal: p}
	switch verb {
	case "inbox prepare":
		return w.in.prepare(w.ctx, c)
	case "inbox wait":
		return w.in.waitVerb(w.ctx, c)
	}
	return w.in.list(w.ctx, c)
}

// listed is the ids of the entries that p's `inbox list` holds.
func (w *servedWorld) listed(p rpc.Principal) []string {
	w.t.Helper()
	res := w.call("inbox list", p, nil)
	if res.Refusal != nil {
		w.t.Fatalf("inbox list as %s = %+v", p.Actor(), res.Refusal)
	}
	es, _ := res.Data.([]Entry)
	ids := []string{}
	for _, e := range es {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	return ids
}

func (w *servedWorld) alertID(project string) string {
	w.t.Helper()
	var id int64
	if err := w.st.Read(w.ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM alerts WHERE project = ?`, project).Scan(&id)
	}); err != nil {
		w.t.Fatal(err)
	}
	return fmt.Sprintf("alert/%d", id)
}

// Done means of spec work entry 4: the inbox of a coordinator of project
// a, over entries of a and of b, a fault of b and an entry with no
// project, lists the entries of a only. A question that a's coordinator
// prepares holds project a, so it lists in a's inbox too, and `inbox
// wait` answers the same entries.
func TestACoordinatorsInboxListsTheEntriesOfItsOwnProject(t *testing.T) {
	w := newServedWorld(t)
	w.alert("a", "run_crashed", "run_crashed:r-a1")
	w.alert("b", "run_crashed", "run_crashed:r-b1")
	w.alert("", "usage_limit", "usage_limit:claude")
	f, err := (&fault.Service{Store: w.st}).Report(w.ctx, rpc.Principal{Kind: cli.PrincipalRun, RunID: "r-b1", Project: "b",
		Item: "b-2", Stage: "implementer"}, "the CLI refused a valid call")
	if err != nil {
		t.Fatal(err)
	}
	res := w.call("inbox prepare", coordinatorOf("a"), map[string]string{"body": "which page?"})
	if res.Refusal != nil {
		t.Fatalf("inbox prepare = %+v", res.Refusal)
	}
	question := res.Data.(map[string]any)["entry"].(string)

	want := []string{w.alertID("a"), question}
	slices.Sort(want)
	if got := w.listed(coordinatorOf("a")); !slices.Equal(got, want) {
		t.Fatalf("a's inbox = %v, want %v", got, want)
	}
	if got := w.listed(coordinatorOf("b")); !slices.Equal(got, []string{w.alertID("b"), "hold/b/b-1"}) {
		t.Fatalf("b's inbox = %v, want b's alert and hold only", got)
	}
	loop := []string{w.alertID(""), "fault/" + f.ID}
	slices.Sort(loop)
	if got := w.listed(coordinatorOf("loop")); !slices.Equal(got, loop) {
		t.Fatalf("the loop project's inbox = %v, want %v", got, loop)
	}

	res = w.call("inbox wait", coordinatorOf("a"), map[string]string{"since": "0"})
	waited := []string{}
	for _, e := range res.Data.(map[string]any)["entries"].([]Entry) {
		waited = append(waited, e.ID)
	}
	slices.Sort(waited)
	if !slices.Equal(waited, want) {
		t.Fatalf("a's inbox wait = %v, want %v", waited, want)
	}
}

// C12: a question with no project, as an earlier release or the old
// session wrote it, routes by the entry it names. One about hold/b/b-1
// and one about an open alert of b list in b's inbox. One with no entry
// lists in the loop project's inbox.
func TestC12AnOldQuestionRoutesByTheEntryItNames(t *testing.T) {
	w := newServedWorld(t)
	w.alert("b", "run_crashed", "run_crashed:r-b1")
	alert := w.alertID("b")
	w.question("hold/b/b-1")
	w.question(alert)
	w.question("")

	b := []string{alert, "hold/b/b-1", "question/1", "question/2"}
	slices.Sort(b)
	if got := w.listed(coordinatorOf("b")); !slices.Equal(got, b) {
		t.Fatalf("b's inbox = %v, want %v", got, b)
	}
	if got := w.listed(coordinatorOf("loop")); !slices.Equal(got, []string{"question/3"}) {
		t.Fatalf("the loop project's inbox = %v, want question/3 only", got)
	}
	if got := w.listed(coordinatorOf("a")); len(got) != 0 {
		t.Fatalf("a's inbox = %v, want none", got)
	}
}
