package beadsx_test

// The contract suite. runContract holds every case that must hold for both
// implementations. TestContractFake runs it on the fake on every `go test`.
// TestContractReal (contract_test.go, build tag beads) runs it on the pinned
// bd and Dolt.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
)

// Statuses and types in the shape of the U5 transition tables.
var (
	testStatuses = []beadsx.StatusDef{
		{Name: "implementing", Category: beadsx.CategoryWIP},
		{Name: "in_review", Category: beadsx.CategoryActive},
		{Name: "awaiting_merge", Category: beadsx.CategoryActive},
		{Name: "merged", Category: beadsx.CategoryDone},
		{Name: "escalated", Category: beadsx.CategoryFrozen},
	}
	testTypes = []string{"ticket", "bugreport", "prd"}
)

func TestContractFake(t *testing.T) {
	c := fake.New("fk")
	ctx := context.Background()
	if err := c.ConfigureStatuses(ctx, testStatuses); err != nil {
		t.Fatal(err)
	}
	if err := c.ConfigureTypes(ctx, testTypes); err != nil {
		t.Fatal(err)
	}
	runContract(t, c)
}

func str(s string) *string { return &s }

func mustCreate(t *testing.T, c beadsx.Client, title string) beadsx.Issue {
	t.Helper()
	is, err := c.Create(context.Background(), beadsx.CreateSpec{Title: title, Type: "ticket", Priority: 2})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return is
}

func mustGet(t *testing.T, c beadsx.Client, id string) beadsx.Issue {
	t.Helper()
	is, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return is
}

