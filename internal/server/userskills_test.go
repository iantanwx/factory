package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/userspace"
)

// neonTree is the stored tree of the pin neon: its SKILL.md and a script
// with an exec bit.
var neonTree = fstest.MapFS{
	"SKILL.md":       {Data: []byte("---\nname: neon\ndescription: Neon.\n---\n\n# Neon\n"), Mode: 0o644},
	"scripts/run.sh": {Data: []byte("#!/bin/sh\necho neon\n"), Mode: 0o755},
}

func neonPin(t *testing.T) config.SkillPin {
	t.Helper()
	sum, err := pack.TreeHash(neonTree)
	if err != nil {
		t.Fatal(err)
	}
	return config.SkillPin{Name: "neon", Repo: "neondatabase/agent-skills",
		Commit: "ccca3334a7542d3133134cc6926f2fafc6edeb1f", Path: "skills/neon", SHA256: sum}
}

// pinWorld is a server whose project alpha pins neon and whose project
// beta pins nothing. Each project's config loads from its own repository,
// so the server's own loader and syncer serve each run.
type pinWorld struct {
	*humanWorld
	pin     config.SkillPin
	store   userspace.Store
	beta    *fake.Client
	fetches atomic.Int32
}

func newPinWorld(t *testing.T, fetch userspace.Fetch) *pinWorld {
	t.Helper()
	pw := &pinWorld{pin: neonPin(t), store: userspace.Store{Dir: filepath.Join(t.TempDir(), "userspace-skills")}, beta: fake.New("be")}
	stages := "stages:\n  code-reviewer: {}\n  implementer: {}\nmerge:\n  mechanism: direct\n"
	alpha := projectRepo(t, stages+"skills:\n  - name: neon\n    repo: "+pw.pin.Repo+"\n    commit: "+pw.pin.Commit+
		"\n    path: "+pw.pin.Path+"\n    sha256: "+pw.pin.SHA256+"\n")
	beta := projectRepo(t, stages)
	repos := map[string]string{"alpha": alpha, "beta": beta}
	w := &humanWorld{t: t, dir: instanceDir(t), bx: fake.New("al"), clock: &testClock{now: time.Now()},
		host: agentfake.New(filepath.Join(t.TempDir(), "checkouts"), agentfake.WithQuietPeriod(time.Minute))}
	for _, bx := range []*fake.Client{w.bx, pw.beta} {
		if err := bx.ConfigureStatuses(context.Background(), machine.Statuses()); err != nil {
			t.Fatal(err)
		}
	}
	w.opts = Options{
		Beads: func(project string) (beadsx.Client, error) {
			if project == "beta" {
				return pw.beta, nil
			}
			return w.bx, nil
		},
		Agents: w.host,
		Sched: SchedOptions{
			Checkouts:    func(project string) sched.CheckoutHost { return worktrees{t: t, host: w.host, repo: repos[project]} },
			Coordinator:  &sched.CoordinatorConfig{Harness: agenthost.Claude},
			TickInterval: time.Hour,
			BaseHead: sched.OriginBaseHead(func() ([]config.Project, error) {
				return []config.Project{{ID: "alpha", Path: alpha}, {ID: "beta", Path: beta}}, nil
			}),
			SkillStore: pw.store,
			FetchSkill: func(ctx context.Context, pin config.SkillPin) (fs.FS, error) {
				pw.fetches.Add(1)
				return fetch(ctx, pin)
			},
		},
		Human: HumanOptions{Now: w.clock.Now, PollInterval: 20 * time.Millisecond, WatchInterval: time.Hour},
	}
	w.start()
	for id, repo := range repos {
		if err := w.srv.Projects.Add(context.Background(), config.Project{ID: id, Repo: "iantanwx/" + id, Path: repo}); err != nil {
			t.Fatal(err)
		}
	}
	w.coord = t.TempDir()
	if _, err := w.srv.Tokens.IssueToCheckout(context.Background(), rpc.IssueRequest{RunID: "coord-1", Kind: cli.PrincipalCoordinator,
		Checkout: w.coord}); err != nil {
		t.Fatal(err)
	}
	pw.humanWorld = w
	return pw
}

