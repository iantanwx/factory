package retro_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// sureReading clears both of the server's thresholds: agreement 0.95 and
// confidence 0.95.
func sureReading() *escalation.Reading {
	return &escalation.Reading{Probabilities: map[string]float64{"0": 0, "1": 0.05, "2": 0.15, "3": 0.8}, Score: 2.75, Confidence: 0.95}
}

func answer(judge string, t retro.Target, e retro.Evidence, c retro.Change) retro.Answer {
	return retro.Answer{Judge: judge, Target: t, Evidence: e, Change: c, Reason: judge + " read the record of " + string(t)}
}

func learning(statement string, cases ...retro.CaseRef) retro.Learning {
	return retro.Learning{Statement: statement, Cases: cases}
}

// landing is a panel that every route rule lets land at t.
func landing(l retro.Learning, t retro.Target, c retro.Change) retro.Panel {
	return retro.Panel{Learning: l, Answers: []retro.Answer{answer("fable-5", t, retro.TwoOrMore, c), answer("opus-5", t, retro.TwoOrMore, c)},
		Reading: sureReading()}
}

// holding is a panel with a judge who reads one case. Its judges name
// different targets and Jev did not score it, and it still holds.
func holding(l retro.Learning) retro.Panel {
	return retro.Panel{Learning: l, Answers: []retro.Answer{answer("fable-5", retro.ProjectPolicy, retro.One, retro.Edit),
		answer("opus-5", retro.LoopFault, retro.TwoOrMore, retro.NA)}}
}

// splitting is a panel whose judges name different targets.
func splitting(l retro.Learning) retro.Panel {
	return retro.Panel{Learning: l, Answers: []retro.Answer{answer("fable-5", retro.ProjectPolicy, retro.TwoOrMore, retro.Edit),
		answer("opus-5", retro.UserspaceSkill, retro.TwoOrMore, retro.Extend)}, Reading: sureReading()}
}

func panelFile(t *testing.T, panels ...retro.Panel) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"panels": append([]retro.Panel{}, panels...)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// retroOf queues a retro on a new epic of project with one run for each
// id, and binds it to runID. The runs are cases of the epic's record.
func (w *world) retroOf(project, runID string, runs ...string) string {
	w.t.Helper()
	ctx := context.Background()
	epic, res := w.m.Create(ctx, machine.CreateRequest{Project: project, Kind: machine.KindEpic, Title: "epic", Verb: "create_architecture",
		Actor: machine.Server("test")})
	if res.Refusal != nil {
		w.t.Fatalf("create epic: %+v", res.Refusal)
	}
	w.exec(`INSERT OR IGNORE INTO projects (id, repo, created_at, updated_at) VALUES (?, 'o/r', 0, 0)`, project)
	for i, r := range runs {
		w.exec(`INSERT INTO runs (run_id, project, stage, item, state, outcome_class, started_at, ended_at) VALUES (?, ?, 'implementer', ?, 'settled', 'failed', ?, ?)`,
			r, project, epic, i, i+1)
	}
	ev := machine.TerminalEvent{Project: project, Item: epic, Kind: machine.KindEpic, Status: machine.Done}
	if err := w.st.Write(ctx, func(tx *store.Tx) error { return retro.Enqueue(tx, ev) }); err != nil {
		w.t.Fatal(err)
	}
	if r, ok, err := retro.Bind(ctx, w.st, project, runID); err != nil || !ok || r.Item != epic {
		w.t.Fatalf("bind %s = %+v %v %v, want %s", runID, r, ok, err, epic)
	}
	return epic
}

func (w *world) service() *retro.Service {
	return &retro.Service{World: retro.World{Store: w.st, Machine: w.m}}
}

// retroCall calls a retro verb as a retro run of project.
func (w *world) retroCall(path, project, runID string, flags, extra map[string]string) cli.Result {
	w.t.Helper()
	v, ok := cli.Surface().Verb(path)
	if !ok {
		w.t.Fatalf("no verb %q", path)
	}
	h, ok := w.service().Handlers()[path]
	if !ok {
		w.t.Fatalf("no handler for %q", path)
	}
	inv := cli.Invocation{Verb: v, Flags: flags, Extra: extra, RequestID: "req-" + path}
	return h(context.Background(), &rpc.Call{Inv: inv,
		Principal: rpc.Principal{Kind: cli.PrincipalRun, RunID: runID, Project: project, Stage: retro.Stage}})
}

