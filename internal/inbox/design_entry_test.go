package inbox

import (
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
)

// designEntries lists the design entries of the inbox now.
func (w *archWorld) designEntries() []Entry {
	w.t.Helper()
	es, err := w.in.Entries(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []Entry
	for _, e := range es {
		if strings.HasPrefix(e.ID, "design/") {
			out = append(out, e)
		}
	}
	return out
}

// designEpic is an epic that reached design: its ADR landed, and its PRD
// holds a UI story.
func (w *archWorld) designEpic() string {
	w.t.Helper()
	ep := w.create(machine.KindEpic, machine.VerbCreateArchitecture, map[string]string{machine.FactUIStories: "US-01"})
	w.must(ep, machine.VerbADRLanded, map[string]string{"path": "docs/adr/0001-x.md", "added": "a1"})
	return ep
}

// C25 of spec factorio-a4yq: an epic in design with no open design room
// has an entry that waits on the coordinator and names the design room.
// An open room ends the entry, and its close brings it back. An answer
// refuses with rule design_entry, and the entry stays. An epic in any
// other state makes no design entry.
func TestC25EpicInDesignHasAnEntryThatNamesTheDesignRoom(t *testing.T) {
	w := newArchWorld(t)
	ep := w.designEpic()
	w.create(machine.KindEpic, machine.VerbCreateArchitecture, map[string]string{machine.FactUIStories: "US-01"})
	id := "design/alpha/" + ep

	es := w.designEntries()
	if len(es) != 1 {
		t.Fatalf("design entries = %+v, want one for %s", es, ep)
	}
	e := es[0]
	if e.ID != id || e.Kind != "design" || e.Awaits != AwaitsCoordinator || e.Item != ep || e.Status != machine.Design {
		t.Fatalf("entry = %+v", e)
	}
	if !strings.Contains(e.Next, "loop room open --kind design --item "+ep+" --project alpha") {
		t.Fatalf("next = %q; it names the design room", e.Next)
	}

	w.exec(`INSERT INTO rooms (id, project, item, kind, state, opened_at) VALUES ('room-d', ?, ?, 'design', 'live', ?)`,
		prj, ep, time.Now().UnixMilli())
	if es := w.designEntries(); len(es) != 0 {
		t.Fatalf("an open design room left %+v", es)
	}
	w.exec(`UPDATE rooms SET closed_at = ? WHERE id = 'room-d'`, time.Now().UnixMilli())
	if es := w.designEntries(); len(es) != 1 || es[0].ID != id {
		t.Fatalf("after the room closed: %+v", es)
	}

	inv, bad := cli.Surface().Validate("inbox answer", []string{id}, map[string]string{"body": "design it"}, "req-1")
	if bad != nil {
		t.Fatalf("validate: %+v", bad)
	}
	res := w.in.answer(w.ctx, &rpc.Call{Inv: inv, Principal: rpc.Principal{Kind: cli.PrincipalCoordinator}})
	r := res.Refusal
	if r == nil || r.Code != cli.CodeUsage || r.Rule != "design_entry" || !strings.Contains(r.Hint, "loop room open --kind design --item "+ep) {
		t.Fatalf("answer = %+v, want USAGE design_entry that names the design room", res)
	}
	if es := w.designEntries(); len(es) != 1 || es[0].ID != id {
		t.Fatalf("after the answer: %+v", es)
	}
}
