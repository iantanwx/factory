package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// testClock is the inbox's and the timers' clock in these tests.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// humanWorld is a server with fake beads, a fake agent host whose
// checkouts are git worktrees, a coordinator session and one project.
type humanWorld struct {
	t     *testing.T
	dir   string
	bx    *fake.Client
	host  *agentfake.Host
	srv   *Server
	run   *running
	opts  Options
	clock *testClock
	coord string // the coordinator's checkout
}

const humanSecret = "hv-7f3c1e9a2b5d4c8e6f0a"

func newHumanWorld(t *testing.T, manifest string) *humanWorld {
	t.Helper()
	repo := projectRepo(t, "stages:\n  code-reviewer: {}\n  implementer: {}\nmerge:\n  mechanism: direct\n")
	pc, err := config.ParseProjectConfig([]byte("stages:\n  code-reviewer: {}\n  implementer: {}\nmerge:\n  mechanism: direct\n"))
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.ProjectSnapshot{Config: pc, Manifest: &config.Manifest{}}
	if manifest != "" {
		if snap.Manifest, err = config.ParseManifest([]byte(manifest)); err != nil {
			t.Fatal(err)
		}
	}
	w := &humanWorld{t: t, dir: instanceDir(t), bx: fake.New("tk"), clock: &testClock{now: time.Now()},
		host: agentfake.New(filepath.Join(t.TempDir(), "checkouts"), agentfake.WithQuietPeriod(time.Minute))}
	if err := w.bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	w.opts = Options{
		Beads:  func(string) (beadsx.Client, error) { return w.bx, nil },
		Agents: w.host,
		Sched: SchedOptions{
			Checkouts:     func(string) sched.CheckoutHost { return worktrees{t: t, host: w.host, repo: repo} },
			ProjectConfig: func(context.Context, string) (*config.ProjectSnapshot, error) { return snap, nil },
			Coordinator:   &sched.CoordinatorConfig{Project: "alpha", Harness: agenthost.Claude},
			TickInterval:  time.Hour,
		},
		Human: HumanOptions{Now: w.clock.Now, PollInterval: 20 * time.Millisecond, WatchInterval: time.Hour},
	}
	w.start()
	if err := w.srv.Projects.Add(context.Background(), config.Project{ID: "alpha", Repo: "iantanwx/alpha", Path: repo}); err != nil {
		t.Fatal(err)
	}
	w.coord = t.TempDir()
	if _, err := w.srv.Tokens.IssueToCheckout(context.Background(), rpc.IssueRequest{RunID: "coord-1", Kind: cli.PrincipalCoordinator,
		Checkout: w.coord}); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *humanWorld) start() {
	opts := w.opts
	opts.Setup = append(append([]func(*Server) error{}, opts.Setup...), func(s *Server) error { w.srv = s; return nil })
	w.run = start(w.t, w.dir, opts, true)
	// The scheduler's first tick starts the coordinator session. Wait for
	// it, so no test races that tick.
	deadline := time.Now().Add(10 * time.Second)
	for w.srv.Sched.Coordinator().Session == nil {
		if time.Now().After(deadline) {
			w.t.Fatal("the coordinator session did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// restart stops the server and starts another on the same instance.
func (w *humanWorld) restart() {
	w.t.Helper()
	w.run.cancel()
	err := <-w.run.done
	w.run.done <- err
	if err != nil {
		w.t.Fatalf("stop: %v", err)
	}
	w.start()
}

// as calls the CLI from inside checkout, with the token found there.
func (w *humanWorld) as(checkout string, args ...string) (map[string]any, int) {
	w.t.Helper()
	w.t.Chdir(checkout)
	r := cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return checkout, nil }}).Attach(r)
	var out bytes.Buffer
	code := r.Run(context.Background(), args, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		w.t.Fatalf("loop %v printed no envelope: %v\n%s", args, err, out.String())
	}
	if strings.Contains(out.String(), humanSecret) {
		w.t.Fatalf("loop %v printed the secret value: %s", args, out.String())
	}
	return env, code
}

func (w *humanWorld) coordinator(args ...string) (map[string]any, int) { return w.as(w.coord, args...) }

// runToken issues a run token bound to item and stage, in a new checkout.
func (w *humanWorld) runToken(id, item, stage string) string {
	w.t.Helper()
	dir := w.t.TempDir()
	if _, err := w.srv.Tokens.IssueToCheckout(context.Background(), rpc.IssueRequest{RunID: id, Kind: cli.PrincipalRun, Project: "alpha",
		Item: item, Stage: stage, Checkout: dir}); err != nil {
		w.t.Fatal(err)
	}
	return dir
}

func (w *humanWorld) show(item string) machine.Item {
	w.t.Helper()
	it, ref := w.srv.Machine.Show(context.Background(), "alpha", item)
	if ref != nil {
		w.t.Fatalf("show %s: %+v", item, ref)
	}
	return it
}

func (w *humanWorld) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.srv.Store.Read(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(query, args...).Scan(&n) }); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// entries lists the inbox as the coordinator.
func (w *humanWorld) entries(awaits string) []map[string]any {
	w.t.Helper()
	args := []string{"inbox", "list"}
	if awaits != "" {
		args = append(args, "--awaits", awaits)
	}
	env, code := w.coordinator(args...)
	if code == 1 {
		return nil
	}
	if code != 0 {
		w.t.Fatalf("inbox list = %d %v", code, env)
	}
	var out []map[string]any
	for _, e := range env["data"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

func entryByID(es []map[string]any, id string) map[string]any {
	for _, e := range es {
		if e["entry"] == id {
			return e
		}
	}
	return nil
}

// waitNotified waits up to a minute of the test's time for the
// coordinator's session to receive a line that holds want.
func (w *humanWorld) waitNotified(want string) {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		cs := w.srv.Sched.Coordinator()
		if cs.Session != nil {
			for _, n := range w.host.Notifications(cs.Session.ID) {
				if strings.Contains(n, want) {
					return
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.t.Fatalf("the coordinator was never told about %s", want)
}

// panelFile writes a panel file of two judges at level into dir.
func panelFile(t *testing.T, dir, level string, reading *escalation.Reading) string {
	t.Helper()
	p := escalation.Panel{Finding: "the finding", Judges: []escalation.JudgeAnswer{
		{Judge: "fable-5", Level: level, Choice: escalation.Inside, Reason: "the rule says so"},
		{Judge: "sol-6", Level: level, Choice: escalation.Inside, Reason: "the rule says so too"},
	}, Reading: reading}
	raw, err := escalation.EncodePanels([]escalation.Panel{p})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "panel.json")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An escalation at level 2 is refused back to the loop with a triaged note,
// and the item does not move. No inbox entry appears.
func TestEscalationAtLevel2IsRoutedBackToTheLoop(t *testing.T) {
	w := newHumanWorld(t, "")
	item := todoTicket(t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	env, code := w.as(co, "escalation", "raise", "--score", "2", "--body", "gh api says the check is gone")
	if code != 3 || env["code"] != cli.CodeRoutedToLoop || !strings.Contains(env["hint"].(string), "level 2 measured routes to the loop") {
		t.Fatalf("raise = %d %v", code, env)
	}
	it := w.show(item)
	if it.Status != machine.Todo || !strings.Contains(it.Fact(machine.FactNote), `sdlc:triaged level="measured"`) ||
		!strings.Contains(it.Fact(machine.FactNote), "gh api says the check is gone") {
		t.Fatalf("item = %s, note %q", it.Status, it.Fact(machine.FactNote))
	}
	if n := w.count(`SELECT count(*) FROM escalations`); n != 0 {
		t.Fatalf("%d escalations recorded", n)
	}
	if es := w.entries(""); entryByID(es, "escalation/alpha/"+item) != nil {
		t.Fatalf("the inbox shows the loop's own finding: %v", es)
	}
}

// An escalation at level 4 waits for the fixer's panel and holds its item.
// The coordinator hears of it within a minute. The panel's route to a person
// moves the item to escalated, with an inbox entry that waits on a person
// and links the item. The answer resumes the item to its stored target.
func TestLevel4EscalationReachesAPersonThroughThePanel(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	if err := w.srv.Sched.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	item := todoTicket(t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	env, code := w.as(co, "escalation", "raise", "--score", "4", "--body", "which of two layouts the owner prefers")
	if code != 0 {
		t.Fatalf("raise = %d %v", code, env)
	}
	rec := env["data"].(map[string]any)["escalation"].(map[string]any)
	id := fmt.Sprint(rec["escalation"])
	if rec["state"] != escalation.StateAwaitingPanel || w.show(item).Status != machine.Todo {
		t.Fatalf("raise recorded %v, item %s", rec, w.show(item).Status)
	}
	if c, ref := w.srv.Admission.Peek(ctx, "alpha", machine.StageImplementer); c != nil || ref != nil {
		t.Fatalf("a held item was offered: %+v %+v", c, ref)
	}
	w.waitNotified("panel/" + id)
	panel := entryByID(w.entries(cli.PrincipalCoordinator), "panel/"+id)
	if panel == nil || !strings.Contains(panel["text"].(string), "<!--sdlc:agent run=run-1-->") {
		t.Fatalf("the panel entry = %v", panel)
	}

	fixer := w.runToken("fix-1", "", "fixer")
	env, code = w.as(fixer, "work", "peek", "--stage", "fixer")
	pending, _ := env["data"].(map[string]any)["escalations"].([]any)
	if code != 0 || len(pending) != 1 || fmt.Sprint(pending[0].(map[string]any)["escalation"]) != id {
		t.Fatalf("fixer peek = %d %v", code, env)
	}
	// A run of another stage cannot route.
	other := w.runToken("run-2", "", machine.StageImplementer)
	panelFile(t, other, "preference", nil)
	if env, _ := w.as(other, "escalation", "route", id, "--file", "panel.json"); env["code"] != cli.CodeCapabilityDenied {
		t.Fatalf("a non-fixer route = %v", env)
	}
	panelFile(t, fixer, "preference", nil)
	env, code = w.as(fixer, "escalation", "route", id, "--file", "panel.json")
	if code != 0 || env["data"].(map[string]any)["route"] != string(escalation.RoutePerson) {
		t.Fatalf("route = %d %v", code, env)
	}
	if it := w.show(item); it.Status != machine.Escalated || it.Fact(machine.FactEscalation) != id {
		t.Fatalf("item = %s %v", it.Status, it.Facts)
	}
	if n := w.count(`SELECT count(*) FROM holds`); n != 0 {
		t.Fatalf("%d holds remain", n)
	}
	e := entryByID(w.entries("person"), "escalation/alpha/"+item)
	if e == nil || e["awaits"] != "person" || e["link"] != "loop work show "+item+" --project alpha" ||
		!strings.Contains(e["text"].(string), "<!--sdlc:agent run=run-1-->\nwhich of two layouts the owner prefers\n<!--/sdlc:agent-->") {
		t.Fatalf("escalation entry = %v", e)
	}
	w.waitNotified("escalation/alpha/" + item)

	env, code = w.coordinator("inbox", "answer", "escalation/alpha/"+item, "--body", "use the left layout")
	if code != 0 {
		t.Fatalf("answer = %d %v", code, env)
	}
	it := w.show(item)
	if it.Status != machine.Todo || it.Fact(machine.FactEntry) != machine.EntryResume || it.Fact(machine.FactAnswer) != "use the left layout" {
		t.Fatalf("answered item = %s %v", it.Status, it.Facts)
	}
	if w.count(`SELECT count(*) FROM escalations WHERE state = 'answered'`) != 1 {
		t.Fatal("the escalation is not marked answered")
	}
	if entryByID(w.entries(""), "escalation/alpha/"+item) != nil {
		t.Fatal("the answered entry stays in the inbox")
	}
}

// Two judges that agree on a level the loop answers route the escalation
// back: the item does not move, the hold drops and a triaged note stays.
func TestPanelThatAgreesOnALoopLevelRoutesBack(t *testing.T) {
	w := newHumanWorld(t, "")
	item := todoTicket(t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	env, _ := w.as(co, "escalation", "raise", "--score", "5", "--body", "only the owner knows")
	id := fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])
	fixer := w.runToken("fix-1", "", "fixer")
	panelFile(t, fixer, "computed", &escalation.Reading{Probabilities: map[string]float64{"0": 0, "1": 0, "2": 0.1, "3": 0.9}, Score: 2.9, Confidence: 0.95})
	env, code := w.as(fixer, "escalation", "route", id, "--file", "panel.json")
	if code != 0 || env["data"].(map[string]any)["route"] != string(escalation.RouteLoop) {
		t.Fatalf("route = %d %v", code, env)
	}
	it := w.show(item)
	if it.Status != machine.Todo || !strings.Contains(it.Fact(machine.FactNote), "panel routed this escalation back to the loop") {
		t.Fatalf("item = %s %q", it.Status, it.Fact(machine.FactNote))
	}
	if w.count(`SELECT count(*) FROM holds`) != 0 {
		t.Fatal("the hold remains")
	}
	// The same call replays its answer. Another panel on a routed
	// escalation is refused.
	if env, _ := w.as(fixer, "escalation", "route", id, "--file", "panel.json"); env["replayed"] != true {
		t.Fatalf("a retry = %v", env)
	}
	panelFile(t, fixer, "preference", nil)
	if env, _ := w.as(fixer, "escalation", "route", id, "--file", "panel.json"); env["code"] != cli.CodeTransitionNotAllowed {
		t.Fatalf("a second route = %v", env)
	}
}

// An answer that amends the task enters the item's inputs, so a
// twice-failed item is admitted again (R10).
func TestAmendingAnswerReadmitsATwiceFailedItem(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	item := todoTicket(t, w.bx)
	for range 2 {
		st, ref := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageImplementer)
		if ref != nil || st == nil || st.Item != item {
			t.Fatalf("dispatch = %+v %+v", st, ref)
		}
		if _, ref := w.srv.Sched.Finish(ctx, st.RunID, "failed"); ref != nil {
			t.Fatalf("finish = %+v", ref)
		}
	}
	if c, _ := w.srv.Admission.Peek(ctx, "alpha", machine.StageImplementer); c != nil {
		t.Fatalf("a twice-failed item was offered: %+v", c)
	}
	co := w.runToken("run-x", item, machine.StageImplementer)
	env, _ := w.as(co, "escalation", "raise", "--score", "5", "--body", "the task names a file that does not exist")
	id := fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])
	fixer := w.runToken("fix-1", "", "fixer")
	panelFile(t, fixer, "owner-only", nil)
	if _, code := w.as(fixer, "escalation", "route", id, "--file", "panel.json"); code != 0 {
		t.Fatal("route failed")
	}
	env, code := w.coordinator("inbox", "answer", "escalation/alpha/"+item, "--body", "edit lib/b.go instead", "--amend")
	if code != 0 {
		t.Fatalf("answer = %d %v", code, env)
	}
	c, ref := w.srv.Admission.Peek(ctx, "alpha", machine.StageImplementer)
	if ref != nil || c == nil || c.Item != item {
		t.Fatalf("the amended item is not admitted again: %+v %+v", c, ref)
	}
}

// Text that holds a stored secret is refused with SECRET_IN_OUTPUT: a
// note, an escalation, a panel file and an inbox answer. No envelope and
// no log line holds the value.
func TestSecretsAreRefusedInEveryHumanWrite(t *testing.T) {
	w := newHumanWorld(t, "")
	w.srv.Redactor.SetLive("test/value", humanSecret)
	item := todoTicket(t, w.bx)
	co := w.runToken("run-1", item, machine.StageImplementer)
	leak := "the key is " + humanSecret
	for _, args := range [][]string{
		{"work", "note", item, "--body", leak},
		{"escalation", "raise", "--score", "4", "--body", leak},
	} {
		if env, code := w.as(co, args...); code != 3 || env["code"] != cli.CodeSecretInOutput {
			t.Fatalf("%v = %d %v", args, code, env)
		}
	}
	env, _ := w.as(co, "escalation", "raise", "--score", "4", "--body", "clean")
	id := fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])
	fixer := w.runToken("fix-1", "", "fixer")
	if err := os.WriteFile(filepath.Join(fixer, "panel.json"), []byte(`{"finding":"`+leak+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if env, code := w.as(fixer, "escalation", "route", id, "--file", "panel.json"); code != 3 || env["code"] != cli.CodeSecretInOutput {
		t.Fatalf("route = %d %v", code, env)
	}
	panelFile(t, fixer, "preference", nil)
	w.as(fixer, "escalation", "route", id, "--file", "panel.json")
	for _, args := range [][]string{
		{"inbox", "answer", "escalation/alpha/" + item, "--body", leak},
		{"inbox", "prepare", "--body", leak},
		{"decision", "file", "--project", "alpha", "--title", "q", "--trigger", leak},
	} {
		if env, code := w.coordinator(args...); code != 3 || env["code"] != cli.CodeSecretInOutput {
			t.Fatalf("%v = %d %v", args, code, env)
		}
	}
	if strings.Contains(w.run.logs.String(), humanSecret) {
		t.Fatal("a log line holds the secret value")
	}
}

// scopedBody is a ticket whose own "Must not change" list forbids the
// file its contract needs, the shape of TIC-70 (alpha).
const scopedBody = "Parse the second format.\n\n" +
	"## Files\n\n" +
	"**May change:**\n" +
	"- `app/task.go`\n\n" +
	"**Must not change:**\n" +
	"- `lib/**`, the shared library.\n" +
	"- `.github/**`\n\n" +
	"## Done means\n\n" +
	"- The gates pass.\n"

// scopedTicket makes a todo ticket with scopedBody.
func scopedTicket(t *testing.T, bx *fake.Client) string {
	t.Helper()
	ctx := context.Background()
	is, err := bx.Create(ctx, beadsx.CreateSpec{Title: "item", Description: scopedBody, Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bx.Update(ctx, is.ID, beadsx.UpdateSpec{IfStatus: is.Status, Status: machine.Todo}); err != nil {
		t.Fatal(err)
	}
	return is.ID
}

// agreeing is the reading of two judges who agree.
var agreeing = &escalation.Reading{Probabilities: map[string]float64{"0": 0, "1": 0, "2": 0.1, "3": 0.9}, Score: 2.9, Confidence: 0.95}

// failTwice dispatches and fails the item twice, so its inputs stop it.
func (w *humanWorld) failTwice(item string) {
	w.t.Helper()
	ctx := context.Background()
	for range 2 {
		st, ref := w.srv.Sched.Dispatch(ctx, "alpha", machine.StageImplementer)
		if ref != nil || st == nil || st.Item != item {
			w.t.Fatalf("dispatch = %+v %+v", st, ref)
		}
		if _, ref := w.srv.Sched.Finish(ctx, st.RunID, "failed"); ref != nil {
			w.t.Fatalf("finish = %+v", ref)
		}
	}
	if c, _ := w.srv.Admission.Peek(ctx, "alpha", machine.StageImplementer); c != nil {
		w.t.Fatalf("a twice-failed item was offered: %+v", c)
	}
}

// raise5 raises a level 5 escalation on item, and returns its id.
func (w *humanWorld) raise5(item, body string) string {
	w.t.Helper()
	co := w.runToken("run-x", item, machine.StageImplementer)
	env, code := w.as(co, "escalation", "raise", "--score", "5", "--body", body)
	if code != 0 {
		w.t.Fatalf("raise = %d %v", code, env)
	}
	return fmt.Sprint(env["data"].(map[string]any)["escalation"].(map[string]any)["escalation"])
}

// A loop route that releases one path moves it out of "Must not change"
// and into "May change" in the body itself, once. A note that widens the
// scope leaves the body forbidding the work, and the implementer escalates
// again (TIC-70, alpha). The edit enters the retry identity, so a
// twice-failed item is admitted again.
func TestFixerReleaseMovesOnePathIntoMayChangeAndReadmitsTheItem(t *testing.T) {
	w := newHumanWorld(t, "")
	ctx := context.Background()
	item := scopedTicket(t, w.bx)
	w.failTwice(item)
	id := w.raise5(item, "the contract needs lib/parse.go, and the ticket forbids lib/**")
	fixer := w.runToken("fix-1", "", "fixer")
	panelFile(t, fixer, "derived", agreeing)
	env, code := w.as(fixer, "escalation", "route", id, "--file", "panel.json",
		"--release", "lib/parse.go", "--forbids", "lib/**", "--condition", "Add the second format's parser, and change no other file in lib/.")
	data, _ := env["data"].(map[string]any)
	if code != 0 || data["route"] != string(escalation.RouteLoop) || data["released"] != "lib/parse.go" {
		t.Fatalf("route = %d %v", code, env)
	}
	want := "Parse the second format.\n\n" +
		"## Files\n\n" +
		"**May change:**\n" +
		"- `app/task.go`\n" +
		"- `lib/parse.go`: Add the second format's parser, and change no other file in lib/. " +
		"Released from \"Must not change\" by the fixer for escalation " + id + ".\n\n" +
		"**Must not change:**\n" +
		"- `lib/**`, the shared library. The fixer released `lib/parse.go` into \"May change\" for escalation " + id +
		", and nothing else here.\n" +
		"- `.github/**`\n\n" +
		"## Done means\n\n" +
		"- The gates pass.\n"
	it := w.show(item)
	if got := it.Fact(machine.FactBody); got != want {
		t.Fatalf("the body =\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(it.Fact(machine.FactNote), "released `lib/parse.go` from `lib/**`") {
		t.Fatalf("the note = %q", it.Fact(machine.FactNote))
	}
	c, ref := w.srv.Admission.Peek(ctx, "alpha", machine.StageImplementer)
	if ref != nil || c == nil || c.Item != item {
		t.Fatalf("the released item is not admitted again: %+v %+v", c, ref)
	}
	if env, _ := w.as(fixer, "escalation", "route", id, "--file", "panel.json",
		"--release", "lib/parse.go", "--forbids", "lib/**", "--condition", "Add the second format's parser, and change no other file in lib/."); env["replayed"] != true {
		t.Fatalf("a retry = %v", env)
	}
	if got := w.show(item).Fact(machine.FactBody); got != want {
		t.Fatalf("a retry edited the body again:\n%s", got)
	}
}

// A crash between the edit and the record of the route leaves the body
// released and the escalation waiting. The same route again finishes it
// and leaves the body as the edit wrote it. A body that holds the same
// path released for another escalation still refuses: that edit is an
// earlier round's, and this escalation has another cause.
func TestFixerReleaseAfterACrashFinishesTheRoute(t *testing.T) {
	const condition = "Add the second format's parser."
	for name, tc := range map[string]struct {
		other int64
		code  int
		rule  string
	}{
		"this escalation":    {0, 0, ""},
		"another escalation": {1, 2, "already_released"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newHumanWorld(t, "")
			ctx := context.Background()
			item := scopedTicket(t, w.bx)
			id := w.raise5(item, "the contract needs lib/parse.go, and the ticket forbids lib/**")
			n, _ := strconv.ParseInt(id, 10, 64)
			edited, r := escalation.ReleasePath(scopedBody, escalation.Release{Path: "lib/parse.go", Forbids: "lib/**",
				Condition: condition, Escalation: n + tc.other})
			if r != nil {
				t.Fatal(r)
			}
			if res := w.srv.Machine.Apply(ctx, machine.Request{Project: "alpha", Item: item, Verb: "edit",
				Params: map[string]string{"body": edited}, Actor: machine.Server("escalation")}); res.Refusal != nil {
				t.Fatalf("edit = %+v", res.Refusal)
			}
			fixer := w.runToken("fix-1", "", "fixer")
			panelFile(t, fixer, "derived", agreeing)
			env, code := w.as(fixer, "escalation", "route", id, "--file", "panel.json",
				"--release", "lib/parse.go", "--forbids", "lib/**", "--condition", condition)
			data, _ := env["data"].(map[string]any)
			if code != tc.code || env["rule"] != nil && env["rule"] != tc.rule ||
				tc.code == 0 && (data["route"] != string(escalation.RouteLoop) || data["released"] != "lib/parse.go") {
				t.Fatalf("route = %d %v", code, env)
			}
			if tc.rule != "" && env["rule"] != tc.rule {
				t.Fatalf("route = %d %v, want %s", code, env, tc.rule)
			}
			if got := w.show(item).Fact(machine.FactBody); got != edited {
				t.Fatalf("the body =\n%s\nwant\n%s", got, edited)
			}
		})
	}
}

// A release the fixer misspelled writes nothing: the escalation still
// waits for its panel, and the body is the one the ticket was cut with.
func TestFixerReleaseThatNamesNoEntryWritesNothing(t *testing.T) {
	w := newHumanWorld(t, "")
	item := scopedTicket(t, w.bx)
	id := w.raise5(item, "the contract needs lib/parse.go")
	fixer := w.runToken("fix-1", "", "fixer")
	panelFile(t, fixer, "derived", agreeing)
	for _, tc := range []struct {
		rule string
		args []string
	}{
		{"no_entry", []string{"--release", "lib/parse.go", "--forbids", "lib/*", "--condition", "Add the parser."}},
		{"not_covered", []string{"--release", "app/other.go", "--forbids", "lib/**", "--condition", "Add the parser."}},
		{"incomplete_release", []string{"--release", "lib/parse.go", "--forbids", "lib/**"}},
		{"path_not_plain", []string{"--release", "lib/x` `.github/workflows/ci.yml", "--forbids", "lib/**", "--condition", "Add the parser."}},
		{"path_not_plain", []string{"--release", "lib/{x,..}/.github/workflows/ci.yml", "--forbids", "lib/**", "--condition", "Add the parser."}},
		{"condition_not_plain", []string{"--release", "lib/parse.go", "--forbids", "lib/**",
			"--condition", "Edit only this file; never touch `.github/workflows/ci.yml`."}},
	} {
		env, code := w.as(fixer, append([]string{"escalation", "route", id, "--file", "panel.json"}, tc.args...)...)
		if code != 2 || env["code"] != cli.CodeUsage || env["rule"] != tc.rule {
			t.Fatalf("%s: route = %d %v", tc.rule, code, env)
		}
	}
	if n := w.count(`SELECT count(*) FROM escalations WHERE state = ?`, escalation.StateAwaitingPanel); n != 1 {
		t.Fatalf("%d escalations wait for their panel", n)
	}
	if b := w.show(item).Fact(machine.FactBody); b != "" {
		t.Fatalf("a refused release edited the body: %q", b)
	}
}

// A path on the never-released list reaches a person, whatever the judges
// scored, and the body keeps its scope. A release on a panel that routes
// to a person is not applied either.
func TestFixerReleaseOfANeverReleasedPathReachesAPerson(t *testing.T) {
	for name, tc := range map[string]struct {
		level   string
		reading *escalation.Reading
		release []string
		because string
	}{
		"never released":  {"derived", agreeing, []string{"--release", ".github/workflows/ci.yml", "--forbids", ".github/**"}, "never released"},
		"a climb into CI": {"derived", agreeing, []string{"--release", "lib/../.github/workflows/ci.yml", "--forbids", "lib/**"}, "never released"},
		"person route":    {"preference", nil, []string{"--release", "lib/parse.go", "--forbids", "lib/**"}, "routes to a person"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newHumanWorld(t, "")
			item := scopedTicket(t, w.bx)
			id := w.raise5(item, "the contract needs a change the ticket forbids")
			fixer := w.runToken("fix-1", "", "fixer")
			panelFile(t, fixer, tc.level, tc.reading)
			args := append([]string{"escalation", "route", id, "--file", "panel.json", "--condition", "Change the one file."}, tc.release...)
			env, code := w.as(fixer, args...)
			data, _ := env["data"].(map[string]any)
			if code != 0 || data["route"] != string(escalation.RoutePerson) || data["released"] != nil ||
				!strings.Contains(fmt.Sprint(data["decision"]), tc.because) {
				t.Fatalf("route = %d %v", code, env)
			}
			it := w.show(item)
			if it.Status != machine.Escalated || it.Fact(machine.FactBody) != "" {
				t.Fatalf("item = %s, body %q", it.Status, it.Fact(machine.FactBody))
			}
		})
	}
}