func runContract(t *testing.T, c beadsx.Client) {
	ctx := context.Background()

	t.Run("AtomicStatusAndMetadata", func(t *testing.T) {
		is := mustCreate(t, c, "atomic")
		got, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{
			IfStatus: "open", Status: "implementing",
			SetMetadata: map[string]string{beadsx.MetaIntentSeq: "1", beadsx.MetaIdemKey: "key-1"},
		})
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if got.Status != "implementing" {
			t.Fatalf("update returned status %q", got.Status)
		}
		read := mustGet(t, c, is.ID)
		if read.Status != "implementing" || read.Metadata[beadsx.MetaIdemKey] != "key-1" {
			t.Fatalf("a read does not show the status and both keys: %+v", read)
		}
		seq, err := read.IntentSeq()
		if err != nil || seq != 1 {
			t.Fatalf("intent seq = %d, %v; want 1", seq, err)
		}
	})

	t.Run("ConcurrentUpdatesOneWinner", func(t *testing.T) {
		is := mustCreate(t, c, "race")
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := range n {
			wg.Go(func() {
				_, errs[i] = c.Update(ctx, is.ID, beadsx.UpdateSpec{
					IfStatus: "open", Status: "implementing",
					SetMetadata: map[string]string{beadsx.MetaIdemKey: fmt.Sprintf("racer-%d", i), fmt.Sprintf("racer-%d", i): "won"},
				})
			})
		}
		wg.Wait()
		winner := -1
		for i, err := range errs {
			switch {
			case err == nil:
				if winner >= 0 {
					t.Fatalf("racers %d and %d both won", winner, i)
				}
				winner = i
			case errors.Is(err, beadsx.ErrCASMiss):
			default:
				t.Fatalf("racer %d: %v, want a miss", i, err)
			}
		}
		if winner < 0 {
			t.Fatal("no racer won")
		}
		read := mustGet(t, c, is.ID)
		if read.Metadata[beadsx.MetaIdemKey] != fmt.Sprintf("racer-%d", winner) {
			t.Fatalf("key is %q, winner was %d", read.Metadata[beadsx.MetaIdemKey], winner)
		}
		for i := range n {
			if _, ok := read.Metadata[fmt.Sprintf("racer-%d", i)]; ok != (i == winner) {
				t.Fatalf("racer %d metadata present=%v, winner %d", i, ok, winner)
			}
		}
	})

	t.Run("MissWritesNothing", func(t *testing.T) {
		is := mustCreate(t, c, "miss")
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "implementing",
			SetMetadata: map[string]string{"a": "1"}}); err != nil {
			t.Fatal(err)
		}
		before := mustGet(t, c, is.ID)
		evBefore, err := c.Events(ctx, is.ID)
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "merged",
			SetMetadata: map[string]string{"a": "2", "b": "x"}, UnsetMetadata: []string{"c"}})
		if !errors.Is(err, beadsx.ErrCASMiss) {
			t.Fatalf("err = %v, want ErrCASMiss", err)
		}
		if errors.Is(err, beadsx.ErrUnavailable) {
			t.Fatal("a miss must not read as unavailable")
		}
		after := mustGet(t, c, is.ID)
		if after.Status != before.Status || !maps.Equal(after.Metadata, before.Metadata) {
			t.Fatalf("miss changed the issue: %+v -> %+v", before, after)
		}
		evAfter, err := c.Events(ctx, is.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(evAfter) != len(evBefore) {
			t.Fatalf("miss added %d journal records", len(evAfter)-len(evBefore))
		}
	})

	t.Run("AssigneeGuard", func(t *testing.T) {
		is := mustCreate(t, c, "fence")
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", IfAssignee: str(""),
			Status: "implementing", Assignee: str("epoch:1")}); err != nil {
			t.Fatal(err)
		}
		// Status holds, the assignee does not: nothing is written.
		_, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "implementing", IfAssignee: str("epoch:0"),
			Status: "in_review", Assignee: str("epoch:2"), SetMetadata: map[string]string{"x": "y"}})
		if !errors.Is(err, beadsx.ErrCASMiss) {
			t.Fatalf("err = %v, want ErrCASMiss", err)
		}
		read := mustGet(t, c, is.ID)
		if read.Status != "implementing" || read.Assignee != "epoch:1" || read.Metadata["x"] != "" {
			t.Fatalf("assignee miss wrote something: %+v", read)
		}
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "implementing", IfAssignee: str("epoch:1"),
			Status: "in_review", Assignee: str("epoch:2")}); err != nil {
			t.Fatal(err)
		}
		if read := mustGet(t, c, is.ID); read.Assignee != "epoch:2" || read.Status != "in_review" {
			t.Fatalf("fenced update: %+v", read)
		}
	})

	t.Run("MetadataStringsMergeAndUnset", func(t *testing.T) {
		is, err := c.Create(ctx, beadsx.CreateSpec{Title: "meta", Type: "ticket",
			Metadata: map[string]string{"keep": "k", "gone": "g"}})
		if err != nil {
			t.Fatal(err)
		}
		if is.Metadata["keep"] != "k" {
			t.Fatalf("create metadata: %+v", is.Metadata)
		}
		vals := map[string]string{"n": "1", "z": "007", "b": "true", "e": "", "f": "1.50", "j": `{"a":1}`}
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", SetMetadata: vals,
			UnsetMetadata: []string{"gone"}}); err != nil {
			t.Fatal(err)
		}
		read := mustGet(t, c, is.ID)
		want := maps.Clone(vals)
		want["keep"] = "k"
		if !maps.Equal(read.Metadata, want) {
			t.Fatalf("metadata = %#v, want %#v", read.Metadata, want)
		}
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", SetMetadata: map[string]string{"a": "1"},
			UnsetMetadata: []string{"a"}}); !errors.Is(err, beadsx.ErrInvalid) {
			t.Fatalf("set and unset of one key: %v", err)
		}
	})

	t.Run("JournalRecordsOneUpdateOnce", func(t *testing.T) {
		is := mustCreate(t, c, "journal")
		for i, st := range []string{"implementing", "in_review", "awaiting_merge"} {
			prev := []string{"open", "implementing", "in_review"}[i]
			if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: prev, Status: st,
				SetMetadata: map[string]string{beadsx.MetaIdemKey: "j-" + st, beadsx.MetaIntentSeq: fmt.Sprint(i + 1)}}); err != nil {
				t.Fatal(err)
			}
		}
		evs, err := c.Events(ctx, is.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(evs) != 4 || evs[0].Op != "create" {
			t.Fatalf("want create + 3 updates, got %+v", evs)
		}
		count := 0
		for i, e := range evs {
			if i > 0 && e.Seq <= evs[i-1].Seq {
				t.Fatalf("seq %d after %d", e.Seq, evs[i-1].Seq)
			}
			if e.Issue != nil && e.Issue.Metadata[beadsx.MetaIdemKey] == "j-in_review" && e.Issue.Status == "in_review" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("the update with key j-in_review is recorded %d times", count)
		}
		// The journal is global: Tail from the first record of this issue
		// returns the later ones in order.
		tail, err := c.Tail(ctx, evs[0].Seq, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(tail) < 3 || tail[0].Seq != evs[0].Seq+1 {
			t.Fatalf("tail after %d: %+v", evs[0].Seq, tail)
		}
		lim, err := c.Tail(ctx, 0, 2)
		if err != nil || len(lim) != 2 || lim[0].Seq != 1 || lim[1].Seq != 2 {
			t.Fatalf("tail limit 2: %+v %v", lim, err)
		}
	})

	t.Run("JournalHead", func(t *testing.T) {
		mustCreate(t, c, "head")
		head, err := c.Head(ctx)
		if err != nil {
			t.Fatal(err)
		}
		all, err := c.Tail(ctx, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) == 0 || head != all[len(all)-1].Seq {
			t.Fatalf("head = %d, want the seq of the last record of %d", head, len(all))
		}
		is := mustCreate(t, c, "head-2")
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "implementing"}); err != nil {
			t.Fatal(err)
		}
		next, err := c.Head(ctx)
		if err != nil || next != head+2 {
			t.Fatalf("head after one create and one update = %d, %v; want %d", next, err, head+2)
		}
		after, err := c.Tail(ctx, next, 0)
		if err != nil || len(after) != 0 {
			t.Fatalf("the journal holds records after the head: %+v %v", after, err)
		}
	})

	t.Run("LookupByIdempotencyKey", func(t *testing.T) {
		is := mustCreate(t, c, "lookup")
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "implementing",
			SetMetadata: map[string]string{beadsx.MetaIdemKey: "lk-1"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "implementing", Status: "in_review",
			SetMetadata: map[string]string{beadsx.MetaIdemKey: "lk-2"}}); err != nil {
			t.Fatal(err)
		}
		got, err := c.List(ctx, beadsx.Filter{Metadata: map[string]string{beadsx.MetaIdemKey: "lk-2"}})
		if err != nil || len(got) != 1 || got[0].ID != is.ID {
			t.Fatalf("list by current key: %+v %v", got, err)
		}
		got, err = c.List(ctx, beadsx.Filter{Metadata: map[string]string{beadsx.MetaIdemKey: "lk-1"}})
		if err != nil || len(got) != 0 {
			t.Fatalf("an old key must not match current metadata: %+v %v", got, err)
		}
		evs, err := c.Events(ctx, is.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, e := range evs {
			if e.Issue != nil && e.Issue.Metadata[beadsx.MetaIdemKey] == "lk-1" {
				found++
			}
		}
		if found != 1 {
			t.Fatalf("journal holds key lk-1 in %d records, want 1", found)
		}
	})

	t.Run("CustomStatusesAndTypes", func(t *testing.T) {
		if err := c.ConfigureStatuses(ctx, testStatuses); err != nil {
			t.Fatal(err)
		}
		if err := c.ConfigureTypes(ctx, testTypes); err != nil {
			t.Fatal(err)
		}
		for _, typ := range testTypes {
			if _, err := c.Create(ctx, beadsx.CreateSpec{Title: "typed " + typ, Type: typ}); err != nil {
				t.Fatalf("type %s: %v", typ, err)
			}
		}
		is := mustCreate(t, c, "walk")
		prev := "open"
		for _, s := range testStatuses {
			if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: prev, Status: s.Name}); err != nil {
				t.Fatalf("%s -> %s: %v", prev, s.Name, err)
			}
			prev = s.Name
		}
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: prev, Status: "nosuch"}); !errors.Is(err, beadsx.ErrInvalid) {
			t.Fatalf("unknown status: %v", err)
		}
		if _, err := c.Create(ctx, beadsx.CreateSpec{Title: "x", Type: "nosuch"}); !errors.Is(err, beadsx.ErrInvalid) {
			t.Fatalf("unknown type: %v", err)
		}
		for _, bad := range [][]beadsx.StatusDef{
			{{Name: "open", Category: beadsx.CategoryWIP}},
			{{Name: "Bad Name", Category: beadsx.CategoryWIP}},
			{{Name: "ok", Category: "bogus"}},
		} {
			if err := c.ConfigureStatuses(ctx, bad); !errors.Is(err, beadsx.ErrInvalid) {
				t.Fatalf("statuses %+v: %v", bad, err)
			}
		}
	})

	t.Run("ListFilters", func(t *testing.T) {
		a := mustCreate(t, c, "list-a")
		b, err := c.Create(ctx, beadsx.CreateSpec{Title: "list-b", Type: "bugreport", Metadata: map[string]string{"tag": "lf"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Update(ctx, a.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "escalated",
			SetMetadata: map[string]string{"tag": "lf"}}); err != nil {
			t.Fatal(err)
		}
		got, err := c.List(ctx, beadsx.Filter{Statuses: []string{"escalated"}, Metadata: map[string]string{"tag": "lf"}})
		if err != nil || len(got) != 1 || got[0].ID != a.ID {
			t.Fatalf("by status: %+v %v", got, err)
		}
		got, err = c.List(ctx, beadsx.Filter{Type: "bugreport", Metadata: map[string]string{"tag": "lf"}})
		if err != nil || len(got) != 1 || got[0].ID != b.ID {
			t.Fatalf("by type: %+v %v", got, err)
		}
		got, err = c.List(ctx, beadsx.Filter{Metadata: map[string]string{"tag": "lf"}})
		if err != nil || len(got) != 2 {
			t.Fatalf("by metadata: %+v %v", got, err)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		if _, err := c.Get(ctx, "zz-nope"); !errors.Is(err, beadsx.ErrNotFound) {
			t.Fatalf("get: %v", err)
		}
		if _, err := c.Update(ctx, "zz-nope", beadsx.UpdateSpec{IfStatus: "open", Status: "implementing"}); !errors.Is(err, beadsx.ErrNotFound) {
			t.Fatalf("update: %v", err)
		}
		is := mustCreate(t, c, "dep")
		if err := c.AddDependency(ctx, is.ID, "zz-nope", beadsx.DepBlocks); !errors.Is(err, beadsx.ErrNotFound) {
			t.Fatalf("dep: %v", err)
		}
	})

	t.Run("Dependency", func(t *testing.T) {
		a, b := mustCreate(t, c, "dep-a"), mustCreate(t, c, "dep-b")
		if err := c.AddDependency(ctx, a.ID, b.ID, beadsx.DepBlocks); err != nil {
			t.Fatal(err)
		}
		got, err := c.Dependencies(ctx, a.ID)
		if err != nil || len(got) != 1 || got[0] != (beadsx.Dependency{ID: b.ID, Kind: beadsx.DepBlocks, Status: "open"}) {
			t.Fatalf("dependencies of a = %+v %v", got, err)
		}
		if got, err := c.Dependencies(ctx, b.ID); err != nil || len(got) != 0 {
			t.Fatalf("b depends on nothing, read %+v %v", got, err)
		}
		// The read carries the target's current status.
		if _, err := c.Update(ctx, b.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "merged"}); err != nil {
			t.Fatal(err)
		}
		if got, err := c.Dependencies(ctx, a.ID); err != nil || len(got) != 1 || got[0].Status != "merged" {
			t.Fatalf("dependencies of a after b merged = %+v %v", got, err)
		}
		if _, err := c.Dependencies(ctx, "zz-nope"); !errors.Is(err, beadsx.ErrNotFound) {
			t.Fatalf("dependencies of an unknown id: %v", err)
		}
	})

	// The server moves a ticket to a done status when it observes the
	// merge, after its blocker reached a done status. The edge must not
	// refuse that write: a blocker in a done status blocks nothing.
	t.Run("DoneBlockerBlocksNoDone", func(t *testing.T) {
		a, b := mustCreate(t, c, "done-a"), mustCreate(t, c, "done-b")
		if err := c.AddDependency(ctx, a.ID, b.ID, beadsx.DepBlocks); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Update(ctx, b.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "merged"}); err != nil {
			t.Fatal(err)
		}
		got, err := c.Update(ctx, a.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "merged"})
		if err != nil || got.Status != "merged" {
			t.Fatalf("the dependent of a merged blocker = %+v %v", got, err)
		}
	})

	// A decompose replay adds the same edge again. bd 1.3.1 keeps one edge
	// and reports success, and it journals a dep_add for each call.
	t.Run("DependencyTwice", func(t *testing.T) {
		a, b := mustCreate(t, c, "twice-a"), mustCreate(t, c, "twice-b")
		for i := range 2 {
			if err := c.AddDependency(ctx, a.ID, b.ID, beadsx.DepBlocks); err != nil {
				t.Fatalf("add %d: %v", i+1, err)
			}
		}
		got, err := c.Dependencies(ctx, a.ID)
		if err != nil || len(got) != 1 || got[0].ID != b.ID || got[0].Kind != beadsx.DepBlocks {
			t.Fatalf("after two adds of one edge: %+v %v; want one edge", got, err)
		}
	})

	// C11: work unblock --by deletes one blocks edge. The other edges of
	// the issue stay, and so does an edge of another kind to the same issue.
	t.Run("RemoveDependencyDeletesOneEdge", func(t *testing.T) {
		a, x, y, r := mustCreate(t, c, "rm-a"), mustCreate(t, c, "rm-x"), mustCreate(t, c, "rm-y"), mustCreate(t, c, "rm-r")
		for _, e := range []struct {
			to   string
			kind beadsx.DepKind
		}{{x.ID, beadsx.DepBlocks}, {y.ID, beadsx.DepBlocks}, {r.ID, beadsx.DepRelated}} {
			if err := c.AddDependency(ctx, a.ID, e.to, e.kind); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.RemoveDependency(ctx, a.ID, x.ID, beadsx.DepBlocks); err != nil {
			t.Fatalf("remove a blocks x: %v", err)
		}
		if err := c.RemoveDependency(ctx, a.ID, r.ID, beadsx.DepBlocks); err != nil {
			t.Fatalf("remove a blocks r, an edge of another kind: %v", err)
		}
		got, err := c.Dependencies(ctx, a.ID)
		want := map[string]beadsx.DepKind{y.ID: beadsx.DepBlocks, r.ID: beadsx.DepRelated}
		if err != nil || len(got) != len(want) {
			t.Fatalf("after one remove: %+v %v; want %v", got, err, want)
		}
		for _, d := range got {
			if want[d.ID] != d.Kind {
				t.Fatalf("after one remove: %+v; want %v", got, want)
			}
		}
	})

	// C11: a replay of work unblock --by removes the same edge again.
	t.Run("RemoveDependencyTwice", func(t *testing.T) {
		a, x, y := mustCreate(t, c, "rm2-a"), mustCreate(t, c, "rm2-x"), mustCreate(t, c, "rm2-y")
		for _, to := range []string{x.ID, y.ID} {
			if err := c.AddDependency(ctx, a.ID, to, beadsx.DepBlocks); err != nil {
				t.Fatal(err)
			}
		}
		for i := range 2 {
			if err := c.RemoveDependency(ctx, a.ID, x.ID, beadsx.DepBlocks); err != nil {
				t.Fatalf("remove %d: %v", i+1, err)
			}
		}
		got, err := c.Dependencies(ctx, a.ID)
		if err != nil || len(got) != 1 || got[0] != (beadsx.Dependency{ID: y.ID, Kind: beadsx.DepBlocks, Status: "open"}) {
			t.Fatalf("after two removes of one edge: %+v %v; want only the edge to %s", got, err, y.ID)
		}
	})

	// C11: an unknown issue at either end is ErrNotFound.
	t.Run("RemoveDependencyNotFound", func(t *testing.T) {
		is := mustCreate(t, c, "rm-nf")
		if err := c.RemoveDependency(ctx, "zz-nope", is.ID, beadsx.DepBlocks); !errors.Is(err, beadsx.ErrNotFound) {
			t.Fatalf("remove from an unknown issue: %v", err)
		}
		if err := c.RemoveDependency(ctx, is.ID, "zz-nope", beadsx.DepBlocks); !errors.Is(err, beadsx.ErrNotFound) {
			t.Fatalf("remove to an unknown issue: %v", err)
		}
	})

	t.Run("RequestValidation", func(t *testing.T) {
		is := mustCreate(t, c, "valid")
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{Status: "implementing"}); !errors.Is(err, beadsx.ErrInvalid) {
			t.Fatalf("no expected status: %v", err)
		}
		if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open"}); !errors.Is(err, beadsx.ErrInvalid) {
			t.Fatalf("no field: %v", err)
		}
		if _, err := c.Create(ctx, beadsx.CreateSpec{Title: " "}); !errors.Is(err, beadsx.ErrInvalid) {
			t.Fatalf("empty title: %v", err)
		}
	})

	t.Run("Version", func(t *testing.T) {
		v, err := c.Version(ctx)
		if err != nil || v != beadsx.PinnedBD {
			t.Fatalf("version %q %v", v, err)
		}
	})
}

func TestFakeUnavailableAndUnclear(t *testing.T) {
	ctx := context.Background()
	c := fake.New("fk")
	is, err := c.Create(ctx, beadsx.CreateSpec{Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	c.SetUnavailable(true)
	if _, err := c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "closed"}); !errors.Is(err, beadsx.ErrUnavailable) || errors.Is(err, beadsx.ErrCASMiss) {
		t.Fatalf("down: %v", err)
	}
	c.SetUnavailable(false)
	if got, _ := c.Get(ctx, is.ID); got.Status != "open" {
		t.Fatal("a down call wrote")
	}
	c.InjectUnclear(1)
	_, err = c.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: "open", Status: "in_progress",
		SetMetadata: map[string]string{beadsx.MetaIntentSeq: "4"}})
	if !errors.Is(err, beadsx.ErrUnavailable) {
		t.Fatalf("unclear: %v", err)
	}
	got, _ := c.Get(ctx, is.ID)
	if seq, _ := got.IntentSeq(); seq != 4 {
		t.Fatalf("an unclear write is applied, and a re-read shows it: seq %d", seq)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Get(cctx, is.ID); !errors.Is(err, beadsx.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
