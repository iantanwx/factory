package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/store"
)

// failingCheckouts refuses each new checkout while fail holds.
type failingCheckouts struct {
	worktrees
	fail *atomic.Bool
}

func (f failingCheckouts) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	if f.fail.Load() {
		return agenthost.Checkout{}, errors.New("the checkout host refused the checkout")
	}
	return f.worktrees.CreateCheckout(ctx, spec)
}

// homesOpts shapes a homesWorld.
type homesOpts struct {
	// old gives alpha the live old session of an earlier release, so alpha
	// gets no coordinator of its own.
	old bool
	// fail names the project whose coordinator checkout cannot be made.
	fail string
	// noLoop registers no project of the loop's own repository.
	noLoop bool
}

// homesWorld is a server with four registered projects, each with its own
// repository: loop, the project of the loop's own repository, alpha, beta,
// and gamma, whose default branch holds no project config. The scheduler
// starts a coordinator for each of the first three.
type homesWorld struct {
	*humanWorld
	repos map[string]string
	fail  map[string]*atomic.Bool
	// old is the checkout of alpha's old session, when it has one.
	old string
}

var homesProjects = []string{"loop", "alpha", "beta", "gamma"}

func newHomesWorld(t *testing.T, o homesOpts) *homesWorld {
	t.Helper()
	const cfg = "stages:\n  code-reviewer: {}\n  implementer: {}\nmerge:\n  mechanism: direct\n"
	pc, err := config.ParseProjectConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.ProjectSnapshot{Config: pc, Manifest: &config.Manifest{}}
	hw := &homesWorld{repos: map[string]string{}, fail: map[string]*atomic.Bool{}}
	var ps []config.Project
	for _, id := range homesProjects {
		repo := "iantanwx/" + id
		if id == "loop" && !o.noLoop {
			repo = coord.LoopRepo
		}
		hw.repos[id] = projectRepo(t, cfg)
		hw.fail[id] = &atomic.Bool{}
		ps = append(ps, config.Project{ID: id, Repo: repo, Path: hw.repos[id]})
	}
	setRepoConfig(t, hw.repos["gamma"], false)
	w := &humanWorld{t: t, dir: instanceDir(t), bx: fake.New("tk"), clock: &testClock{now: time.Now()},
		host: agentfake.New(filepath.Join(t.TempDir(), "checkouts"), agentfake.WithQuietPeriod(time.Minute))}
	if err := w.bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
		t.Fatal(err)
	}
	w.opts = Options{
		Beads:  func(string) (beadsx.Client, error) { return w.bx, nil },
		Agents: w.host,
		Sched: SchedOptions{
			Checkouts: func(project string) sched.CheckoutHost {
				return failingCheckouts{worktrees: worktrees{t: t, host: w.host, repo: hw.repos[project], project: project}, fail: hw.fail[project]}
			},
			ProjectConfig: func(context.Context, string) (*config.ProjectSnapshot, error) { return snap, nil },
			Coordinator:   &sched.CoordinatorConfig{Harness: agenthost.Claude},
			TickInterval:  time.Hour,
			BaseHead:      sched.OriginBaseHead(func() ([]config.Project, error) { return ps, nil }),
		},
		Human: HumanOptions{Now: w.clock.Now, PollInterval: 20 * time.Millisecond, WatchInterval: time.Hour},
	}
	w.start()
	hw.humanWorld = w
	if o.old {
		hw.oldSession()
	}
	for _, p := range ps {
		if err := w.srv.Projects.Add(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	if o.fail != "" {
		hw.fail[o.fail].Store(true)
	}
	hw.tick()
	return hw
}

// oldSession writes the coordinator row of a release before ADR 0015: a
// live session in a checkout of alpha, with a token that names no project.
func (hw *homesWorld) oldSession() {
	hw.t.Helper()
	ctx := context.Background()
	co, err := hw.opts.Sched.Checkouts("alpha").CreateCheckout(ctx, agenthost.CheckoutSpec{Name: "loop-coordinator"})
	if err != nil {
		hw.t.Fatal(err)
	}
	prompt := filepath.Join(hw.t.TempDir(), "prompt.md")
	if err := os.WriteFile(prompt, []byte("old\n"), 0o600); err != nil {
		hw.t.Fatal(err)
	}
	if _, err := hw.srv.Tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: "coordinator-old", Kind: cli.PrincipalCoordinator,
		Checkout: co.Path, Release: "v0"}); err != nil {
		hw.t.Fatal(err)
	}
	sess, err := hw.host.Start(ctx, agenthost.StartSpec{Kind: agenthost.KindCoordinator, Checkout: co.Path,
		Harness: agenthost.Claude, Model: "opus", PromptFile: prompt, Title: "loop coordinator"})
	if err != nil {
		hw.t.Fatal(err)
	}
	raw, err := json.Marshal(sched.CoordinatorState{RunID: "coordinator-old", Project: "alpha", Checkout: co.Path, Session: &sess, Release: "v0"})
	if err != nil {
		hw.t.Fatal(err)
	}
	if err := hw.srv.Store.Write(ctx, func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at) VALUES ('', 'coordinator', ?, 0, 0)`, string(raw))
		return err
	}); err != nil {
		hw.t.Fatal(err)
	}
	hw.old = co.Path
}

// setRepoConfig pushes a commit to the default branch of the origin of a
// projectRepo clone: one that deletes .loop/config.yaml, or one that
// brings it back.
func setRepoConfig(t *testing.T, repo string, present bool) {
	t.Helper()
	seed := filepath.Join(filepath.Dir(repo), "seed")
	if present {
		gitT(t, seed, "revert", "--no-edit", "HEAD")
	} else {
		gitT(t, seed, "rm", "-q", config.ProjectConfigPath)
		gitT(t, seed, "commit", "-q", "-m", "drop the config")
	}
	gitT(t, seed, "push", "-q", filepath.Join(filepath.Dir(repo), "origin.git"), "main")
}

// tick ticks the scheduler, and waits for the session of each project
// whose coordinator row stands and whose checkout can be made.
func (hw *homesWorld) tick() {
	hw.t.Helper()
	if err := hw.srv.Sched.Tick(context.Background()); err != nil {
		hw.t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for _, id := range homesProjects {
		for {
			cs, ok := hw.srv.Sched.Coordinators()[id]
			if !ok || cs.Session != nil || hw.fail[id].Load() {
				break
			}
			if time.Now().After(deadline) {
				hw.t.Fatalf("the coordinator of %s did not start", id)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func (hw *homesWorld) session(project string) agenthost.RunID {
	hw.t.Helper()
	cs := hw.srv.Sched.Coordinators()[project]
	if cs.Session == nil {
		hw.t.Fatalf("no coordinator session of %s", project)
	}
	return cs.Session.ID
}

// rings counts the doorbell's rings to project's coordinator session that
// name entry.
func (hw *homesWorld) rings(project, entry string) int {
	hw.t.Helper()
	n := 0
	for _, text := range hw.host.Notifications(hw.session(project)) {
		if strings.HasPrefix(text, "loop inbox:") && strings.Contains(text, entry+" ") {
			n++
		}
	}
	return n
}

// ringsOnly fails the test unless entry rang project's session once, and
// rang no other session.
func (hw *homesWorld) ringsOnly(project, entry string) {
	hw.t.Helper()
	for _, sess := range hw.host.Sessions() {
		for _, text := range hw.host.Notifications(sess.ID) {
			if sess.ID != hw.session(project) && strings.HasPrefix(text, "loop inbox:") && strings.Contains(text, entry+" ") {
				hw.t.Fatalf("%s rang session %s, which does not serve %s: %q", entry, sess.ID, project, text)
			}
		}
	}
	if n := hw.rings(project, entry); n != 1 {
		hw.t.Fatalf("%s rang %s's session %d times, want once", entry, project, n)
	}
}

func (hw *homesWorld) poke() {
	hw.t.Helper()
	if err := hw.srv.Notifier.Poke(context.Background()); err != nil {
		hw.t.Fatal(err)
	}
}

// alert raises an alert of project, and returns its entry id.
func (hw *homesWorld) alert(project, key string) string {
	hw.t.Helper()
	if err := config.StoreAlerter(hw.srv.Store).Raise(context.Background(), config.Alert{Project: project, Kind: sched.AlertRunUnknown,
		DedupeKey: key, Subject: "a run's liveness is unknown"}); err != nil {
		hw.t.Fatal(err)
	}
	return hw.alertEntry(`dedupe_key = ?`, key)
}

func (hw *homesWorld) alertEntry(where string, args ...any) string {
	hw.t.Helper()
	var id int64
	if err := hw.srv.Store.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM alerts WHERE `+where, args...).Scan(&id)
	}); err != nil {
		hw.t.Fatal(err)
	}
	return fmt.Sprintf("alert/%d", id)
}