// route sends file with `loop retro route` as the retro run runID of
// project.
func (w *world) route(project, runID, file string) cli.Result {
	w.t.Helper()
	return w.retroCall("retro route", project, runID, map[string]string{"file": "panel.json"},
		map[string]string{cli.ExtraFile: base64.StdEncoding.EncodeToString([]byte(file))})
}

// routed is the answer of `loop retro route`.
type routed struct {
	Notice    string `json:"notice"`
	Learnings []struct {
		ID     string `json:"id"`
		Route  string `json:"route"`
		Target string `json:"target"`
		Reason string `json:"reason"`
		Entry  string `json:"entry"`
	} `json:"learnings"`
}

func (w *world) mustRoute(project, runID, file string) routed {
	w.t.Helper()
	res := w.route(project, runID, file)
	if res.Refusal != nil || res.Exit != cli.ExitDone {
		w.t.Fatalf("route = %+v", res)
	}
	var out routed
	decode(w.t, res.Data, &out)
	return out
}

func decode(t *testing.T, data, into any) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
}

func (w *world) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query, args...).Scan(&n) }); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (w *world) retroState(project, item string) string {
	w.t.Helper()
	var s string
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT state FROM retros WHERE project = ? AND item = ?`, project, item).Scan(&s)
	}); err != nil {
		w.t.Fatal(err)
	}
	return s
}

// created counts the tickets and bugs of project, and the loop faults.
func (w *world) created(project string) int {
	w.t.Helper()
	n := w.count(`SELECT count(*) FROM faults`)
	for _, k := range []machine.Kind{machine.KindTicket, machine.KindBug} {
		items, ref := w.m.List(context.Background(), project, machine.ListFilter{Kind: k})
		if ref != nil {
			w.t.Fatalf("list %s: %+v", k, ref)
		}
		n += len(items)
	}
	return n
}

// held is one learning as `loop retro held` and work show print it.
type held struct {
	ID        string   `json:"id"`
	RetroItem string   `json:"retro_item"`
	Statement string   `json:"statement"`
	Cases     []string `json:"cases"`
}

func (w *world) held(project, runID string) []held {
	w.t.Helper()
	res := w.retroCall("retro held", project, runID, map[string]string{}, map[string]string{})
	if res.Refusal != nil {
		w.t.Fatalf("held = %+v", res)
	}
	var out []held
	decode(w.t, res.Data, &out)
	return out
}

// workShowHeld is the held_learnings list of `loop work show item`.
func (w *world) workShowHeld(item string) []held {
	w.t.Helper()
	w.m.SetRetroReader(func(ctx context.Context, project, item string) (machine.RetroOnItem, error) {
		return retro.OnItem(ctx, w.st, project, item)
	})
	v, _ := cli.Surface().Verb("work show")
	res := w.m.Handlers()["work show"](context.Background(), &rpc.Call{
		Inv:       cli.Invocation{Verb: v, Args: []string{item}, Flags: map[string]string{}, Extra: map[string]string{}},
		Principal: rpc.Principal{Kind: cli.PrincipalCoordinator, RunID: "coord", Project: prj}})
	if res.Refusal != nil {
		w.t.Fatalf("work show %s = %+v", item, res.Refusal)
	}
	var out struct {
		Held []held `json:"held_learnings"`
	}
	decode(w.t, res.Data, &out)
	return out.Held
}

// C10: a learning that names a case outside the bound retro's record is
// refused, and the refusal names the case. A case of the record of another
// item is outside it, and so is a case of the retro's record that the
// extended learning does not hold when the case belongs to another retro.
// Nothing is written, and the retro stays running.
func TestC10RouteRefusesACaseOutsideTheRecord(t *testing.T) {
	w := newWorld(t)
	first := w.retroOf(prj, "r-first", "r-a1", "r-a2")
	w.mustRoute(prj, "r-first", panelFile(t, holding(learning("name the base", "run:r-a1"))))
	epic := w.retroOf(prj, "r-retro", "r-good")
	learnings, notices := w.count(`SELECT count(*) FROM learnings`), w.count(`SELECT count(*) FROM notices`)
	for name, tc := range map[string]struct {
		panel   retro.Panel
		foreign string
	}{
		"a case of no record": {landing(learning("check the gate", "run:r-good", "run:r-elsewhere"), retro.ProjectPolicy, retro.Edit), "run:r-elsewhere"},
		"a case of another retro's record that the held learning lacks": {landing(retro.Learning{Statement: "name the base",
			Cases: []retro.CaseRef{"run:r-good", "run:r-a1", "run:r-a2"}, Extends: "L-1"}, retro.ProjectPolicy, retro.Edit), "run:r-a2"},
	} {
		t.Run(name, func(t *testing.T) {
			res := w.route(prj, "r-retro", panelFile(t, landing(learning("a good learning", "run:r-good"), retro.LoopFault, retro.NA), tc.panel))
			if res.Refusal == nil || res.Refusal.Rule != "case_not_in_record" || !strings.Contains(res.Refusal.Hint, tc.foreign) {
				t.Fatalf("route = %+v, want case_not_in_record naming %s", res, tc.foreign)
			}
			if got := w.count(`SELECT count(*) FROM learnings`); got != learnings {
				t.Fatalf("%d learnings after the refusal, want %d", got, learnings)
			}
			if got := w.count(`SELECT count(*) FROM notices`); got != notices {
				t.Fatalf("%d notices after the refusal, want %d", got, notices)
			}
			if got := w.retroState(prj, epic); got != retro.StateRunning {
				t.Fatalf("the retro is %s after the refusal, want running", got)
			}
			if got := w.held(prj, "r-retro"); len(got) != 1 || got[0].RetroItem != first {
				t.Fatalf("held = %+v, want L-1 of %s still held", got, first)
			}
		})
	}
}

// C11: a panel in which a judge answers one holds, whatever its targets,
// sizes and reading. The learning lands nothing, `loop retro held` lists it
// with its cases, and `loop work show` of the retro's item lists it under
// held_learnings until a later learning extends it.
func TestC11ASingleCaseIsHeld(t *testing.T) {
	w := newWorld(t)
	epic := w.retroOf(prj, "r-1", "r-a", "r-b")
	before := w.created(prj)
	got := w.mustRoute(prj, "r-1", panelFile(t, holding(learning("run the gates before the push", "run:r-a", "run:r-b"))))
	if len(got.Learnings) != 1 || got.Learnings[0].Route != "hold" || got.Learnings[0].ID != "L-1" {
		t.Fatalf("route = %+v, want L-1 held", got)
	}
	if n := w.created(prj); n != before {
		t.Fatalf("a held learning created %d tickets, bugs or faults", n-before)
	}
	if n := w.count(`SELECT count(*) FROM learnings WHERE status = 'held'`); n != 1 {
		t.Fatalf("%d held learnings stored, want 1", n)
	}
	want := []held{{ID: "L-1", RetroItem: epic, Statement: "run the gates before the push", Cases: []string{"run:r-a", "run:r-b"}}}
	if h := w.held(prj, "r-1"); !slices.EqualFunc(h, want, heldEqual) {
		t.Fatalf("retro held = %+v, want %+v", h, want)
	}
	if h := w.workShowHeld(epic); !slices.EqualFunc(h, want, heldEqual) {
		t.Fatalf("work show held_learnings = %+v, want %+v", h, want)
	}

	next := w.retroOf(prj, "r-2", "r-c")
	w.mustRoute(prj, "r-2", panelFile(t, landing(retro.Learning{Statement: "run the gates before the push",
		Cases: []retro.CaseRef{"run:r-a", "run:r-c"}, Extends: "L-1"}, retro.ProjectPolicy, retro.Edit)))
	if h := w.workShowHeld(epic); len(h) != 0 {
		t.Fatalf("work show of %s lists %+v after a later learning extended L-1", epic, h)
	}
	if h := w.workShowHeld(next); len(h) != 0 {
		t.Fatalf("work show of %s lists %+v, and its learning landed", next, h)
	}
}

func heldEqual(a, b held) bool {
	return a.ID == b.ID && a.RetroItem == b.RetroItem && a.Statement == b.Statement && slices.Equal(a.Cases, b.Cases)
}

// C12: a learning that extends held L-1 is stored with L-1's cases and its
// own, and L-1 moves to counted. An extends that names a learning not held
// is refused with extends_not_held.
func TestC12ALaterCaseCountsBoth(t *testing.T) {
	w := newWorld(t)
	w.retroOf(prj, "r-1", "r-a")
	w.mustRoute(prj, "r-1", panelFile(t, holding(learning("pin the base", "run:r-a"))))
	w.retroOf(prj, "r-2", "r-b")
	got := w.mustRoute(prj, "r-2", panelFile(t, landing(retro.Learning{Statement: "pin the base again",
		Cases: []retro.CaseRef{"run:r-b"}, Extends: "L-1"}, retro.ProjectPolicy, retro.Edit)))
	if len(got.Learnings) != 1 || got.Learnings[0].ID != "L-2" || got.Learnings[0].Route != "land" {
		t.Fatalf("route = %+v, want L-2 landed", got)
	}
	var status, cases string
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		if err := tx.QueryRow(`SELECT status FROM learnings WHERE id = 1`).Scan(&status); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT cases FROM learnings WHERE id = 2`).Scan(&cases)
	}); err != nil {
		t.Fatal(err)
	}
	if status != "counted" {
		t.Fatalf("L-1 is %s, want counted", status)
	}
	var cs []string
	if err := json.Unmarshal([]byte(cases), &cs); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cs, []string{"run:r-a", "run:r-b"}) {
		t.Fatalf("L-2 holds cases %v, want L-1's and its own", cs)
	}
	if h := w.held(prj, "r-2"); len(h) != 0 {
		t.Fatalf("held = %+v after L-2 counted L-1", h)
	}

	epic := w.retroOf(prj, "r-3", "r-c")
	for _, ext := range []string{"L-1", "L-2", "L-9"} {
		res := w.route(prj, "r-3", panelFile(t, holding(retro.Learning{Statement: "again", Cases: []retro.CaseRef{"run:r-c"}, Extends: ext})))
		if res.Refusal == nil || res.Refusal.Rule != "extends_not_held" {
			t.Fatalf("extends %s = %+v, want extends_not_held", ext, res)
		}
	}
	if n := w.count(`SELECT count(*) FROM learnings`); n != 2 {
		t.Fatalf("%d learnings after the refusals, want 2", n)
	}
	if s := w.retroState(prj, epic); s != retro.StateRunning {
		t.Fatalf("the retro is %s after the refusals, want running", s)
	}
}

