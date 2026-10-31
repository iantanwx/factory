package retro_test

import (
	"context"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

// coordinatorCompared calls `loop retro compared <id>` as the coordinator.
func (w *world) coordinatorCompared(id string, flags map[string]string) cli.Result {
	w.t.Helper()
	v, ok := cli.Surface().Verb("retro compared")
	if !ok {
		w.t.Fatal("no verb retro compared")
	}
	return w.service().Handlers()["retro compared"](context.Background(), &rpc.Call{
		Inv:       cli.Invocation{Verb: v, Args: []string{id}, Flags: flags, RequestID: "req-" + id},
		Principal: rpc.Principal{Kind: cli.PrincipalCoordinator}})
}

// C25: `loop retro compared` sets compared_at and the linked fault of a
// landed learning. It refuses a learning that did not land with
// not_landed, and a fault the server never gave out with fault_unknown.
// Neither refusal writes the learning.
func TestC25RetroComparedRecordsTheComparison(t *testing.T) {
	w := newWorld(t)
	w.exec(`INSERT INTO learnings (id, project, retro_item, statement, cases, panel, target, status, landed_ref, created_at)
		VALUES (1, 'factorio', 'epic-1', 'a rule', '["run:r-1"]', '{}', 'project_policy', 'landed', 'factorio-t1', 0),
		       (2, 'factorio', 'epic-1', 'a rule', '["run:r-1"]', '{}', 'project_policy', 'held', '', 0)`)
	w.exec(`INSERT INTO faults (n, text, reporter_kind, status, reported_at, updated_at) VALUES (3, 'one rule', 'coordinator', 'open', 0, 0)`)

	for _, c := range []struct {
		id, fault, rule string
	}{
		{"L-2", "", "not_landed"},
		{"L-9", "", "not_landed"},
		{"L-1", "F-4", "fault_unknown"},
	} {
		res := w.coordinatorCompared(c.id, map[string]string{"fault": c.fault})
		if res.Refusal == nil || res.Refusal.Rule != c.rule {
			t.Errorf("retro compared %s --fault %q = %+v, want the refusal %s", c.id, c.fault, res, c.rule)
		}
	}
	if n := w.count(`SELECT count(*) FROM learnings WHERE compared_at IS NOT NULL OR compared_fault <> ''`); n != 0 {
		t.Fatalf("a refused compared wrote %d learnings", n)
	}

	res := w.coordinatorCompared("L-1", map[string]string{"fault": "F-3"})
	if res.Refusal != nil {
		t.Fatalf("retro compared L-1 --fault F-3 = %+v", res.Refusal)
	}
	var got struct {
		ID         string `json:"id"`
		ComparedAt string `json:"compared_at"`
		Fault      string `json:"compared_fault"`
	}
	decode(t, res.Data, &got)
	if got.ID != "L-1" || got.ComparedAt == "" || got.Fault != "F-3" {
		t.Fatalf("retro compared answered %+v, want L-1 with its compared_at and F-3", got)
	}
	if n := w.count(`SELECT count(*) FROM learnings WHERE id = 1 AND compared_at IS NOT NULL AND compared_fault = 'F-3'`); n != 1 {
		t.Fatal("retro compared did not record the comparison of L-1")
	}
}