// reportFault reports a loop fault from a run of alpha.
func (hw *homesWorld) reportFault() string {
	hw.t.Helper()
	co := hw.runToken("run-1", "", machine.StageImplementer)
	if env, code := hw.as(co, "fault", "report", "--file", writeReport(hw.t, "`loop branch push` answered INTERNAL.")); code != 0 {
		hw.t.Fatalf("fault report = %d %v", code, env)
	}
	return "fault/F-1"
}

// inboxOf lists the inbox entries that the coordinator in checkout reads.
func (hw *homesWorld) inboxOf(checkout string) []string {
	hw.t.Helper()
	env, code := hw.as(checkout, "inbox", "list")
	if code != 0 && code != 1 {
		hw.t.Fatalf("inbox list = %d %v", code, env)
	}
	return entryIDs(env)
}

// entryIDs are the ids of the entries an `inbox list` envelope holds.
func entryIDs(env map[string]any) []string {
	var out []string
	es, _ := env["data"].([]any)
	for _, e := range es {
		out = append(out, e.(map[string]any)["entry"].(string))
	}
	return out
}

// C21: beta's coordinator does not start, while alpha's old session
// lives. The alert that names beta lists in the loop project's inbox and
// rings the loop project's session only. The loop project's coordinator
// starts in the same tick.
func TestC21AFailedStartNamesItsProjectAndRingsTheLoopProject(t *testing.T) {
	hw := newHomesWorld(t, homesOpts{old: true, fail: "beta"})
	if cs := hw.srv.Sched.Coordinators()["beta"]; cs.Session != nil {
		t.Fatalf("fixture: beta's coordinator started: %+v", cs)
	}
	entry := hw.alertEntry(`kind = ? AND project = 'beta' AND state = 'open'`, sched.AlertCoordinator)
	hw.poke()
	hw.ringsOnly("loop", entry)
	if got := hw.inboxOf(hw.srv.Sched.Coordinators()["loop"].Checkout); !slices.Contains(got, entry) {
		t.Fatalf("the loop project's inbox = %q, want %s", got, entry)
	}
	if got := hw.inboxOf(hw.old); slices.Contains(got, entry) {
		t.Fatalf("the old session's inbox = %q, and lists beta's failed start", got)
	}
}