// C18: a pack PR learning that every other rule lands escalates with
// reason pack_outside in a project whose repository is not the loop's, and
// creates no ticket in either project. In the loop's own project the same
// panel lands.
func TestC18NoPackPROutsideTheLoopsProject(t *testing.T) {
	w := newWorld(t)
	const home = "home"
	bxs := map[string]*fake.Client{prj: fake.New("al"), home: fake.New("hm")}
	for _, bx := range bxs {
		if err := bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
			t.Fatal(err)
		}
	}
	m, err := machine.New(machine.Config{Store: w.st, Beads: func(p string) (beadsx.Client, error) { return bxs[p], nil },
		Terminal: retro.Enqueue, Registry: cli.Surface(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	w.m = m
	w.exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES (?, ?, 0, 0)`, home, strings.ToUpper(release.ReleaseRepo))
	tickets := func() int { return w.created(prj) + w.created(home) - w.count(`SELECT count(*) FROM faults`) }

	w.retroOf(prj, "r-1", "r-a")
	got := w.mustRoute(prj, "r-1", panelFile(t, landing(learning("say it in the pack", "run:r-a"), retro.PackPR, retro.Edit)))
	if len(got.Learnings) != 1 || got.Learnings[0].Route != "escalate" || got.Learnings[0].Reason != "pack_outside" {
		t.Fatalf("route outside the loop's project = %+v, want escalate pack_outside", got)
	}
	if n := tickets(); n != 0 {
		t.Fatalf("%d tickets or bugs in both projects, want none", n)
	}

	w.retroOf(home, "r-2", "r-b")
	got = w.mustRoute(home, "r-2", panelFile(t, landing(learning("say it in the pack", "run:r-b"), retro.PackPR, retro.Edit)))
	if len(got.Learnings) != 1 || got.Learnings[0].Route != "land" || got.Learnings[0].Target != "pack_pr" {
		t.Fatalf("route in the loop's project = %+v, want land at pack_pr", got)
	}
	var ref string
	if err := w.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT coalesce(landed_ref, '') FROM learnings WHERE project = ? AND status = 'landed'`, home).Scan(&ref)
	}); err != nil {
		t.Fatal(err)
	}
	if ref != "" {
		t.Fatalf("the landed learning names %q; this route creates nothing", ref)
	}
	if n := tickets(); n != 0 {
		t.Fatalf("%d tickets or bugs in both projects, want none", n)
	}
}

