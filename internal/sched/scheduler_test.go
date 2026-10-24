package sched

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/store"
)

// counter counts the calls of a native job by project.
type counter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *counter) job(_ context.Context, project string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[project]++
	return nil
}

func (c *counter) get(project string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[project]
}

// Each stage and native job ticks on its throughput schedule. A project's
// schedule override replaces it for that project only.
func TestStagesAndJobsTickOnTheirSchedules(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, map[string]string{"implementer": "0 * * * *", "merge-handoff": "*/5 * * * *"})
	if err := config.StoreProjectRow(w.st)(context.Background(), config.Project{ID: "beta", Repo: "iantanwx/beta", Path: "/srv/beta"}); err != nil {
		t.Fatal(err)
	}
	alphaCfg := snapshot(t, "stages:\n  implementer: {schedule: \"*/10 * * * *\"}\n  merge-handoff: {schedule: \"0 * * * *\"}\nmerge:\n  mechanism: direct\n", "")
	betaCfg := snapshot(t, "merge:\n  mechanism: direct\n", "")
	w.cfg.Projects = func() ([]config.Project, error) {
		return []config.Project{{ID: prj, Repo: "iantanwx/alpha", Path: w.repo}, {ID: "beta", Repo: "iantanwx/beta", Path: "/srv/beta"}}, nil
	}
	w.cfg.ProjectConfig = func(_ context.Context, p string) (*config.ProjectSnapshot, error) {
		if p == "beta" {
			return betaCfg, nil
		}
		return alphaCfg, nil
	}
	w.rebuild()
	c := &counter{}
	if err := w.s.RegisterJob("merge-handoff", c.job); err != nil {
		t.Fatal(err)
	}
	if err := w.s.RegisterJob("no-such-job", c.job); err == nil {
		t.Fatal("RegisterJob accepted a name that is no native job")
	}
	w.ticket(machine.Todo)
	w.clock.Set(at(t, "2026-10-21T10:00:30Z"))
	w.tick() // the first sight sets each deadline after now
	if c.get(prj) != 0 || c.get("beta") != 0 || len(w.host.Launches()) != 0 {
		t.Fatal("a first tick fired")
	}
	w.clock.Set(at(t, "2026-10-21T10:05:30Z"))
	w.tick()
	if c.get("beta") != 1 || c.get(prj) != 0 {
		t.Fatalf("at 10:05 beta = %d, alpha = %d; want 1 and 0", c.get("beta"), c.get(prj))
	}
	w.clock.Set(at(t, "2026-10-21T10:10:30Z"))
	w.tick()
	if n := len(w.host.Launches()); n != 1 {
		t.Fatalf("the implementer override */10 started %d runs at 10:10, want 1", n)
	}
	w.clock.Set(at(t, "2026-10-21T11:00:30Z"))
	w.tick()
	if c.get(prj) != 1 || c.get("beta") != 3 {
		t.Fatalf("at 11:00 alpha = %d, beta = %d; want 1 and 3", c.get(prj), c.get("beta"))
	}
}

// A deadline missed while the server was down fires once on the next
// start.
func TestMissedDeadlineFiresOnceAfterRestart(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, map[string]string{"merge-handoff": "*/5 * * * *"})
	c := &counter{}
	register := func() {
		if err := w.s.RegisterJob("merge-handoff", c.job); err != nil {
			t.Fatal(err)
		}
	}
	register()
	w.clock.Set(at(t, "2026-10-21T10:00:30Z"))
	w.tick()
	w.clock.Set(at(t, "2026-10-21T11:02:00Z"))
	w.rebuild()
	register()
	w.tick()
	w.tick()
	if c.get(prj) != 1 {
		t.Fatalf("twelve missed deadlines fired %d times, want once", c.get(prj))
	}
}

