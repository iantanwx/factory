package inbox

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

const prj = "alpha"

// archWorld is one store, fake beads, a machine over both, and the inbox
// the server builds over the machine.
type archWorld struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	m   *machine.Machine
	in  *Inbox
}

func newArchWorld(t *testing.T) *archWorld {
	t.Helper()
	ctx := context.Background()
	bx := fake.New("tk")
	if err := bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "loop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m, err := machine.New(machine.Config{Store: st, Beads: func(string) (beadsx.Client, error) { return bx, nil },
		Registry: cli.Surface(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	in := New(Config{Store: st, Machine: m, Projects: func() ([]config.Project, error) { return []config.Project{{ID: prj}}, nil }})
	return &archWorld{t: t, ctx: ctx, st: st, m: m, in: in}
}

func (w *archWorld) create(kind machine.Kind, verb string, facts map[string]string) string {
	w.t.Helper()
	id, res := w.m.Create(w.ctx, machine.CreateRequest{Project: prj, Kind: kind, Title: "item", Verb: verb,
		Actor: machine.Server("test"), Facts: facts})
	if res.Refusal != nil {
		w.t.Fatalf("create %s: %+v", kind, res.Refusal)
	}
	return id
}

func (w *archWorld) must(id, verb string, params map[string]string) {
	w.t.Helper()
	if res := w.m.Apply(w.ctx, machine.Request{Project: prj, Item: id, Verb: verb, Params: params, Actor: machine.Server("test")}); res.Refusal != nil {
		w.t.Fatalf("%s %s: %+v", verb, id, res.Refusal)
	}
}

func (w *archWorld) exec(query string, args ...any) {
	w.t.Helper()
	if err := w.st.Write(w.ctx, func(tx *store.Tx) error { _, err := tx.Exec(query, args...); return err }); err != nil {
		w.t.Fatal(err)
	}
}

// entries lists the architecture entries of the inbox now.
func (w *archWorld) entries() []Entry {
	w.t.Helper()
	es, err := w.in.Entries(w.ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []Entry
	for _, e := range es {
		if strings.HasPrefix(e.ID, "architecture/") {
			out = append(out, e)
		}
	}
	return out
}

// enteredArchitecture is when the epic entered architecture, as the
// transitions record it.
func (w *archWorld) enteredArchitecture(epic string) int64 {
	w.t.Helper()
	var at int64
	if err := w.st.Read(w.ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(w.ctx, `SELECT at FROM transitions WHERE item = ? AND to_status = ? ORDER BY seq DESC LIMIT 1`,
			epic, machine.Architecture).Scan(&at)
	}); err != nil {
		w.t.Fatal(err)
	}
	return at
}

// C15: an architecture epic with no ADR ticket waits on the coordinator.
// A ticket filed under it removes the entry, and the entry returns when
// that ticket is terminal while the epic stays in architecture. An epic
// in spec_needed makes no entry.
func TestArchitectureEpicWithNoADRTicketWaitsOnTheCoordinator(t *testing.T) {
	w := newArchWorld(t)
	ep := w.create(machine.KindEpic, machine.VerbCreateArchitecture, nil)
	landed := w.create(machine.KindEpic, machine.VerbCreateArchitecture, nil)
	w.must(landed, machine.VerbADRLanded, map[string]string{"path": "docs/adr/0001-x.md"})

	es := w.entries()
	if len(es) != 1 {
		t.Fatalf("entries = %+v, want one for %s", es, ep)
	}
	e := es[0]
	if e.ID != "architecture/alpha/"+ep || e.Kind != "architecture" || e.Awaits != AwaitsCoordinator || e.Project != prj ||
		e.Item != ep || e.Status != machine.Architecture {
		t.Fatalf("entry = %+v", e)
	}
	if want := w.enteredArchitecture(ep); want == 0 || e.At != want {
		t.Fatalf("at = %d, want %d, when the epic entered architecture", e.At, want)
	}
	if !strings.Contains(e.Next, "loop work create --project alpha --epic "+ep) || !strings.Contains(e.Next, "loop room open --kind architect --project alpha --item "+ep) {
		t.Fatalf("next = %q; it names the two acts that end the entry", e.Next)
	}

	adr := w.create(machine.KindTicket, "create", map[string]string{machine.FactEpic: ep})
	if es := w.entries(); len(es) != 0 {
		t.Fatalf("a ticket under the epic left %+v", es)
	}
	w.must(adr, machine.VerbEscalate, nil)
	w.must(adr, "cancel", map[string]string{"reason": "dropped"})
	if es := w.entries(); len(es) != 1 || es[0].ID != "architecture/alpha/"+ep {
		t.Fatalf("after the ticket was canceled: %+v", es)
	}
}

// C16: a room on the epic ends the entry, and its close brings it back. An
// answer refuses with USAGE, rule architecture_entry, and the entry stays.
func TestARoomEndsTheArchitectureEntryAndAnAnswerDoesNot(t *testing.T) {
	w := newArchWorld(t)
	ep := w.create(machine.KindEpic, machine.VerbCreateArchitecture, nil)
	id := "architecture/alpha/" + ep

	w.exec(`INSERT INTO rooms (id, project, item, kind, state, opened_at) VALUES ('room-a', ?, ?, 'architect', 'live', ?)`,
		prj, ep, time.Now().UnixMilli())
	if es := w.entries(); len(es) != 0 {
		t.Fatalf("an open room left %+v", es)
	}
	w.exec(`UPDATE rooms SET closed_at = ? WHERE id = 'room-a'`, time.Now().UnixMilli())
	if es := w.entries(); len(es) != 1 || es[0].ID != id {
		t.Fatalf("after the room closed: %+v", es)
	}

	inv, bad := cli.Surface().Validate("inbox answer", []string{id}, map[string]string{"body": "write the ADR"}, "req-1")
	if bad != nil {
		t.Fatalf("validate: %+v", bad)
	}
	res := w.in.answer(w.ctx, &rpc.Call{Inv: inv, Principal: rpc.Principal{Kind: cli.PrincipalCoordinator}})
	r := res.Refusal
	if r == nil || r.Code != cli.CodeUsage || r.Rule != "architecture_entry" {
		t.Fatalf("answer = %+v, want USAGE architecture_entry", res)
	}
	if !strings.Contains(r.Hint, "loop work create") || !strings.Contains(r.Hint, "loop room open") {
		t.Fatalf("hint = %q; it names the two acts that end the entry", r.Hint)
	}
	if es := w.entries(); len(es) != 1 || es[0].ID != id {
		t.Fatalf("after the answer: %+v", es)
	}
}
