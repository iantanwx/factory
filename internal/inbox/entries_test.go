package inbox

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/admission"
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
	w.must(landed, machine.VerbADRLanded, map[string]string{"path": "docs/adr/0001-x.md", "added": "a1"})

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

// C18 of factorio-ku99: a PRD epic that the operator sent to the ADR
// stage makes the same architecture entry. Its ticket cites a story of
// the PRD, so the entry and the refused answer name --story.
func TestAPRDEpicsArchitectureEntryNamesAStory(t *testing.T) {
	w := newArchWorld(t)
	ep := w.create(machine.KindEpic, machine.VerbCreateArchitecture, map[string]string{machine.FactPRDPath: "docs/prd/widgets.md"})
	id := "architecture/alpha/" + ep
	story := "loop work create --project alpha --epic " + ep + " --story <a story label of its PRD> --request"

	es := w.entries()
	if len(es) != 1 || es[0].ID != id || es[0].Awaits != AwaitsCoordinator {
		t.Fatalf("entries = %+v, want one for %s", es, ep)
	}
	if !strings.Contains(es[0].Next, story) || !strings.Contains(es[0].Next, "loop room open --kind architect --project alpha --item "+ep) {
		t.Fatalf("next = %q; it names the ticket with a story, and the room", es[0].Next)
	}

	inv, bad := cli.Surface().Validate("inbox answer", []string{id}, map[string]string{"body": "write the ADR"}, "req-1")
	if bad != nil {
		t.Fatalf("validate: %+v", bad)
	}
	res := w.in.answer(w.ctx, &rpc.Call{Inv: inv, Principal: rpc.Principal{Kind: cli.PrincipalCoordinator}})
	if r := res.Refusal; r == nil || r.Rule != "architecture_entry" || !strings.Contains(r.Hint, story) {
		t.Fatalf("answer = %+v; its hint names the ticket with a story", res)
	}
}

// C10: each pause is one entry that awaits the coordinator. It quotes the
// reason, is dated at paused_at, and names its `stage resume` command. An
// answer refuses with USAGE, rule pause_entry, and the entry stays. A
// defer hides it, and a resume removes it.
func TestEachPauseIsOneInboxEntryThatAwaitsTheCoordinator(t *testing.T) {
	w := newArchWorld(t)
	pauses := func() map[string]Entry {
		t.Helper()
		es, err := w.in.Entries(w.ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]Entry{}
		for _, e := range es {
			if strings.HasPrefix(e.ID, "pause/") {
				out[e.ID] = e
			}
		}
		return out
	}
	every, err := admission.Pause(w.ctx, w.st, admission.StagePause{Stage: "implementer", Reason: "the CI runners are down", By: "coordinator:r-1"})
	if err != nil {
		t.Fatal(err)
	}
	one, err := admission.Pause(w.ctx, w.st, admission.StagePause{Project: prj, Stage: "code-reviewer", Reason: "a review gate misfires", By: "coordinator:r-1"})
	if err != nil {
		t.Fatal(err)
	}

	es := pauses()
	if len(es) != 2 {
		t.Fatalf("pause entries = %+v, want two", es)
	}
	for _, tc := range []struct {
		id, project, reason, next string
		at                        int64
	}{
		{"pause/implementer", "", "the CI runners are down", "loop stage resume implementer", every.At.UnixMilli()},
		{"pause/alpha/code-reviewer", prj, "a review gate misfires", "loop stage resume code-reviewer --project alpha", one.At.UnixMilli()},
	} {
		e, ok := es[tc.id]
		if !ok {
			t.Fatalf("no entry %s in %+v", tc.id, es)
		}
		if e.Kind != "pause" || e.Awaits != AwaitsCoordinator || e.Project != tc.project {
			t.Errorf("%s = %+v", tc.id, e)
		}
		if !strings.Contains(e.Text, tc.reason) {
			t.Errorf("%s text = %q; it quotes the reason", tc.id, e.Text)
		}
		if e.At == 0 || e.At != tc.at {
			t.Errorf("%s at = %d, want paused_at %d", tc.id, e.At, tc.at)
		}
		if e.Next != tc.next {
			t.Errorf("%s next = %q, want %q", tc.id, e.Next, tc.next)
		}
	}

	call := func(verb string, args []string, flags map[string]string) cli.Result {
		t.Helper()
		inv, bad := cli.Surface().Validate(verb, args, flags, "req-1")
		if bad != nil {
			t.Fatalf("validate %s: %+v", verb, bad)
		}
		c := &rpc.Call{Inv: inv, Principal: rpc.Principal{Kind: cli.PrincipalCoordinator}}
		if verb == "inbox answer" {
			return w.in.answer(w.ctx, c)
		}
		return w.in.deferVerb(w.ctx, c)
	}
	res := call("inbox answer", []string{"pause/alpha/code-reviewer"}, map[string]string{"body": "resume it"})
	if r := res.Refusal; r == nil || r.Code != cli.CodeUsage || r.Rule != "pause_entry" ||
		!strings.Contains(r.Hint, "loop stage resume code-reviewer --project alpha") {
		t.Fatalf("answer = %+v, want USAGE pause_entry with a hint that names loop stage resume", res)
	}
	if _, ok := pauses()["pause/alpha/code-reviewer"]; !ok {
		t.Fatal("the answer removed the entry")
	}

	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if res := call("inbox defer", []string{"pause/implementer"}, map[string]string{"until": until, "reason": "the runners return at noon"}); res.Refusal != nil {
		t.Fatalf("defer = %+v", res)
	}
	if es := pauses(); len(es) != 1 {
		t.Fatalf("after the defer: %+v, want pause/alpha/code-reviewer only", es)
	}

	if _, err := admission.Resume(w.ctx, w.st, prj, "code-reviewer", "coordinator:r-1"); err != nil {
		t.Fatal(err)
	}
	if es := pauses(); len(es) != 0 {
		t.Fatalf("after the resume: %+v", es)
	}
}