// hold stores neon for project, as a passed fetch does.
func (pw *pinWorld) hold(project string) {
	pw.t.Helper()
	if err := pw.store.Put(project, pw.pin, neonTree); err != nil {
		pw.t.Fatal(err)
	}
}

// dispatch starts one run of stage for project on a new ticket.
func (pw *pinWorld) dispatch(project, stage, status string) *sched.Started {
	pw.t.Helper()
	bx := pw.bx
	if project == "beta" {
		bx = pw.beta
	}
	id := todoTicket(pw.t, bx)
	if status != machine.Todo {
		if _, err := bx.Update(context.Background(), id, beadsx.UpdateSpec{IfStatus: machine.Todo, Status: status}); err != nil {
			pw.t.Fatal(err)
		}
	}
	st, ref := pw.srv.Sched.Dispatch(context.Background(), project, stage)
	if ref != nil || st == nil {
		pw.t.Fatalf("dispatch %s %s = %+v, %+v", project, stage, st, ref)
	}
	return st
}

// openRoomOf opens a review room on a new ticket of project, and returns
// its checkout.
func (pw *pinWorld) openRoomOf(project string) string {
	pw.t.Helper()
	bx := pw.bx
	if project == "beta" {
		bx = pw.beta
	}
	item := todoTicket(pw.t, bx)
	env, code := pw.coordinator("room", "open", "--kind", "review", "--project", project, "--item", item, "--reason", "r", "--score", "4")
	if code != 0 {
		pw.t.Fatalf("room open %s = %d %v", project, code, env)
	}
	return env["data"].(map[string]any)["room"].(map[string]any)["checkout"].(string)
}