// C19: each retro that reaches done holds one notice with source retro and
// ref <project>/<item>, also after a repeated route and with no learning.
// It awaits a person, its body asks nothing, and it lists each landed
// learning with its target and each held one.
func TestC19OneNoticePerRetroAndItAsksNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		panels []retro.Panel
		says   []string
	}{
		"no learning": {nil, []string{"no learning"}},
		"one held and one landed": {[]retro.Panel{
			holding(learning("why did the gate pass? name it", "run:r-a")),
			landing(learning("does the policy say so? it should", "run:r-a", "run:r-b"), retro.ProjectPolicy, retro.Edit),
		}, []string{"L-1", "L-2", "project_policy"}},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			epic := w.retroOf(prj, "r-1", "r-a", "r-b")
			file := panelFile(t, tc.panels...)
			got := w.mustRoute(prj, "r-1", file)
			if w.retroState(prj, epic) != "done" {
				t.Fatalf("the retro is %s after its route, want done", w.retroState(prj, epic))
			}
			for _, again := range []string{file, panelFile(t, landing(learning("another", "run:r-b"), retro.LoopFault, retro.NA))} {
				if res := w.route(prj, "r-1", again); res.Refusal == nil || res.Refusal.Rule != "retro_done" {
					t.Fatalf("second route = %+v, want retro_done", res)
				}
			}
			ref := prj + "/" + epic
			if n := w.count(`SELECT count(*) FROM notices WHERE source = 'retro' AND ref = ?`, ref); n != 1 {
				t.Fatalf("%d notices keyed retro %s, want 1", n, ref)
			}
			if n := w.count(`SELECT count(*) FROM notices`); n != 1 {
				t.Fatalf("%d notices, want 1", n)
			}
			es, err := inbox.New(inbox.Config{Store: w.st}).Entries(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var notice *inbox.Entry
			for i := range es {
				if es[i].ID == got.Notice {
					notice = &es[i]
				}
			}
			if notice == nil || notice.Kind != inbox.KindNotice || notice.Awaits != inbox.AwaitsPerson || notice.Project != prj {
				t.Fatalf("notice %q in the inbox = %+v", got.Notice, notice)
			}
			text := notice.Title + "\n" + notice.Text
			if strings.Contains(text, "?") {
				t.Fatalf("the notice asks something:\n%s", text)
			}
			for _, s := range append([]string{epic}, tc.says...) {
				if !strings.Contains(text, s) {
					t.Fatalf("the notice does not say %q:\n%s", s, text)
				}
			}
		})
	}
}

