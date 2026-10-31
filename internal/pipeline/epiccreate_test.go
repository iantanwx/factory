package pipeline_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

const operatorWords = "Ian:  port the tickertown watchlist,\nthen let traders share one. "

// prdEpic is the coordinator's flags for a PRD epic, with extra on top.
func prdEpic(extra map[string]string) map[string]string {
	flags := map[string]string{"title": "Watchlists", "project": testProject, "request": operatorWords,
		"body": "Port the tickertown watchlist."}
	maps.Copy(flags, extra)
	return flags
}

// archEpic is the coordinator's flags for an architecture epic, with extra
// on top.
func archEpic(extra map[string]string) map[string]string {
	flags := map[string]string{"title": "Move the store to Postgres", "project": testProject, "architecture": "true"}
	maps.Copy(flags, extra)
	return flags
}

// C4: each refusal of epic create returns its code and rule, and the
// project holds no new item and no new edge after it.
func TestC4EpicCreateRefusesBeforeAnyWrite(t *testing.T) {
	w := newWorld(t)
	w.brainstormCap = 1
	w.commitPRD(prdText)
	ok(t, w.submit("k-prd"))
	open := w.newItem(machine.KindTicket, machine.VerbCreate)
	bug := w.newItem(machine.KindBug, machine.VerbFile)
	before := len(w.items(""))

	for _, c := range []struct {
		name       string
		pr         func() map[string]string
		disable    bool
		code, rule string
	}{
		{"no --architecture and no --request", func() map[string]string { f := prdEpic(nil); delete(f, "request"); return f },
			false, cli.CodePRDRequired, "no_operator_request"},
		{"a blank --request", func() map[string]string { return prdEpic(map[string]string{"request": "  "}) },
			false, cli.CodePRDRequired, "no_operator_request"},
		{"a project without unattended-brainstorm", func() map[string]string { return prdEpic(nil) },
			true, cli.CodeUsage, "prd_author_disabled"},
		{"an unknown blocker on a PRD epic", func() map[string]string { return prdEpic(map[string]string{"blocked-by": open + ",tk-404"}) },
			false, cli.CodeNotFound, "no_blocker"},
		{"an unknown blocker on an architecture epic", func() map[string]string { return archEpic(map[string]string{"blocked-by": open + ",tk-404"}) },
			false, cli.CodeNotFound, "no_blocker"},
		{"a bug blocker on a PRD epic", func() map[string]string { return prdEpic(map[string]string{"blocked-by": open + "," + bug}) },
			false, cli.CodeUsage, "blocker_kind"},
		{"a bug blocker on an architecture epic", func() map[string]string { return archEpic(map[string]string{"blocked-by": open + "," + bug}) },
			false, cli.CodeUsage, "blocker_kind"},
		{"a full brainstorm cap", func() map[string]string { return prdEpic(nil) },
			false, cli.CodeCapFull, "cap_full"},
	} {
		w.disabled[machine.StageUnattendedBrainstorm] = c.disable
		res := w.call(w.coordinator(), "", "epic create", c.pr())
		if r := wantCode(t, res, c.code); r.Rule != c.rule {
			t.Errorf("%s: rule %q, want %q", c.name, r.Rule, c.rule)
		}
	}
	w.disabled[machine.StageUnattendedBrainstorm] = false
	for name, principal := range map[string]rpc.Principal{"a run": w.run("run-x", "", machine.StageImplementer), "a room": w.brainstormRoom()} {
		res := w.call(principal, "", "epic create", prdEpic(nil))
		if r := wantCode(t, res, cli.CodeCapabilityDenied); r.Rule != "coordinator_only" {
			t.Errorf("%s: rule %q, want coordinator_only", name, r.Rule)
		}
	}

	if n := len(w.items("")); n != before {
		t.Fatalf("refused creates made %d items", n-before)
	}
	for _, it := range w.items("") {
		if e := w.edges(it.ID); len(e) != 0 {
			t.Fatalf("a refused create left %s waiting on %v", it.ID, e)
		}
	}
}

