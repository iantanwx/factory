package sched

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// fixture is one registered project of a home coordinator test: its own
// origin and clone, and a checkout host that makes worktrees of its clone.
type fixture struct {
	project config.Project
	dir     string // holds origin.git, seed and the clone
	co      *namedCheckouts
}

// namedCheckouts makes one project's checkouts. Orca keeps each
// repository's worktrees in a directory of their own, and the fake host
// keeps every checkout in one root. So the fixture puts the project's id
// in front of each name, and records the name the scheduler asked for.
type namedCheckouts struct {
	project string
	g       *gitCheckouts
	mu      sync.Mutex
	asked   []string
}

func (c *namedCheckouts) CreateCheckout(ctx context.Context, spec agenthost.CheckoutSpec) (agenthost.Checkout, error) {
	c.mu.Lock()
	c.asked = append(c.asked, spec.Name)
	c.mu.Unlock()
	spec.Name = c.project + "-" + spec.Name
	return c.g.CreateCheckout(ctx, spec)
}

func (c *namedCheckouts) RemoveCheckout(ctx context.Context, path string) error {
	return c.g.RemoveCheckout(ctx, path)
}

func (c *namedCheckouts) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.asked)
}

// homeWorld is a world whose registered projects are the fixtures, each
// with its own clone, and a scheduler that starts coordinators.
type homeWorld struct {
	*world
	mu       sync.Mutex
	fixtures map[string]*fixture
	order    []string // the registered projects, in registration order
}

// newHomeWorld registers one fixture for each id. A project whose id
// config holds false has no .loop/config.yaml on its default branch. The
// project "loop" has the loop's repository.
func newHomeWorld(t *testing.T, ids []string, config map[string]bool) *homeWorld {
	t.Helper()
	hw := &homeWorld{world: newWorld(t), fixtures: map[string]*fixture{}}
	for _, id := range ids {
		hw.addFixture(id, config[id])
	}
	hw.cfg.Projects = hw.projects
	hw.cfg.Checkouts = func(project string) CheckoutHost {
		hw.mu.Lock()
		defer hw.mu.Unlock()
		if f, ok := hw.fixtures[project]; ok {
			return f.co
		}
		return hw.co
	}
	hw.cfg.Coordinator = &CoordinatorConfig{Harness: agenthost.Claude, Model: "opus"}
	hw.rebuild()
	if err := hw.s.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	return hw
}

func (hw *homeWorld) addFixture(id string, withConfig bool) {
	hw.t.Helper()
	dir := filepath.Join(hw.dir, "projects", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		hw.t.Fatal(err)
	}
	_, repo := makeProjectRepo(hw.t, dir)
	repoName := "iantanwx/" + id
	if id == "loop" {
		repoName = coord.LoopRepo
	}
	f := &fixture{project: config.Project{ID: id, Repo: repoName, Path: repo}, dir: dir}
	f.co = &namedCheckouts{project: id, g: &gitCheckouts{t: hw.t, host: hw.host, repo: repo}}
	if !withConfig {
		hw.setConfig(f, false)
	}
	hw.mu.Lock()
	hw.fixtures[id] = f
	hw.order = append(hw.order, id)
	hw.mu.Unlock()
}

func (hw *homeWorld) projects() ([]config.Project, error) {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	out := make([]config.Project, 0, len(hw.order))
	for _, id := range hw.order {
		out = append(out, hw.fixtures[id].project)
	}
	return out, nil
}

// remove unregisters project id.
func (hw *homeWorld) remove(id string) {
	hw.mu.Lock()
	defer hw.mu.Unlock()
	hw.order = slices.DeleteFunc(hw.order, func(p string) bool { return p == id })
}

// setConfig commits .loop/config.yaml to the fixture's remote default
// branch, or a commit that deletes it.
func (hw *homeWorld) setConfig(f *fixture, present bool) {
	hw.t.Helper()
	seed := filepath.Join(f.dir, "seed")
	if present {
		if err := os.WriteFile(filepath.Join(seed, config.ProjectConfigPath), []byte("stages: {}\n"), 0o644); err != nil {
			hw.t.Fatal(err)
		}
		gitIn(hw.t, seed, "add", "-A")
	} else {
		gitIn(hw.t, seed, "rm", "-q", config.ProjectConfigPath)
	}
	gitIn(hw.t, seed, "commit", "-q", "-m", "config")
	gitIn(hw.t, seed, "push", "-q", filepath.Join(f.dir, "origin.git"), "main")
}

func (hw *homeWorld) home(id string) (CoordinatorState, bool) {
	cs, ok := hw.s.Coordinators()[id]
	return cs, ok
}