func readBrief(t *testing.T, checkout string) brief.Brief {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b brief.Brief
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// rawUserSkills is the brief's user_skills as JSON, so a test reads the
// field by its name.
func rawUserSkills(t *testing.T, checkout string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(checkout, brief.BriefJSON))
	if err != nil {
		t.Fatal(err)
	}
	var b struct {
		UserSkills []map[string]any `json:"user_skills"`
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b.UserSkills
}

// assertHoldsNeon fails unless checkout holds .claude/skills/neon with each
// stored byte and mode, lists it in info/exclude, and names it in the brief.
func (pw *pinWorld) assertHoldsNeon(checkout string) {
	t := pw.t
	t.Helper()
	dir := filepath.Join(checkout, ".claude", "skills", "neon")
	for name, f := range neonTree {
		p := filepath.Join(dir, filepath.FromSlash(name))
		got, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("the run lacks %s: %v", name, err)
		}
		if string(got) != string(f.Data) {
			t.Errorf("%s = %q, want %q", name, got, f.Data)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != f.Mode.Perm() {
			t.Errorf("%s mode = %o, want %o", name, info.Mode().Perm(), f.Mode.Perm())
		}
	}
	out, err := os.ReadFile(gitPath(t, checkout, "info/exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\n/.claude/skills/neon/\n") {
		t.Errorf("info/exclude does not list /.claude/skills/neon/:\n%s", out)
	}
	want := []map[string]any{{"name": "neon", "repo": pw.pin.Repo, "commit": pw.pin.Commit, "sha256": pw.pin.SHA256}}
	if got := rawUserSkills(t, checkout); !jsonEqual(got, want) {
		t.Errorf("brief user_skills = %v, want %v", got, want)
	}
}

// assertHoldsNoUserSkill fails when checkout holds neon under either root,
// or its brief lists a user skill.
func assertHoldsNoUserSkill(t *testing.T, checkout string) {
	t.Helper()
	for _, root := range []string{".claude/skills", ".agents/skills"} {
		if _, err := os.Stat(filepath.Join(checkout, filepath.FromSlash(root), "neon")); err == nil {
			t.Errorf("%s holds %s/neon", checkout, root)
		}
	}
	if got := rawUserSkills(t, checkout); got != nil {
		t.Errorf("brief user_skills = %v, want none", got)
	}
}

func gitPath(t *testing.T, checkout, name string) string {
	t.Helper()
	out, err := gitx.Run(context.Background(), checkout, "rev-parse", "--git-path", name)
	if err != nil {
		t.Fatal(err)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(checkout, p)
	}
	return p
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func noFetch(context.Context, config.SkillPin) (fs.FS, error) {
	return nil, fs.ErrNotExist
}

// C13: a stage run and a room of a project that pins neon, with neon held,
// hold .claude/skills/neon byte for byte, list it in info/exclude, and
// name it in the brief. The run's pack hash is the hash of a run of a
// project that pins nothing.
func TestC13ARunHoldsItsProjectsPinByteForByte(t *testing.T) {
	pw := newPinWorld(t, noFetch)
	pw.hold("alpha")

	run := pw.dispatch("alpha", machine.StageImplementer, machine.Todo)
	pw.assertHoldsNeon(run.Checkout)
	pw.assertHoldsNeon(pw.openRoomOf("alpha"))

	other := pw.dispatch("beta", machine.StageImplementer, machine.Todo)
	if got, want := readBrief(t, run.Checkout).PackHash, readBrief(t, other.Checkout).PackHash; got != want {
		t.Errorf("pack_hash of the run that holds neon = %s, of the run that holds none = %s", got, want)
	}
	if n := pw.fetches.Load(); n != 0 {
		t.Errorf("%d fetches of a held pin, want 0", n)
	}
}

// C14: a pin reaches only its own project. The store holds neon for alpha
// and for beta, and only alpha pins it. Beta's run and room hold no neon,
// and neither does the coordinator, whose checkout comes from alpha.
func TestC14APinReachesOnlyItsOwnProject(t *testing.T) {
	pw := newPinWorld(t, noFetch)
	pw.hold("alpha")
	pw.hold("beta")

	// The next tick starts alpha's coordinator while alpha pins neon and
	// the store holds it.
	if err := pw.srv.Sched.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	coord := pw.srv.Sched.Coordinators()["alpha"]
	if coord.Project != "alpha" || coord.Checkout == "" || coord.Session == nil {
		t.Fatalf("coordinator = %+v, want a session in a checkout of alpha", coord)
	}
	if _, ok := pw.store.Held("alpha", pw.pin); !ok {
		t.Fatal("the store lacks neon for alpha when the coordinator starts")
	}
	assertHoldsNoUserSkill(t, coord.Checkout)

	pw.assertHoldsNeon(pw.dispatch("alpha", machine.StageImplementer, machine.Todo).Checkout)
	assertHoldsNoUserSkill(t, pw.dispatch("beta", machine.StageImplementer, machine.Todo).Checkout)
	assertHoldsNoUserSkill(t, pw.openRoomOf("beta"))
}

// C15: a run start never fetches, and never waits. While the store lacks
// neon and the fetch that the config read started blocks, a run of alpha
// starts, holds no neon, and lists none. Once that fetch stores neon, the
// next run holds it, and no start made a fetch of its own.
func TestC15ARunStartNeverFetchesAndNeverWaits(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	pw := newPinWorld(t, func(ctx context.Context, pin config.SkillPin) (fs.FS, error) {
		<-release
		return neonTree, nil
	})

	first := pw.dispatch("alpha", machine.StageImplementer, machine.Todo)
	assertHoldsNoUserSkill(t, first.Checkout)
	deadline := time.Now().Add(5 * time.Second)
	for pw.fetches.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the config read started no fetch of neon")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := pw.store.Held("alpha", pw.pin); ok {
		t.Fatal("the store holds neon while its fetch blocks")
	}

	close(release)
	for {
		if _, ok := pw.store.Held("alpha", pw.pin); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the store lacks neon after its fetch ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
	pw.assertHoldsNeon(pw.dispatch("alpha", machine.StageCodeReviewer, machine.InReview).Checkout)
	if n := pw.fetches.Load(); n != 1 {
		t.Errorf("%d fetches, want the one the config read started", n)
	}
}