// C18: a notice is an inbox entry that asks nothing. A put notice lists
// with kind notice, awaits a person, shows its subject and body, and its
// next holds no question. A defer refuses, and an answer dismisses it. No
// item, run or decision changes state when it is put or dismissed.
func TestANoticeIsAnInboxEntryThatAsksNothing(t *testing.T) {
	w := newArchWorld(t)
	tk := w.create(machine.KindTicket, "create", nil)
	state := func() string {
		t.Helper()
		var s string
		if err := w.st.Read(w.ctx, func(tx *sql.Tx) error {
			return tx.QueryRowContext(w.ctx, `SELECT (SELECT count(*) FROM transitions) || '/' || (SELECT count(*) FROM runs) || '/' ||
				(SELECT count(*) FROM intents)`).Scan(&s)
		}); err != nil {
			t.Fatal(err)
		}
		items, ref := w.m.List(w.ctx, prj, machine.ListFilter{})
		if ref != nil {
			t.Fatalf("list: %+v", ref)
		}
		for _, it := range items {
			s += " " + it.ID + "=" + it.Status
		}
		return s
	}
	before := state()

	var row int64
	if err := w.st.Write(w.ctx, func(tx *store.Tx) (err error) {
		row, err = PutNotice(tx, Notice{Project: prj, Subject: "alpha: stages.implementer.cap 8 (host) to 3",
			Body: "The cap is 3 from project.", Source: "settings_change", Ref: "7"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.st.Write(w.ctx, func(tx *store.Tx) error {
		again, err := PutNotice(tx, Notice{Project: prj, Subject: "a repeat", Body: "b", Source: "settings_change", Ref: "7"})
		if err == nil && again != row {
			t.Errorf("a repeated put = row %d, want %d", again, row)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if after := state(); after != before {
		t.Fatalf("the put changed state: %s, want %s (%s)", after, before, tk)
	}

	call := func(verb string, args []string, flags map[string]string) cli.Result {
		t.Helper()
		inv, bad := cli.Surface().Validate(verb, args, flags, "req-"+verb)
		if bad != nil {
			t.Fatalf("validate %s: %+v", verb, bad)
		}
		c := &rpc.Call{Inv: inv, Principal: rpc.Principal{Kind: cli.PrincipalCoordinator}}
		switch verb {
		case "inbox list":
			return w.in.list(w.ctx, c)
		case "inbox answer":
			return w.in.answer(w.ctx, c)
		}
		return w.in.deferVerb(w.ctx, c)
	}
	notices := func() []Entry {
		t.Helper()
		res := call("inbox list", nil, map[string]string{"awaits": "person"})
		if res.Refusal != nil {
			t.Fatalf("list = %+v", res)
		}
		var out []Entry
		listed, _ := res.Data.([]Entry)
		for _, e := range listed {
			if e.Kind == "notice" {
				out = append(out, e)
			}
		}
		return out
	}
	id := "notice/" + strconv.FormatInt(row, 10)
	es := notices()
	if len(es) != 1 {
		t.Fatalf("notice entries = %+v, want one", es)
	}
	e := es[0]
	if e.ID != id || e.Awaits != "person" || e.Project != prj || e.Title != "alpha: stages.implementer.cap 8 (host) to 3" ||
		e.Text != "The cap is 3 from project." || e.At == 0 {
		t.Fatalf("entry = %+v", e)
	}
	if want := "tell the operator; `loop inbox answer " + id + " --body <anything>` dismisses it"; e.Next != want {
		t.Fatalf("next = %q, want %q", e.Next, want)
	}
	if strings.Contains(e.Next, "?") || strings.Contains(e.Next, "the operator's answer") {
		t.Fatalf("next = %q asks a question", e.Next)
	}

	until := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	res := call("inbox defer", []string{id}, map[string]string{"until": until, "reason": "later"})
	if r := res.Refusal; r == nil || r.Code != cli.CodeTransitionNotAllowed || r.Rule != "notice_defer" {
		t.Fatalf("defer = %+v, want TRANSITION_NOT_ALLOWED notice_defer", res)
	}
	if es := notices(); len(es) != 1 {
		t.Fatalf("after the defer: %+v", es)
	}

	if res := call("inbox answer", []string{id}, map[string]string{"body": "told"}); res.Refusal != nil {
		t.Fatalf("answer = %+v", res)
	}
	if es := notices(); len(es) != 0 {
		t.Fatalf("after the answer: %+v", es)
	}
	if after := state(); after != before {
		t.Fatalf("the dismissal changed state: %s, want %s", after, before)
	}
	res = call("inbox answer", []string{id}, map[string]string{"body": "again"})
	if r := res.Refusal; r == nil || r.Code != cli.CodeNotFound || r.Rule != "no_notice" {
		t.Fatalf("second answer = %+v, want NOT_FOUND no_notice", res)
	}
}