// C20: each escalated learning puts one learning entry that awaits a
// person. Its body holds the learning, its cases, each judge's target,
// evidence, change and reason, and Jev's agreement and confidence, or the
// words "no reading". The notice names each entry.
func TestC20AnEscalationHoldsBothJudgesAndJev(t *testing.T) {
	w := newWorld(t)
	epic := w.retroOf(prj, "r-1", "r-a", "r-b")
	unscored := landing(learning("file the fault with its run", "run:r-b"), retro.LoopFault, retro.NA)
	unscored.Reading = nil
	got := w.mustRoute(prj, "r-1", panelFile(t,
		splitting(learning("split the spec before the PRD", "run:r-a", "run:r-b")),
		unscored,
		landing(learning("lands with no person", "run:r-a"), retro.ProjectPolicy, retro.Edit)))
	es, err := inbox.New(inbox.Config{Store: w.st}).Entries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var entries []inbox.Entry
	var notice inbox.Entry
	for _, e := range es {
		switch e.Kind {
		case inbox.KindLearning:
			entries = append(entries, e)
		case inbox.KindNotice:
			notice = e
		}
	}
	if len(entries) != 2 {
		t.Fatalf("%d learning entries, want one for each of the 2 escalated learnings: %+v", len(entries), es)
	}
	wants := [][]string{
		{"split the spec before the PRD", "run:r-a", "run:r-b", "fable-5", "opus-5", "project_policy", "userspace_skill", "two_or_more",
			"edit", "extend", "fable-5 read the record of project_policy", "opus-5 read the record of userspace_skill", "agreement 0.95",
			"confidence 0.95"},
		{"file the fault with its run", "run:r-b", "fable-5", "opus-5", "loop_fault", "two_or_more", "n/a",
			"fable-5 read the record of loop_fault", "no reading"},
	}
	for i, e := range entries {
		if e.Awaits != inbox.AwaitsPerson || e.Project != prj || e.Item != epic || e.ID != got.Learnings[i].Entry {
			t.Fatalf("entry %d = %+v, want one that awaits a person on %s, named %s", i, e, epic, got.Learnings[i].Entry)
		}
		for _, s := range wants[i] {
			if !strings.Contains(e.Text, s) {
				t.Fatalf("entry %s does not hold %q:\n%s", e.ID, s, e.Text)
			}
		}
		if !strings.Contains(notice.Text, e.ID) {
			t.Fatalf("the notice does not name entry %s:\n%s", e.ID, notice.Text)
		}
	}

}