func (hw *homeWorld) homeRows() int {
	return hw.count(`SELECT count(*) FROM schedule WHERE kind = 'home_coordinator'`)
}

func (hw *homeWorld) live(sess *agenthost.Session) bool {
	if sess == nil {
		return false
	}
	st, err := hw.host.Settle(context.Background(), sess.ID)
	return err == nil && st.Liveness == agenthost.Live
}

func (hw *homeWorld) readBrief(checkout string) brief.Brief {
	hw.t.Helper()
	raw, err := os.ReadFile(filepath.Join(checkout, brief.BriefJSON))
	if err != nil {
		hw.t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		hw.t.Fatal(err)
	}
	return b
}

func (hw *homeWorld) tokenProject(run string) string {
	return hw.str(`SELECT project FROM run_tokens WHERE run_id = ?`, run)
}

// oldSession writes the coordinator row of the release before this one:
// a live session in a loop-coordinator checkout of project, with a token
// that names no project, started under release v0.
func (hw *homeWorld) oldSession(project string) CoordinatorState {
	hw.t.Helper()
	ctx := context.Background()
	co, err := hw.fixtures[project].co.CreateCheckout(ctx, agenthost.CheckoutSpec{Name: coordinatorCheckout})
	if err != nil {
		hw.t.Fatal(err)
	}
	prompt := filepath.Join(hw.t.TempDir(), "prompt.md")
	if err := os.WriteFile(prompt, []byte("old\n"), 0o600); err != nil {
		hw.t.Fatal(err)
	}
	const run = "coordinator-old"
	if _, err := hw.tokens.IssueToCheckout(ctx, rpc.IssueRequest{RunID: run, Kind: cli.PrincipalCoordinator,
		Checkout: co.Path, Release: "v0"}); err != nil {
		hw.t.Fatal(err)
	}
	sess, err := hw.host.Start(ctx, agenthost.StartSpec{Kind: agenthost.KindCoordinator, Checkout: co.Path,
		Harness: agenthost.Claude, Model: "opus", PromptFile: prompt, Title: "loop coordinator"})
	if err != nil {
		hw.t.Fatal(err)
	}
	cs := CoordinatorState{RunID: run, Project: project, Checkout: co.Path, Session: &sess, Release: "v0"}
	raw, err := json.Marshal(cs)
	if err != nil {
		hw.t.Fatal(err)
	}
	if err := hw.st.Write(ctx, func(tx *store.Tx) error {
		ms := tx.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO schedule (project, kind, payload, created_at, updated_at) VALUES ('', 'coordinator', ?, ?, ?)`,
			string(raw), ms, ms)
		return err
	}); err != nil {
		hw.t.Fatal(err)
	}
	return cs
}

func (hw *homeWorld) oldRows() int {
	return hw.count(`SELECT count(*) FROM schedule WHERE kind = 'coordinator'`)
}

// C17, C19 in part and C24: two registered projects with a project config
// get one coordinator each, in a loop-coordinator checkout made from the
// head of their own default branch. Each token names its project, and each
// brief names its project and whether it is the loop project. The third
// project has no config, so it gets no session and no row.
func TestC17OneCoordinatorForEachProjectWithAConfig(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "loop", "gamma"}, map[string]bool{"alpha": true, "loop": true})
	hw.tick()

	if n := hw.homeRows(); n != 2 {
		t.Fatalf("%d home_coordinator rows, want 2", n)
	}
	if _, ok := hw.home("gamma"); ok {
		t.Fatal("gamma has no config and got a coordinator")
	}
	if names := hw.fixtures["gamma"].co.names(); len(names) != 0 {
		t.Fatalf("gamma has no config and got checkouts %q", names)
	}
	seen := map[string]bool{}
	for _, id := range []string{"alpha", "loop"} {
		cs, ok := hw.home(id)
		if !ok || !hw.live(cs.Session) {
			t.Fatalf("no live coordinator for %s: %+v", id, cs)
		}
		if seen[cs.Checkout] || seen[cs.Session.ID.String()] {
			t.Fatalf("%s shares a checkout or a session: %+v", id, cs)
		}
		seen[cs.Checkout], seen[cs.Session.ID.String()] = true, true
		if names := hw.fixtures[id].co.names(); !slices.Equal(names, []string{"loop-coordinator"}) {
			t.Fatalf("%s checkouts asked = %q, want one loop-coordinator", id, names)
		}
		f := hw.fixtures[id]
		if head, want := gitIn(t, cs.Checkout, "rev-parse", "HEAD"), gitIn(t, f.project.Path, "rev-parse", "origin/main"); head != want {
			t.Fatalf("%s checkout at %s, want the head of its own default branch %s", id, head, want)
		}
		if got := hw.tokenProject(cs.RunID); got != id {
			t.Fatalf("C24: the token of %s's coordinator names project %q", id, got)
		}
		b := hw.readBrief(cs.Checkout)
		if b.Project != id || b.LoopProject != (id == "loop") {
			t.Fatalf("%s brief: project %q, loop_project %v", id, b.Project, b.LoopProject)
		}
	}
	if n := len(hw.host.Launches()); n != 2 {
		t.Fatalf("%d sessions started, want 2", n)
	}

	hw.tick()
	if n := len(hw.host.Launches()); n != 2 {
		t.Fatalf("a second tick started more sessions: %d", n)
	}
}

// C18: a registered project with no config gets no session. After a commit
// that adds .loop/config.yaml reaches its remote default branch, the next
// tick starts its coordinator.
func TestC18TheStartFollowsTheConfigWithinOneTick(t *testing.T) {
	hw := newHomeWorld(t, []string{"gamma"}, nil)
	hw.tick()
	if cs, ok := hw.home("gamma"); ok {
		t.Fatalf("gamma has no config and got a coordinator: %+v", cs)
	}

	hw.setConfig(hw.fixtures["gamma"], true)
	hw.tick()
	cs, ok := hw.home("gamma")
	if !ok || !hw.live(cs.Session) {
		t.Fatalf("no coordinator one tick after the config landed: %+v", cs)
	}
}

// C20: A's session exits. The next tick starts A's next session in A's
// existing checkout, and B's session id stays the same.
func TestC20ASessionEndRestartsThatProjectsCoordinatorOnly(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, map[string]bool{"alpha": true, "beta": true})
	hw.tick()
	a, _ := hw.home("alpha")
	b, _ := hw.home("beta")
	if !hw.live(a.Session) || !hw.live(b.Session) {
		t.Fatalf("fixture: coordinators %+v %+v", a, b)
	}

	hw.host.Exit(a.Session.ID, 0)
	hw.tick()
	a2, _ := hw.home("alpha")
	if !hw.live(a2.Session) || a2.RunID == a.RunID || a2.Checkout != a.Checkout {
		t.Fatalf("alpha's next coordinator = %+v, first %+v", a2, a)
	}
	if hw.liveToken(a.RunID) || !hw.liveToken(a2.RunID) {
		t.Fatalf("alpha tokens: old live %v, new live %v", hw.liveToken(a.RunID), hw.liveToken(a2.RunID))
	}
	if b2, _ := hw.home("beta"); b2.RunID != b.RunID || b2.Session.ID != b.Session.ID || !hw.live(b2.Session) {
		t.Fatalf("beta's coordinator changed: %+v, was %+v", b2, b)
	}
	if names := hw.fixtures["alpha"].co.names(); len(names) != 1 {
		t.Fatalf("alpha checkouts asked = %q, want the first one only", names)
	}
}

// A failed start raises an alert that names its project, with a dedupe key
// of its own, and the tick starts the other project's coordinator.
func TestAFailedStartNamesItsProjectAndStopsNoOther(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, map[string]bool{"alpha": true, "beta": true})
	hw.fixtures["alpha"].co.g.fail.Store(true)
	hw.tick()
	if cs, _ := hw.home("beta"); !hw.live(cs.Session) {
		t.Fatalf("beta did not start in the tick of alpha's failure: %+v", cs)
	}
	if got := hw.str(`SELECT project || ' ' || dedupe_key FROM alerts WHERE kind = ?`, AlertCoordinator); got != "alpha coordinator_start_failed:alpha" {
		t.Fatalf("alert = %q, want one that names alpha", got)
	}
}

// C25: the first tick of this release keeps the old session. It gets one
// message that names the new release, and its session id stays. Y gets its
// own coordinator, and X gets no second session.
func TestC25TheFirstStartKeepsTheOldSession(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha", "beta"}, map[string]bool{"alpha": true, "beta": true})
	old := hw.oldSession("alpha")
	hw.tick()
	hw.tick()

	if !hw.live(old.Session) || !hw.liveToken(old.RunID) {
		t.Fatal("the first tick of this release ended the old session")
	}
	notes := hw.host.Notifications(old.Session.ID)
	if len(notes) != 1 || !strings.Contains(notes[0], "v1") {
		t.Fatalf("the old session's messages = %q, want one that names v1", notes)
	}
	if hw.oldRows() != 1 {
		t.Fatal("the old row is gone while its session lives")
	}
	if cs, ok := hw.home("alpha"); ok && cs.RunID != old.RunID {
		t.Fatalf("alpha got a second coordinator: %+v", cs)
	}
	if hw.homeRows() != 1 {
		t.Fatalf("%d home rows, want beta's only", hw.homeRows())
	}
	if cs, _ := hw.home("beta"); !hw.live(cs.Session) {
		t.Fatalf("beta has no coordinator: %+v", cs)
	}
	if n := len(hw.host.Launches()); n != 2 {
		t.Fatalf("%d sessions started, want the old one and beta's", n)
	}
	if got := hw.s.Topology(); got.Old != "alpha" || !got.Homes["beta"] || got.Homes["alpha"] {
		t.Fatalf("topology = %+v", got)
	}
	for _, p := range []string{"alpha", "beta", ""} {
		if id, ok := hw.s.Session(p); !ok || id != old.Session.ID {
			t.Fatalf("Session(%q) = %v %v, want the old session while it lives", p, id, ok)
		}
	}
}

// C26: the old session gives its project to a new coordinator when it
// ends, by the three cases of the spec.
func TestC26TheOldSessionGivesItsProjectToANewCoordinator(t *testing.T) {
	t.Run("it exits and its project has a config", func(t *testing.T) {
		hw := newHomeWorld(t, []string{"alpha", "loop"}, map[string]bool{"alpha": true, "loop": true})
		hw.s.SetLeftOpen(func(_ context.Context, project string) ([]string, error) {
			return []string{"question/4 of " + project, "room/r-7 of " + project}, nil
		})
		old := hw.oldSession("alpha")
		hw.tick()
		hw.host.Exit(old.Session.ID, 0)
		hw.tick()

		if hw.liveToken(old.RunID) {
			t.Fatal("the old token still verifies")
		}
		if hw.oldRows() != 0 {
			t.Fatal("the old row stays after its session ended")
		}
		cs, ok := hw.home("alpha")
		if !ok || !hw.live(cs.Session) || cs.Checkout != old.Checkout || !cs.Made || cs.Fresh {
			t.Fatalf("alpha's coordinator after the hand-over = %+v, old checkout %s", cs, old.Checkout)
		}
		if got := hw.tokenProject(cs.RunID); got != "alpha" {
			t.Fatalf("the new token names %q", got)
		}
		launches := hw.host.Launches()
		prompt, err := os.ReadFile(launches[len(launches)-1].Spec.PromptFile)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"question/4 of alpha", "room/r-7 of alpha"} {
			if !strings.Contains(string(prompt), want) {
				t.Fatalf("the prompt does not name %q:\n%s", want, prompt)
			}
		}
		if id, ok := hw.s.Session("beta"); !ok || id != hw.mustHome("loop").Session.ID {
			t.Fatalf("Session after the old session = %v %v, want the loop project's", id, ok)
		}
	})

	t.Run("it exits and its project has no config", func(t *testing.T) {
		hw := newHomeWorld(t, []string{"alpha"}, nil)
		old := hw.oldSession("alpha")
		hw.tick()
		hw.host.Exit(old.Session.ID, 0)
		hw.tick()

		if hw.liveToken(old.RunID) {
			t.Fatal("the old token still verifies")
		}
		if exists(old.Checkout) {
			t.Fatal("the old checkout stays")
		}
		if hw.oldRows() != 0 || hw.homeRows() != 0 {
			t.Fatalf("rows: old %d, home %d, want none", hw.oldRows(), hw.homeRows())
		}

		hw.setConfig(hw.fixtures["alpha"], true)
		hw.tick()
		cs, ok := hw.home("alpha")
		if !ok || !hw.live(cs.Session) || !exists(cs.Checkout) {
			t.Fatalf("alpha's coordinator after its config landed = %+v", cs)
		}
		if names := hw.fixtures["alpha"].co.names(); !slices.Equal(names, []string{"loop-coordinator", "loop-coordinator"}) {
			t.Fatalf("checkouts asked = %q, want a new loop-coordinator checkout", names)
		}
		if n := hw.alerts(AlertCoordinatorCheckoutMissing); n != 0 {
			t.Fatalf("%d alerts name loop coordinator restart", n)
		}
	})

	t.Run("it exits and its project's head cannot be read", func(t *testing.T) {
		hw := newHomeWorld(t, []string{"alpha", "loop"}, map[string]bool{"alpha": true, "loop": true})
		old := hw.oldSession("alpha")
		hw.tick()
		origin := filepath.Join(hw.fixtures["alpha"].dir, "origin.git")
		if err := os.Rename(origin, origin+".gone"); err != nil {
			t.Fatal(err)
		}
		hw.host.Exit(old.Session.ID, 0)
		hw.tick()

		n := hw.count(`SELECT count(*) FROM alerts WHERE kind = ? AND project = 'alpha' AND state = 'open'`, AlertCoordinator)
		if n != 1 {
			t.Fatalf("%d open %s alerts name alpha, want 1", n, AlertCoordinator)
		}

		if err := os.Rename(origin+".gone", origin); err != nil {
			t.Fatal(err)
		}
		hw.tick()
		cs, ok := hw.home("alpha")
		if !ok || !hw.live(cs.Session) || cs.Checkout != old.Checkout || hw.oldRows() != 0 {
			t.Fatalf("alpha's coordinator after its head came back = %+v, old rows %d", cs, hw.oldRows())
		}
	})

	t.Run("the operator removes its project while it lives", func(t *testing.T) {
		hw := newHomeWorld(t, []string{"alpha", "beta"}, map[string]bool{"alpha": true, "beta": true})
		old := hw.oldSession("alpha")
		hw.tick()
		hw.remove("alpha")
		hw.tick()

		if hw.live(old.Session) {
			t.Fatal("the old session still runs")
		}
		if hw.liveToken(old.RunID) {
			t.Fatal("the old token still verifies")
		}
		if exists(old.Checkout) {
			t.Fatal("the old checkout stays")
		}
		if hw.oldRows() != 0 {
			t.Fatal("the store holds the coordinator row")
		}
		if _, ok := hw.home("alpha"); ok {
			t.Fatal("the removed project got a coordinator")
		}
	})
}

func (hw *homeWorld) mustHome(id string) CoordinatorState {
	hw.t.Helper()
	cs, ok := hw.home(id)
	if !ok || cs.Session == nil {
		hw.t.Fatalf("no coordinator for %s", id)
	}
	return cs
}

func (hw *homeWorld) cursor(kind, project string) (int64, bool) {
	var at int64
	n := hw.count(`SELECT count(*) FROM schedule WHERE kind = ? AND project = ?`, kind, project)
	if n == 0 {
		return 0, false
	}
	if err := hw.st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT due_at FROM schedule WHERE kind = ? AND project = ?`, kind, project).Scan(&at)
	}); err != nil {
		hw.t.Fatal(err)
	}
	return at, true
}