// C5: epic create --request gives the epic the operator's words verbatim,
// and --blocked-by on either branch holds one blocks edge to each named
// item, lands in backlog with the entry it unblocks to, and fires no
// decision. A replay returns the same epic with no second edge.
func TestC5EpicCreateBlockedByRecordsTheEdgesOnBothBranches(t *testing.T) {
	w := newWorld(t)
	ticket := w.newItem(machine.KindTicket, machine.VerbCreate)
	epic := w.newItem(machine.KindEpic, machine.VerbCreateArchitecture)
	decision := w.newItem(machine.KindDecision, machine.VerbFile)
	blockers := []string{ticket, epic, decision}

	for _, c := range []struct {
		name, key string
		flags     map[string]string
		entry     string
	}{
		{"--request", "k-prd", prdEpic(map[string]string{"blocked-by": strings.Join(blockers, ",")}), machine.PRDNeeded},
		{"--architecture", "k-arch", archEpic(map[string]string{"blocked-by": strings.Join(blockers, ",")}), machine.Architecture},
	} {
		data := ok(t, w.call(w.coordinator(), c.key, "epic create", c.flags))
		id := data["epic"].(string)
		it := w.show(id)
		if it.Kind != machine.KindEpic || it.Status != machine.Backlog || data["status"] != machine.Backlog {
			t.Fatalf("%s: the epic is a %s in %s, answered %v; want an epic in %s", c.name, it.Kind, it.Status, data["status"], machine.Backlog)
		}
		if got := it.Fact(machine.FactEpicEntry); got != c.entry {
			t.Errorf("%s: epic_entry = %q, want %q", c.name, got, c.entry)
		}
		if got := w.edges(id); !slices.Equal(got, blockers) {
			t.Errorf("%s: the epic waits on %v, want %v", c.name, got, blockers)
		}
		again := ok(t, w.call(w.coordinator(), c.key, "epic create", c.flags))
		if again["epic"] != id || again["status"] != machine.Backlog {
			t.Errorf("%s: the replay answered %v, want %s in %s", c.name, again, id, machine.Backlog)
		}
		if got := w.edges(id); !slices.Equal(got, blockers) {
			t.Errorf("%s: after the replay the epic waits on %v", c.name, got)
		}
	}

	prd := w.items(machine.KindEpic)
	var made machine.Item
	for _, it := range prd {
		if it.Fact(machine.FactEpicEntry) == machine.PRDNeeded {
			made = w.show(it.ID)
		}
	}
	if made.Fact(machine.FactRequest) != operatorWords || made.Title != "Watchlists" || !strings.Contains(made.Body, "Port the tickertown watchlist.") {
		t.Errorf("the PRD epic holds request %q, title %q, body %q", made.Fact(machine.FactRequest), made.Title, made.Body)
	}
	if got := w.show(decision).Status; got != machine.DecisionPending {
		t.Errorf("the blocker decision is %s after epic create, want %s", got, machine.DecisionPending)
	}
	if n := len(w.items(machine.KindEpic)); n != 3 {
		t.Errorf("the project holds %d epics, want 3", n)
	}
}

// C5: --request with no blocker creates the epic in prd_needed with the
// operator's words, and no edge.
func TestC5EpicCreateRequestLandsInPRDNeeded(t *testing.T) {
	w := newWorld(t)
	data := ok(t, w.call(w.coordinator(), "k-free", "epic create", prdEpic(nil)))
	it := w.show(data["epic"].(string))
	if it.Status != machine.PRDNeeded || data["status"] != machine.PRDNeeded {
		t.Fatalf("the epic is in %s, answered %v; want %s", it.Status, data["status"], machine.PRDNeeded)
	}
	if it.Fact(machine.FactRequest) != operatorWords || it.Fact(machine.FactEpicEntry) != "" || len(w.edges(it.ID)) != 0 {
		t.Fatalf("the epic holds request %q, epic_entry %q, edges %v", it.Fact(machine.FactRequest), it.Fact(machine.FactEpicEntry), w.edges(it.ID))
	}
}