// C29: a route by a run that holds no retro is refused with
// retro_not_bound, and a second route of a done retro with retro_done.
// Neither writes a learning, a notice or an item.
func TestC29OneRouteForEachRetro(t *testing.T) {
	w := newWorld(t)
	epic := w.retroOf(prj, "r-1", "r-a")
	file := panelFile(t, landing(learning("lands", "run:r-a"), retro.ProjectPolicy, retro.Edit))
	before := w.created(prj)
	if res := w.route(prj, "r-unbound", file); res.Refusal == nil || res.Refusal.Rule != "retro_not_bound" {
		t.Fatalf("route by an unbound run = %+v, want retro_not_bound", res)
	}
	if n := w.count(`SELECT count(*) FROM learnings`) + w.count(`SELECT count(*) FROM notices`); n != 0 {
		t.Fatalf("the unbound route wrote %d learnings and notices", n)
	}
	if s := w.retroState(prj, epic); s != retro.StateRunning {
		t.Fatalf("the retro is %s after an unbound route, want running", s)
	}
	w.mustRoute(prj, "r-1", file)
	for _, f := range []string{panelFile(t), panelFile(t, holding(learning("held", "run:r-a")))} {
		if res := w.route(prj, "r-1", f); res.Refusal == nil || res.Refusal.Rule != "retro_done" {
			t.Fatalf("second route = %+v, want retro_done", res)
		}
	}
	if l, n := w.count(`SELECT count(*) FROM learnings`), w.count(`SELECT count(*) FROM notices`); l != 1 || n != 1 {
		t.Fatalf("%d learnings and %d notices after the refused routes, want 1 and 1", l, n)
	}
	if n := w.created(prj); n != before {
		t.Fatalf("the routes created %d items", n-before)
	}
}

// The loop's own project is the one whose repository is the release
// repository, as the bugs part of overwatch reads it.
func TestLoopRepoIsTheReleaseRepo(t *testing.T) {
	if retro.LoopRepo != release.ReleaseRepo {
		t.Fatalf("retro.LoopRepo = %s, release.ReleaseRepo = %s", retro.LoopRepo, release.ReleaseRepo)
	}
}