func (hw *homeWorld) setCursor(kind, project string, at int64) {
	hw.t.Helper()
	if err := hw.st.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO schedule (project, kind, due_at, created_at, updated_at) VALUES (?, ?, ?, 0, 0)`, project, kind, at)
		return err
	}); err != nil {
		hw.t.Fatal(err)
	}
}

// C41: the tick writes a new coordinator's home_notify row at the cursor
// of the destination that served its project until then, in the
// transaction that creates its home_coordinator row. A row that exists
// stays as it is.
func TestC41ANewCoordinatorsCursorStartsAtItsFormerDestinations(t *testing.T) {
	hw := newHomeWorld(t, []string{"loop", "gamma", "delta"}, map[string]bool{"loop": true})
	hw.setCursor(homeNotifyKind, "loop", 500)
	hw.setCursor(homeNotifyKind, "delta", 900)
	hw.tick()
	if _, ok := hw.cursor(homeNotifyKind, "gamma"); ok {
		t.Fatal("gamma has no config and got a cursor")
	}

	hw.setConfig(hw.fixtures["gamma"], true)
	hw.setConfig(hw.fixtures["delta"], true)
	hw.tick()
	if _, ok := hw.home("gamma"); !ok {
		t.Fatal("fixture: gamma has no coordinator")
	}
	if at, ok := hw.cursor(homeNotifyKind, "gamma"); !ok || at != 500 {
		t.Fatalf("gamma's cursor = %d %v, want the loop project's 500", at, ok)
	}
	if at, _ := hw.cursor(homeNotifyKind, "delta"); at != 900 {
		t.Fatalf("delta's cursor = %d, want its own 900 as it was", at)
	}
}

// C41 on a host with no loop project and no cursor row of the
// destination: the new cursor starts at the inbox_notify value.
func TestC41ACursorStartsAtTheOldCursorWithNoRowOfItsDestination(t *testing.T) {
	hw := newHomeWorld(t, []string{"alpha"}, map[string]bool{"alpha": true})
	hw.setCursor(notifyKind, "", 300)
	hw.tick()
	if at, ok := hw.cursor(homeNotifyKind, "alpha"); !ok || at != 300 {
		t.Fatalf("alpha's cursor = %d %v, want the inbox_notify value 300", at, ok)
	}
}
