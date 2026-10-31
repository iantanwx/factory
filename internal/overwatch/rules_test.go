package overwatch

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// learning writes one learning row of project, and returns its id L-<n>.
// ref is the landed_ref, and compared sets compared_at.
func (w *fakeWorld) learning(t *testing.T, project, status, target, ref string, compared bool) string {
	t.Helper()
	var n int64
	err := w.st.Write(context.Background(), func(tx *store.Tx) error {
		var at sql.NullInt64
		if compared {
			at = sql.NullInt64{Int64: tx.Now().UnixMilli(), Valid: true}
		}
		reason := ""
		if status == "escalated" || status == "answered" {
			reason = "size"
		}
		r, err := tx.Exec(`INSERT INTO learnings (project, retro_item, statement, cases, panel, reason, target, status, landed_ref, compared_at, created_at)
			VALUES (?, 'epic-1', 'Name the base in each merge.', '["run:r-1"]', '{}', ?, ?, ?, ?, ?, 0)`,
			project, reason, target, status, ref, at)
		if err != nil {
			return err
		}
		n, err = r.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return "L-" + itoa(n)
}

// ticket puts one ticket in status in project.
func (w *fakeWorld) ticket(project, id, status string) {
	w.add(project, machine.Item{ID: id, Kind: machine.KindTicket, Status: status}, "")
}

// sweepRules sweeps the rules part alone.
func sweepRules(t *testing.T, w World) PartReport {
	t.Helper()
	r := Sweep(context.Background(), w, []Part{rulesPart()})
	if len(r.Parts) != 1 || r.Parts[0].Name != "rules" {
		t.Fatalf("parts = %+v", r.Parts)
	}
	return r.Parts[0]
}

// keysOf returns the keys of each finding the rules part reads.
func keysOf(t *testing.T, w World) []string {
	t.Helper()
	reading, err := rulesPart().Read(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, f := range reading.Findings {
		keys = append(keys, f.Key)
	}
	return keys
}

// C25: the rules part reads one finding for each landed project_policy or
// userspace_skill learning whose ticket is done and that no comparison
// read. It reads none for a pack_pr or loop_fault learning, a ticket not
// yet done, a compared learning, or a learning that did not land.
func TestC25RulesPartListsEachUncomparedLandedRule(t *testing.T) {
	w := newFakeWorld(t)
	w.ticket("factorio", "factorio-t1", machine.Done)
	w.ticket("factorio", "factorio-t2", machine.Done)
	w.ticket("factorio", "factorio-t3", "in_progress")
	w.ticket("factorio", "factorio-t4", machine.Done)
	w.ticket("alpha", "alpha-t1", machine.Done)
	policy := w.learning(t, "factorio", "landed", "project_policy", "factorio-t1", false)
	w.learning(t, "factorio", "landed", "pack_pr", "factorio-t2", false)
	w.learning(t, "factorio", "landed", "project_policy", "factorio-t3", false)
	w.learning(t, "factorio", "landed", "userspace_skill", "factorio-t4", true)
	w.learning(t, "factorio", "landed", "loop_fault", "F-9", false)
	w.learning(t, "factorio", "held", "project_policy", "", false)
	w.learning(t, "factorio", "escalated", "userspace_skill", "", false)
	skill := w.learning(t, "alpha", "landed", "userspace_skill", "alpha-t1", false)

	want := []string{"overwatch:rules:factorio:" + policy, "overwatch:rules:alpha:" + skill}
	if got := keysOf(t, w); !slices.Equal(got, want) {
		t.Fatalf("the rules part reads %q, want %q", got, want)
	}
	pr := sweepRules(t, w)
	if pr.Code != Work || len(pr.Lines) != 2 {
		t.Fatalf("the rules part = %+v, want code 1 with two lines", pr)
	}
	for i, k := range want {
		if !slices.Contains(Tokens(pr.Lines[i]), k) {
			t.Errorf("line %d %q does not quote %s", i, pr.Lines[i], k)
		}
	}
}

// C25: an open fault that holds the finding key quiets the finding.
func TestC25AnOpenFaultQuietsTheRule(t *testing.T) {
	w := newFakeWorld(t)
	w.ticket("factorio", "factorio-t1", machine.Done)
	id := w.learning(t, "factorio", "landed", "project_policy", "factorio-t1", false)
	f := w.fault(t, "alpha and factorio hold one rule: overwatch:rules:factorio:"+id, "open")
	pr := sweepRules(t, w)
	if pr.Code != Quiet || len(pr.Lines) != 1 || !slices.Contains(Tokens(pr.Lines[0]), f) {
		t.Fatalf("the rules part = %+v, want code 0 and the finding filed in %s", pr, f)
	}
}

// compared calls `loop retro compared` as the coordinator.
func compared(t *testing.T, w *fakeWorld, id string, flags map[string]string) cli.Result {
	t.Helper()
	v, ok := cli.Surface().Verb("retro compared")
	if !ok {
		t.Fatal("no verb retro compared")
	}
	s := &retro.Service{World: retro.World{Store: w.st}}
	h, ok := s.Handlers()["retro compared"]
	if !ok {
		t.Fatal("no handler for retro compared")
	}
	return h(context.Background(), &rpc.Call{Inv: cli.Invocation{Verb: v, Args: []string{id}, Flags: flags, RequestID: "req-" + id},
		Principal: rpc.Principal{Kind: cli.PrincipalCoordinator}})
}

// C25, the ticket's done line: a sweep over a landed userspace learning
// whose ticket is done reads code 1 with its key, and code 0 after
// `loop retro compared`, with or without --fault.
func TestC25ASweepIsQuietAfterRetroCompared(t *testing.T) {
	for _, withFault := range []bool{false, true} {
		w := newFakeWorld(t)
		w.ticket("alpha", "alpha-t1", machine.Done)
		id := w.learning(t, "alpha", "landed", "userspace_skill", "alpha-t1", false)
		pr := sweepRules(t, w)
		if pr.Code != Work || len(pr.Lines) != 1 || !slices.Contains(Tokens(pr.Lines[0]), "overwatch:rules:alpha:"+id) {
			t.Fatalf("before compared, the rules part = %+v, want code 1 with overwatch:rules:alpha:%s", pr, id)
		}
		flags := map[string]string{}
		if withFault {
			flags["fault"] = w.fault(t, "a shared rule", "fixed")
		}
		if res := compared(t, w, id, flags); res.Exit != cli.ExitDone {
			t.Fatalf("retro compared %s %v = %+v", id, flags, res)
		}
		if pr := sweepRules(t, w); pr.Code != Quiet || len(pr.Lines) != 0 {
			t.Fatalf("after compared %v, the rules part = %+v, want code 0 and no line", flags, pr)
		}
	}
}