// C5 kill point: on either branch, a crash after the edges and before the
// create row leaves the epic in Initial with its edges. A replay lands it
// in backlog with the same edges, once each.
func TestC5EpicCreateCrashAfterTheEdgesReplaysToBacklog(t *testing.T) {
	for _, c := range []struct {
		name  string
		flags func(blocker string) map[string]string
		entry string
	}{
		{"--request", func(b string) map[string]string { return prdEpic(map[string]string{"blocked-by": b}) }, machine.PRDNeeded},
		{"--architecture", func(b string) map[string]string { return archEpic(map[string]string{"blocked-by": b}) }, machine.Architecture},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			x := w.newItem(machine.KindTicket, machine.VerbCreate)
			flags := c.flags(x)
			w.p.SetCrash(func(point string) bool { return point == "blockers_added" })
			wantCode(t, w.call(w.coordinator(), "k-crash", "epic create", flags), cli.CodeInternal)
			w.p.SetCrash(nil)

			epics := w.items(machine.KindEpic)
			if len(epics) != 1 || epics[0].Status != machine.Initial {
				t.Fatalf("after the crash: %+v, want one epic in %s", epics, machine.Initial)
			}
			id := epics[0].ID
			if got := w.edges(id); len(got) != 1 || got[0] != x {
				t.Fatalf("after the crash the epic waits on %v", got)
			}

			data := ok(t, w.call(w.coordinator(), "k-crash", "epic create", flags))
			if data["epic"] != id || data["status"] != machine.Backlog {
				t.Fatalf("the replay answered %v, want %s in %s", data, id, machine.Backlog)
			}
			it := w.show(id)
			if it.Status != machine.Backlog || it.Fact(machine.FactEpicEntry) != c.entry {
				t.Fatalf("after the replay the epic is in %s with epic_entry %q", it.Status, it.Fact(machine.FactEpicEntry))
			}
			if got := w.edges(id); len(got) != 1 || got[0] != x {
				t.Fatalf("after the replay the epic waits on %v", got)
			}
		})
	}
}

// C6: at a brainstorm cap of 1 with one epic in prd_review, a PRD epic with
// no blocker is CAP_FULL and nothing is created. With --blocked-by it is
// created in backlog. A replay of a create that landed before the cap
// filled returns that epic. An architecture epic never asks the cap.
func TestC6TheBrainstormCapRefusesANewPRDEpic(t *testing.T) {
	w := newWorld(t)
	w.brainstormCap = 1
	early := ok(t, w.call(w.coordinator(), "k-early", "epic create", prdEpic(nil)))
	if early["status"] != machine.PRDNeeded {
		t.Fatalf("the first PRD epic answered %v, want %s", early, machine.PRDNeeded)
	}
	w.commitPRD(prdText)
	ok(t, w.submit("k-prd"))
	if n := len(w.items(machine.KindEpic)); n != 2 {
		t.Fatalf("the project holds %d epics before the cases, want 2", n)
	}
	inReview := 0
	for _, it := range w.items(machine.KindEpic) {
		if it.Status == machine.PRDReview {
			inReview++
		}
	}
	if inReview != 1 {
		t.Fatalf("%d epics in %s, want 1", inReview, machine.PRDReview)
	}
	before := len(w.items(""))

	// A PRD epic with no blocker.
	if r := wantCode(t, w.call(w.coordinator(), "k-full", "epic create", prdEpic(nil)), cli.CodeCapFull); r.Rule != "cap_full" {
		t.Fatalf("rule %q, want cap_full", r.Rule)
	}
	if n := len(w.items("")); n != before {
		t.Fatalf("CAP_FULL made %d items", n-before)
	}

	// The replay of the create that landed before the cap filled.
	again := ok(t, w.call(w.coordinator(), "k-early", "epic create", prdEpic(nil)))
	if again["epic"] != early["epic"] || again["status"] != machine.PRDNeeded {
		t.Fatalf("the replay answered %v, want %v", again, early)
	}

	// A PRD epic with a blocker.
	x := w.newItem(machine.KindTicket, machine.VerbCreate)
	blocked := ok(t, w.call(w.coordinator(), "k-blocked", "epic create", prdEpic(map[string]string{"blocked-by": x})))
	if blocked["status"] != machine.Backlog || w.show(blocked["epic"].(string)).Status != machine.Backlog {
		t.Fatalf("the blocked PRD epic answered %v, want %s", blocked, machine.Backlog)
	}

	// An architecture epic.
	arch := ok(t, w.call(w.coordinator(), "k-arch", "epic create", archEpic(nil)))
	if arch["status"] != machine.Architecture || w.show(arch["epic"].(string)).Status != machine.Architecture {
		t.Fatalf("the architecture epic answered %v, want %s", arch, machine.Architecture)
	}
}