// C22: an alert of gamma, which has no config, rings the loop project's
// session, and the session of alpha gets no ring. An entry of alpha rings
// alpha's session, so the session of alpha can get a ring. Alpha's session
// is its own coordinator, or its old session.
func TestC22AnEntryOfAProjectWithNoConfigRingsTheLoopProject(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprintf("old session %v", old), func(t *testing.T) {
			hw := newHomesWorld(t, homesOpts{old: old})
			if _, ok := hw.srv.Sched.Coordinators()["gamma"]; ok {
				t.Fatal("fixture: gamma has no config and got a coordinator")
			}
			entry := hw.alert("gamma", "u-gamma")
			hw.poke()
			hw.ringsOnly("loop", entry)

			own := hw.alert("alpha", "u-alpha")
			hw.poke()
			hw.ringsOnly("alpha", own)
		})
	}
}

// C23: a fault that a run of alpha reports rings the loop project's
// session only, and the session of alpha gets no ring.
func TestC23ALoopFaultRingsTheLoopProjectOnly(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprintf("old session %v", old), func(t *testing.T) {
			hw := newHomesWorld(t, homesOpts{old: old})
			fault := hw.reportFault()
			hw.poke()
			hw.ringsOnly("loop", fault)

			own := hw.alert("alpha", "u-alpha")
			hw.poke()
			hw.ringsOnly("alpha", own)
		})
	}
}

// C41: gamma has no config, so its entry rings the loop project's
// session. Then gamma's config lands, and gamma's coordinator starts. Its
// first ring names gamma's next entry, and not the entry that already rang
// the loop project's session.
func TestC41ANewCoordinatorsFirstRingNamesNoEntryThatRangAnotherSession(t *testing.T) {
	hw := newHomesWorld(t, homesOpts{})
	before := hw.alert("gamma", "u-gamma-1")
	hw.poke()
	hw.ringsOnly("loop", before)

	setRepoConfig(t, hw.repos["gamma"], true)
	hw.tick()
	hw.poke()
	after := hw.alert("gamma", "u-gamma-2")
	hw.poke()
	if n := hw.rings("gamma", before); n != 0 {
		t.Fatalf("gamma's session got %d rings of %s, which rang the loop project's", n, before)
	}
	hw.ringsOnly("gamma", after)
}