// C2: a host config that v0.0.22 wrote holds no overwatch entry. The
// scheduler ticks the overwatch row on the default schedule, and the file
// stays as it was.
func TestC2OldHostConfigTicksOverwatchOnTheDefault(t *testing.T) {
	w := newWorld(t)
	fixture, err := os.ReadFile(filepath.Join("..", "config", "testdata", "host-v0.0.22.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.hostPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	n := 0
	if err := w.s.RegisterInstanceJob("overwatch", func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		n++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	calls := func() int {
		mu.Lock()
		defer mu.Unlock()
		return n
	}
	for _, c := range []struct {
		at   string
		want int
	}{
		{"2026-10-21T10:00:30Z", 0}, // the first sight sets the deadline after now
		{"2026-10-21T10:03:30Z", 1},
		{"2026-10-21T10:10:30Z", 1},
		{"2026-10-21T10:13:30Z", 2},
		{"2026-10-21T10:53:30Z", 3},
	} {
		w.clock.Set(at(t, c.at))
		w.tick()
		if got := calls(); got != c.want {
			t.Fatalf("at %s overwatch ran %d times, want %d", c.at, got, c.want)
		}
	}
	if got := w.count(`SELECT count(*) FROM schedule WHERE kind = 'cron' AND project = '' AND stage = 'overwatch' AND cron_expr = ?`,
		"3,13,23,33,43,53 * * * *"); got != 1 {
		t.Fatalf("%d overwatch rows on the default schedule, want 1", got)
	}
	after, err := os.ReadFile(w.hostPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(fixture) {
		t.Fatalf("the scheduler changed host config:\n%s", after)
	}
}

// leaseCheck wraps the fake host and records the item's lease owner when a
// run starts.
type leaseCheck struct {
	*fake.Host
	w     *world
	item  string
	owner string
}

func (h *leaseCheck) Start(ctx context.Context, spec agenthost.StartSpec) (agenthost.Session, error) {
	h.owner = h.w.lease(h.item).Owner
	return h.Host.Start(ctx, spec)
}

// Dispatch claims before it starts. The run's checkout holds its token,
// its brief and its prompt, and git sees none of them.
func TestDispatchClaimsBeforeStart(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	lc := &leaseCheck{Host: w.host, w: w, item: id}
	w.cfg.Agents = lc
	w.rebuild()
	st := w.dispatch(machine.StageImplementer)
	if lc.owner != st.RunID {
		t.Fatalf("the lease owner at start was %q, want %s", lc.owner, st.RunID)
	}
	if w.status(id) != machine.InProgress || st.Item != id {
		t.Fatalf("status %s, started %+v", w.status(id), st)
	}
	if s, _ := w.runState(st.RunID); s != "running" {
		t.Fatalf("run state = %s", s)
	}
	tf, err := cli.ReadTokenFile(st.Checkout)
	if err != nil || tf.RunID != st.RunID || tf.Item != id {
		t.Fatalf("token file %+v, %v", tf, err)
	}
	raw, err := os.ReadFile(filepath.Join(st.Checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil || b.Item == nil || b.Item.ID != id || b.Attempt != 1 || len(b.AllowedNext) == 0 {
		t.Fatalf("brief = %s", raw)
	}
	if out := gitIn(t, st.Checkout, "status", "--porcelain", "--untracked-files=all"); out != "" {
		t.Fatalf("git status in the run's checkout:\n%s", out)
	}
	l := w.host.Launches()
	if len(l) != 1 || l[0].Spec.Kind != agenthost.KindStage || !strings.HasSuffix(l[0].Spec.PromptFile, brief.PromptFile) {
		t.Fatalf("launches = %+v", l)
	}
	if w.str(`SELECT pack_hash FROM runs WHERE run_id = ?`, st.RunID) == "" {
		t.Fatal("the run record holds no pack hash")
	}
}

// A start failure releases the claim and restores the frontier state. The
// failure count does not change.
func TestStartFailureReleasesTheClaim(t *testing.T) {
	for name, brk := range map[string]func(w *world){
		"never ready": func(w *world) { w.host.SetNeverReady(true) },
		"no checkout": func(w *world) { w.co.fail.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			id := w.ticket(machine.Todo)
			brk(w)
			st, ref := w.s.Dispatch(context.Background(), prj, machine.StageImplementer)
			if ref == nil || st != nil {
				t.Fatalf("dispatch = %+v, %+v; want a refusal", st, ref)
			}
			if w.status(id) != machine.Todo || w.lease(id).Owner != "" || w.failures(id) != 0 {
				t.Fatalf("status %s, lease %+v, failures %d", w.status(id), w.lease(id), w.failures(id))
			}
			run := w.str(`SELECT run_id FROM runs`)
			if _, class := w.runState(run); class != "not-ready" || w.liveToken(run) {
				t.Fatalf("class %s, live token %v", class, w.liveToken(run))
			}
			if w.count(`SELECT count(*) FROM runs WHERE ended_at IS NULL`) != 0 {
				t.Fatal("the failed start holds a cap slot")
			}
		})
	}
}

// A tracked file at an injected path refuses the run with CONFIG_CONFLICT,
// and the failure count is unchanged.
func TestTrackedInjectedPathRefusesUncounted(t *testing.T) {
	w := newWorld(t)
	seed := filepath.Join(w.dir, "seed")
	if err := os.WriteFile(filepath.Join(seed, ".loop", "brief.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, seed, "add", "-A")
	gitIn(t, seed, "commit", "-q", "-m", "track brief")
	gitIn(t, seed, "push", "-q", w.origin, "main")
	gitIn(t, w.repo, "fetch", "-q", "origin")
	id := w.ticket(machine.Todo)
	_, ref := w.s.Dispatch(context.Background(), prj, machine.StageImplementer)
	if ref == nil || ref.Code != cli.CodeConfigConflict || !strings.Contains(ref.Hint, ".loop/brief.json") {
		t.Fatalf("dispatch = %+v", ref)
	}
	if w.failures(id) != 0 || w.status(id) != machine.Todo || len(w.host.Launches()) != 0 {
		t.Fatalf("failures %d, status %s, launches %d", w.failures(id), w.status(id), len(w.host.Launches()))
	}
	run := w.str(`SELECT run_id FROM runs`)
	if _, class := w.runState(run); class != "refused" {
		t.Fatalf("class = %s, want refused", class)
	}
	if w.alerts(AlertConfigConflict) != 1 {
		t.Fatal("no config conflict alert")
	}
}

// AE8: a manifest secret with no stored value starts no run, puts one inbox
// item naming it, and changes no failure count.
func TestMissingSecretRefusesBeforeClaim(t *testing.T) {
	w := newWorld(t)
	w.project.set(snapshot(t, "stages:\n  implementer: {}\nmerge:\n  mechanism: direct\n",
		"secrets:\n  - name: STRIPE_KEY\n    source: op://dev/stripe/key\n"))
	id := w.ticket(machine.Todo)
	for range 2 {
		_, ref := w.s.Dispatch(context.Background(), prj, machine.StageImplementer)
		if ref == nil || ref.Code != cli.CodeMissingSecret || !strings.Contains(ref.Hint, "STRIPE_KEY") {
			t.Fatalf("dispatch = %+v", ref)
		}
	}
	if n := w.count(`SELECT count(*) FROM runs`); n != 0 {
		t.Fatalf("%d run rows", n)
	}
	if n := w.count(`SELECT count(*) FROM alerts WHERE kind = ? AND state = 'open' AND body LIKE '%STRIPE_KEY%'`, AlertMissingSecret); n != 1 {
		t.Fatalf("%d open missing-secret items, want 1", n)
	}
	if w.failures(id) != 0 || w.status(id) != machine.Todo {
		t.Fatal("the refusal moved the item or counted")
	}
	const value = "sk_test_value_123"
	if err := w.sec.Set(context.Background(), prj, "STRIPE_KEY", strings.NewReader(value)); err != nil {
		t.Fatal(err)
	}
	st := w.dispatch(machine.StageImplementer)
	env, err := os.ReadFile(filepath.Join(st.Checkout, ".env"))
	if err != nil || !strings.Contains(string(env), value) {
		t.Fatalf(".env: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(st.Checkout, brief.BriefJSON))
	if strings.Contains(string(raw), value) || !strings.Contains(string(raw), "STRIPE_KEY") {
		t.Fatalf("brief: %s", raw)
	}
}

// A run receives only the secrets its stage's entries name, each in the
// .env of the entry's directory (parity 9.3, 11.2). A secret of another
// stage with no stored value does not stop the run, and the brief names
// only the run's own secrets.
func TestRunReceivesOnlyItsStagesSecretsInEachDir(t *testing.T) {
	w := newWorld(t)
	w.project.set(snapshot(t, "stages:\n  implementer: {}\nmerge:\n  mechanism: direct\n",
		"secrets:\n  - name: ROOT_KEY\n  - name: WEB_KEY\n    dir: apps/web\n    stages: [implementer]\n"+
			"  - name: REVIEW_KEY\n    stages: [code-reviewer]\n"))
	for name, v := range map[string]string{"ROOT_KEY": "root_value_1", "WEB_KEY": "web_value_1"} {
		if err := w.sec.Set(context.Background(), prj, name, strings.NewReader(v)); err != nil {
			t.Fatal(err)
		}
	}
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	root, err := os.ReadFile(filepath.Join(st.Checkout, ".env"))
	if err != nil || string(root) != "ROOT_KEY='root_value_1'\n" {
		t.Fatalf(".env = %q, %v", root, err)
	}
	web, err := os.ReadFile(filepath.Join(st.Checkout, "apps/web/.env"))
	if err != nil || string(web) != "WEB_KEY='web_value_1'\n" {
		t.Fatalf("apps/web/.env = %q, %v", web, err)
	}
	raw, _ := os.ReadFile(filepath.Join(st.Checkout, brief.BriefJSON))
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.Secrets.Present, []string{"ROOT_KEY", "WEB_KEY"}) {
		t.Fatalf("brief secrets = %v", b.Secrets.Present)
	}
	if _, ref := w.s.Finish(context.Background(), st.RunID, "done"); ref != nil {
		t.Fatal(ref)
	}
	if exists(filepath.Join(st.Checkout, "apps/web/.env")) || exists(filepath.Join(st.Checkout, ".env")) {
		t.Fatal("a .env stays after the run settled")
	}
}

// run finish --outcome done releases the lease and settles the checkout.
// The next tick closes the session.
func TestFinishDoneReleasesTheLease(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.InReview)
	st := w.dispatch(machine.StageCodeReviewer)
	if w.lease(id).Owner != st.RunID {
		t.Fatal("the reviewer holds no lease")
	}
	res, ref := w.s.Finish(context.Background(), st.RunID, "done")
	if ref != nil || res.Class != "done" || res.Counted || !res.Released {
		t.Fatalf("finish = %+v, %+v", res, ref)
	}
	if w.lease(id).Owner != "" || w.status(id) != machine.InReview {
		t.Fatalf("lease %+v, status %s", w.lease(id), w.status(id))
	}
	if w.liveToken(st.RunID) || exists(filepath.Join(st.Checkout, ".env")) {
		t.Fatal("the token is live or .env stays")
	}
	if s := w.str(`SELECT state FROM checkouts WHERE run_id = ?`, st.RunID); s != CheckoutSettled {
		t.Fatalf("checkout state = %s", s)
	}
	again, ref := w.s.Finish(context.Background(), st.RunID, "done")
	if ref != nil || !again.Replayed || again.Class != "done" {
		t.Fatalf("a second finish = %+v, %+v", again, ref)
	}
	w.tick()
	if inv, _ := w.host.Inventory(context.Background()); len(inv.Rows) != 0 {
		t.Fatalf("the finished run's session is open: %+v", inv.Rows)
	}
}

// A blocked finish holds the item at its stage and raises one alert that
// says so, instead of a new run on every tick. An answer to the alert
// lifts the hold. A run that escalated leaves the hold to its escalation.
func TestBlockedFinishHoldsTheItemAndAlerts(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	id := w.ticket(machine.InReview)
	st := w.dispatch(machine.StageCodeReviewer)
	if _, ref := w.s.FinishFault(ctx, st.RunID, "blocked", ""); ref != nil {
		t.Fatal(ref)
	}
	if w.alerts(AlertRunBlocked) != 1 || w.status(id) != machine.InReview {
		t.Fatalf("blocked alerts = %d, status %s", w.alerts(AlertRunBlocked), w.status(id))
	}
	if again, ref := w.s.Dispatch(ctx, prj, machine.StageCodeReviewer); again != nil || ref != nil {
		t.Fatalf("the held item started again: %+v %+v", again, ref)
	}
	if err := w.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`UPDATE alerts SET state = 'closed' WHERE kind = ?`, AlertRunBlocked)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	next := w.dispatch(machine.StageCodeReviewer)
	if next.Item != id {
		t.Fatalf("the answered item did not start: %+v", next)
	}
	if err := w.st.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO escalations (project, item, run_id, level, choice, finding, created_at, updated_at)
			VALUES (?, ?, ?, 5, 'inside', 'f', 0, 0)`, prj, id, next.RunID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, ref := w.s.Finish(ctx, next.RunID, "blocked"); ref != nil {
		t.Fatal(ref)
	}
	if n := w.count(`SELECT count(*) FROM alerts WHERE kind = ? AND state = 'open'`, AlertRunBlocked); n != 0 {
		t.Fatalf("a run that escalated raised %d blocked alerts", n)
	}
}

// The second counted failure with unchanged inputs raises one alert that
// names the item, the stage and both failed runs.
func TestRetryLimitAlertsOnce(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	var runs []string
	for range 2 {
		st := w.dispatch(machine.StageImplementer)
		runs = append(runs, st.RunID)
		if _, ref := w.s.Finish(ctx, st.RunID, "failed"); ref != nil {
			t.Fatal(ref)
		}
		if len(runs) == 1 && w.alerts(AlertRetryLimit) != 0 {
			t.Fatal("one failure raised the retry-limit alert")
		}
	}
	if w.alerts(AlertRetryLimit) != 1 {
		t.Fatalf("retry-limit alerts = %d, want 1", w.alerts(AlertRetryLimit))
	}
	subject := w.str(`SELECT subject FROM alerts WHERE kind = ?`, AlertRetryLimit)
	body := w.str(`SELECT body FROM alerts WHERE kind = ?`, AlertRetryLimit)
	if !strings.Contains(subject, id) || !strings.Contains(subject, machine.StageImplementer) ||
		!strings.Contains(body, runs[0]) || !strings.Contains(body, runs[1]) || !strings.Contains(body, "loop work edit "+id) {
		t.Fatalf("alert = %q / %q", subject, body)
	}
}

// A session that ends with no finish settles as crashed, with no CLI call.
// It restores the stage-correct state, keeps the checkout without .env and
// raises an inbox alert.
func TestEndWithoutFinishSettlesCrashed(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	epoch := w.lease(id).Epoch
	w.host.Exit(st.Session.ID, 1)
	w.tick()
	if _, class := w.runState(st.RunID); class != "crashed" {
		t.Fatalf("class = %s", class)
	}
	if w.status(id) != machine.Todo || w.lease(id).Owner != "" || w.lease(id).Epoch <= epoch || w.failures(id) != 1 {
		t.Fatalf("status %s, lease %+v (epoch was %d), failures %d", w.status(id), w.lease(id), epoch, w.failures(id))
	}
	if !exists(st.Checkout) || exists(filepath.Join(st.Checkout, ".env")) {
		t.Fatal("the checkout is gone or .env stays")
	}
	if s := w.str(`SELECT state FROM checkouts WHERE run_id = ?`, st.RunID); s != CheckoutKept {
		t.Fatalf("checkout state = %s", s)
	}
	if w.alerts(AlertRunCrashed) != 1 || w.liveToken(st.RunID) {
		t.Fatal("no crash alert, or a live token")
	}
}

// An agent idle at its prompt with no finish settles as crashed after the
// quiet period. Its session closes, its checkout stays and its slot frees.
// The agent leaves uncommitted work, so the cleanup pass of the same tick
// keeps the checkout too.
func TestIdleAtPromptSettlesAfterTheQuietPeriod(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	if err := os.WriteFile(filepath.Join(st.Checkout, "work.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.host.EndTurn(st.Session.ID)
	w.clock.Advance(10 * time.Minute)
	w.tick()
	if s, _ := w.runState(st.RunID); s != "running" {
		t.Fatalf("state after 10 quiet minutes = %s", s)
	}
	w.clock.Advance(25 * time.Minute)
	w.tick()
	if _, class := w.runState(st.RunID); class != "crashed" {
		t.Fatalf("class = %s", class)
	}
	if inv, _ := w.host.Inventory(context.Background()); len(inv.Rows) != 0 || !exists(st.Checkout) {
		t.Fatal("the session stays open, or the checkout is gone")
	}
	if w.count(`SELECT count(*) FROM runs WHERE ended_at IS NULL`) != 0 || w.status(id) != machine.Todo {
		t.Fatal("the slot is held")
	}
}

// A live run past its run budget raises one coordinator-owned alert and
// keeps running.
func TestRunPastBudgetAlertsAndKeepsRunning(t *testing.T) {
	w := newWorld(t)
	w.writeHost(nil, nil, func(c *config.HostConfig) { c.RunBudget = time.Hour })
	id := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	w.clock.Advance(61 * time.Minute)
	w.tick()
	w.tick()
	if w.alerts(AlertRunOverBudget) != 1 {
		t.Fatalf("budget alerts = %d, want 1", w.alerts(AlertRunOverBudget))
	}
	if s, _ := w.runState(st.RunID); s != "running" || w.lease(id).Owner != st.RunID {
		t.Fatal("the run over budget was stopped")
	}
	if inv, _ := w.host.Inventory(context.Background()); len(inv.Rows) != 1 {
		t.Fatal("the session was closed")
	}
}

// The liveness tick is written every interval. After a SIGKILL the next
// start records the outage span.
func TestLivenessTickAndOutageSpan(t *testing.T) {
	w := newWorld(t)
	if err := w.s.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	last := w.clock.Now()
	if got := w.count(`SELECT last_tick_at FROM liveness_ticks WHERE id = 1`); int64(got) != last.UnixMilli() {
		t.Fatalf("last tick %d, want %d", got, last.UnixMilli())
	}
	if DefaultLivenessInterval != 15*time.Second {
		t.Fatalf("liveness interval = %s", DefaultLivenessInterval)
	}
	// SIGKILL: no stop step runs.
	w.clock.Advance(10 * time.Minute)
	w.restart()
	if n := w.count(`SELECT count(*) FROM outages WHERE started_at = ? AND ended_at = ?`, last.UnixMilli(), w.clock.Now().UnixMilli()); n != 1 {
		t.Fatalf("outage spans matching = %d", n)
	}
}

// A 30-minute command inside one run is not reclaimed, and no second run
// starts on that item.
func TestLongCommandIsNotReclaimed(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	w.host.StartTool(st.Session.ID)
	for range 4 {
		w.clock.Advance(10 * time.Minute)
		w.tick()
	}
	if s, _ := w.runState(st.RunID); s != "running" || w.lease(id).Owner != st.RunID {
		t.Fatal("the run was reclaimed")
	}
	if again, ref := w.s.Dispatch(context.Background(), prj, machine.StageImplementer); again != nil || ref != nil {
		t.Fatalf("a second dispatch = %+v, %+v", again, ref)
	}
	if len(w.host.Launches()) != 1 {
		t.Fatal("a second run started")
	}
}

// The OnCall hook records a run's last CLI call, which decides infra.
func TestOnCallRecordsLastCLICall(t *testing.T) {
	w := newWorld(t)
	w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	w.s.OnCall(context.Background(), principal(st))
	if got := w.count(`SELECT coalesce(last_cli_call_at, 0) FROM runs WHERE run_id = ?`, st.RunID); int64(got) != w.clock.Now().UnixMilli() {
		t.Fatalf("last_cli_call_at = %d", got)
	}
}

// A session that ends during an outage after its last CLI call is infra,
// which does not count.
func TestEndDuringOutageIsInfra(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	st := w.dispatch(machine.StageImplementer)
	w.clock.Advance(time.Minute)
	if err := w.s.Heartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(5 * time.Minute)
	w.host.Exit(st.Session.ID, 1)
	w.clock.Advance(5 * time.Minute)
	w.restart()
	if _, class := w.runState(st.RunID); class != "infra" || w.failures(id) != 0 || w.status(id) != machine.Todo {
		t.Fatalf("class %s, failures %d, status %s", class, w.failures(id), w.status(id))
	}
}

// The brief carries the body that work edit wrote, not the bead's
// original description.
func TestBriefCarriesTheEditedBody(t *testing.T) {
	w := newWorld(t)
	id := w.ticket(machine.Todo)
	if _, err := w.bx.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: machine.Todo,
		SetMetadata: map[string]string{"loop." + machine.FactBody: "The edited body."},
	}); err != nil {
		t.Fatal(err)
	}
	st := w.dispatch(machine.StageImplementer)
	raw, err := os.ReadFile(filepath.Join(st.Checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil || b.Item == nil || b.Item.Body != "The edited body." {
		t.Fatalf("brief = %s", raw)
	}
}

// A crashed attempt's uncommitted work stays in its kept checkout. The
// next attempt's brief names that checkout and each dirty file in it, so
// the run can carry the work over. A kept checkout with nothing dirty,
// and one of another item, go unnamed.
func TestBriefNamesTheKeptCheckoutOfACrashedAttempt(t *testing.T) {
	w := newWorld(t)
	w.ticket(machine.Todo)
	w.ticket(machine.Todo)
	dirty := w.dispatch(machine.StageImplementer)
	clean := w.dispatch(machine.StageImplementer)
	if err := os.WriteFile(filepath.Join(dirty.Checkout, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dirty.Checkout, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty.Checkout, "pkg", "work.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.host.Exit(dirty.Session.ID, 1)
	w.host.Exit(clean.Session.ID, 1)
	w.tick()
	briefs := map[string]brief.Brief{}
	mds := map[string]string{}
	for range 2 {
		st := w.dispatch(machine.StageImplementer)
		raw, err := os.ReadFile(filepath.Join(st.Checkout, brief.BriefJSON))
		if err != nil {
			t.Fatal(err)
		}
		var b brief.Brief
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatal(err)
		}
		md, err := os.ReadFile(filepath.Join(st.Checkout, brief.BriefMD))
		if err != nil {
			t.Fatal(err)
		}
		briefs[st.Item], mds[st.Item] = b, string(md)
	}
	want := []brief.KeptCheckout{{RunID: dirty.RunID, Path: dirty.Checkout, Dirty: []string{"README.md", "pkg/work.go"}}}
	got := briefs[dirty.Item].KeptCheckouts
	if !slices.EqualFunc(got, want, func(a, b brief.KeptCheckout) bool {
		return a.RunID == b.RunID && a.Path == b.Path && slices.Equal(a.Dirty, b.Dirty)
	}) {
		t.Fatalf("kept checkouts = %+v, want %+v", got, want)
	}
	for _, s := range []string{dirty.Checkout, dirty.RunID, "`README.md`", "`pkg/work.go`"} {
		if !strings.Contains(mds[dirty.Item], s) {
			t.Fatalf("brief.md lacks %q:\n%s", s, mds[dirty.Item])
		}
	}
	if got := briefs[clean.Item].KeptCheckouts; len(got) != 0 || strings.Contains(mds[clean.Item], dirty.Checkout) {
		t.Fatalf("the clean item's brief names a kept checkout: %+v\n%s", got, mds[clean.Item])
	}
}

// dirtyPaths names a modified, a deleted, a renamed and an untracked
// path, each once, and nothing in a clean checkout.
func TestDirtyPathsNamesEachChangedPathOnce(t *testing.T) {
	_, repo := makeProjectRepo(t, t.TempDir())
	if got, err := dirtyPaths(context.Background(), repo); err != nil || len(got) != 0 {
		t.Fatalf("clean checkout: %v, %v", got, err)
	}
	gitIn(t, repo, "mv", "README.md", "READ ME.md")
	gitIn(t, repo, "rm", "-q", ".loop/secrets.yaml")
	if err := os.WriteFile(filepath.Join(repo, ".loop", "config.yaml"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := dirtyPaths(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{".loop/config.yaml", ".loop/secrets.yaml", "READ ME.md", "new.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("dirty = %q, want %q", got, want)
	}
}

// A rename that git status names in the worktree column, as for a moved
// file added with intent to add, names its new path once, and not a
// piece of the path it came from.
func TestDirtyPathsReadsAWorktreeRename(t *testing.T) {
	_, repo := makeProjectRepo(t, t.TempDir())
	if err := os.Rename(filepath.Join(repo, "README.md"), filepath.Join(repo, "NOTES.md")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "add", "-N", "NOTES.md")
	got, err := dirtyPaths(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"NOTES.md"}; !slices.Equal(got, want) {
		t.Fatalf("dirty = %q, want %q", got, want)
	}
}

// A kept checkout with more dirty files than a brief names lists the
// first MaxDirty of them, and counts the rest.
func TestBriefCapsTheDirtyFilesOfAKeptCheckout(t *testing.T) {
	w := newWorld(t)
	w.ticket(machine.Todo)
	crashed := w.dispatch(machine.StageImplementer)
	for i := range brief.MaxDirty + 3 {
		if err := os.WriteFile(filepath.Join(crashed.Checkout, fmt.Sprintf("f%03d.txt", i)), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.host.Exit(crashed.Session.ID, 1)
	w.tick()
	st := w.dispatch(machine.StageImplementer)
	raw, err := os.ReadFile(filepath.Join(st.Checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.KeptCheckouts) != 1 {
		t.Fatalf("kept checkouts = %+v", b.KeptCheckouts)
	}
	if k := b.KeptCheckouts[0]; len(k.Dirty) != brief.MaxDirty || k.More != 3 {
		t.Fatalf("dirty = %d files, more = %d, want %d and 3", len(k.Dirty), k.More, brief.MaxDirty)
	}
}

// One fire of a run stage admits runs until the stage's cap is full or no
// candidate is left. Tickertown runs four implementer rows, each with its
// own window of runs, so 16 starts an hour reach its cap of 8
// (scripts/sdlc/timer-implementer-rows.ts, alpha). A fire that starts one
// run holds the loop to one start a tick, shared by new and repair work.
func TestOneFireFillsTheStagesFreeCap(t *testing.T) {
	w := newWorld(t)
	w.writeHost(map[string]int{"implementer": 8}, map[string]string{"implementer": "0 * * * *"})
	w.rebuild()
	var ids []string
	for range 9 {
		ids = append(ids, w.ticket(machine.Todo))
	}
	w.clock.Set(at(t, "2026-10-21T10:00:30Z"))
	w.tick() // the first sight sets the deadline after now
	w.clock.Set(at(t, "2026-10-21T11:00:30Z"))
	w.tick()
	if n := len(w.host.Launches()); n != 8 {
		t.Fatalf("one fire with 9 todo tickets and cap 8 started %d runs, want 8", n)
	}
	todo := 0
	for _, id := range ids {
		if w.status(id) == machine.Todo {
			todo++
		}
	}
	if todo != 1 {
		t.Fatalf("%d tickets still todo, want 1", todo)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE ended_at IS NULL`); n != 8 {
		t.Fatalf("%d live runs, want 8", n)
	}
}
