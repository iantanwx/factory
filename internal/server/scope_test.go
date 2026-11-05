package server

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	agentfake "github.com/iantanwx/loop/internal/agenthost/fake"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pipeline"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/store"
)

// scopeWorld is a server with three registered projects, each with its own
// fake beads: alpha, beta, and loop, the project of the loop's own
// repository. Three coordinator tokens call it: alpha's, the loop
// project's, and the old session's, which names no project.
type scopeWorld struct {
	*humanWorld
	beads      map[string]*fake.Client
	alpha, lp  string // the checkouts of alpha's and the loop project's coordinator
	ticket     map[string]string
	runOf      map[string]string
	faultOf    map[string]string
	alertOf    map[string]string
	roomOfBeta string
}

func newScopeWorld(t *testing.T) *scopeWorld {
	t.Helper()
	const cfg = "stages:\n  code-reviewer: {}\n  implementer: {}\nmerge:\n  mechanism: direct\n"
	repo := projectRepo(t, cfg)
	pc, err := config.ParseProjectConfig([]byte(cfg))
	if err != nil {
		t.Fatal(err)
	}
	snap := &config.ProjectSnapshot{Config: pc, Manifest: &config.Manifest{}}
	ctx := context.Background()
	w := &scopeWorld{beads: map[string]*fake.Client{"alpha": fake.New("ta"), "beta": fake.New("tb"), "loop": fake.New("tl")},
		ticket: map[string]string{}, runOf: map[string]string{}, faultOf: map[string]string{}, alertOf: map[string]string{}}
	for _, bx := range w.beads {
		if err := bx.ConfigureStatuses(ctx, machine.Statuses()); err != nil {
			t.Fatal(err)
		}
	}
	w.humanWorld = &humanWorld{t: t, dir: instanceDir(t), bx: w.beads["alpha"], clock: &testClock{now: time.Now()},
		host: agentfake.New(filepath.Join(t.TempDir(), "checkouts"), agentfake.WithQuietPeriod(time.Minute))}
	w.opts = Options{
		Beads: func(project string) (beadsx.Client, error) {
			if bx, ok := w.beads[project]; ok {
				return bx, nil
			}
			return nil, fmt.Errorf("no beads for %s", project)
		},
		Agents: w.host,
		Sched: SchedOptions{
			Checkouts:     func(string) sched.CheckoutHost { return worktrees{t: t, host: w.host, repo: repo} },
			ProjectConfig: func(context.Context, string) (*config.ProjectSnapshot, error) { return snap, nil },
			Coordinator:   &sched.CoordinatorConfig{Harness: agenthost.Claude},
			TickInterval:  time.Hour,
			BaseHead:      repoBaseHead(repo),
		},
		Human: HumanOptions{Now: w.clock.Now, PollInterval: 20 * time.Millisecond, WatchInterval: time.Hour},
		// The pipeline serves work create. A bug report reads no GitHub.
		Setup: []func(*Server) error{func(s *Server) error {
			pl, err := pipeline.New(pipeline.Config{Store: s.Store, Machine: s.Machine, Beads: s.opts.Beads, GitHub: &github.Service{},
				Projects: s.Projects, BrainstormCap: s.Admission.BrainstormCap,
				StageEnabled: func(context.Context, string, string) (bool, error) { return true, nil }})
			if err != nil {
				return err
			}
			return pl.Register(s.RPC)
		}},
	}
	w.start()
	for _, p := range []config.Project{
		{ID: "alpha", Repo: "iantanwx/alpha", Path: repo},
		{ID: "beta", Repo: "iantanwx/beta", Path: t.TempDir()},
		{ID: "loop", Repo: coord.LoopRepo, Path: t.TempDir()},
	} {
		if err := w.srv.Projects.Add(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	w.awaitCoordinator()
	w.coord, w.alpha, w.lp = t.TempDir(), t.TempDir(), t.TempDir()
	for _, r := range []rpc.IssueRequest{
		{RunID: "coord-old", Kind: cli.PrincipalCoordinator, Checkout: w.coord},
		{RunID: "coord-alpha", Kind: cli.PrincipalCoordinator, Project: "alpha", Checkout: w.alpha},
		{RunID: "coord-loop", Kind: cli.PrincipalCoordinator, Project: "loop", Checkout: w.lp},
	} {
		if _, err := w.srv.Tokens.IssueToCheckout(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"alpha", "beta"} {
		w.ticket[p] = w.item(p, "task")
		w.runOf[p] = "r-" + p
		w.exec(`INSERT INTO runs (run_id, project, stage, item, state, started_at) VALUES (?, ?, 'implementer', ?, 'running', 1)`,
			w.runOf[p], p, w.ticket[p])
		f, err := w.srv.Faults.Report(ctx, rpc.Principal{Kind: cli.PrincipalRun, RunID: w.runOf[p], Project: p, Item: w.ticket[p],
			Stage: "implementer"}, "the CLI refused a valid call")
		if err != nil {
			t.Fatal(err)
		}
		w.faultOf[p] = f.ID
		if err := config.StoreAlerter(w.srv.Store).Raise(ctx, config.Alert{Project: p, Kind: sched.AlertRunCrashed,
			DedupeKey: "run_crashed:" + w.runOf[p], Subject: "a run crashed"}); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err := w.srv.Store.Read(ctx, func(tx *sql.Tx) error {
			return tx.QueryRow(`SELECT id FROM alerts WHERE project = ?`, p).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		w.alertOf[p] = fmt.Sprintf("alert/%d", id)
	}
	w.roomOfBeta = "room-b1"
	w.exec(`INSERT INTO rooms (id, project, item, kind, state, reason, opened_by, opened_at, updated_at)
		VALUES (?, 'beta', ?, 'decision', 'idle', 'the operator decides', 'coordinator:coord-old', 1, 1)`, w.roomOfBeta, w.ticket["beta"])
	return w
}

// item creates one open item of kind in project's beads.
func (w *scopeWorld) item(project, kind string) string {
	w.t.Helper()
	is, err := w.beads[project].Create(context.Background(), beadsx.CreateSpec{Title: "Fix the thing", Description: "No paths.", Type: kind})
	if err != nil {
		w.t.Fatal(err)
	}
	return is.ID
}

func (w *scopeWorld) exec(q string, args ...any) {
	w.t.Helper()
	if err := w.srv.Store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// stateOf is everything a verb could change for project: its beads'
// event head and items, and its rows of each table a coordinator verb
// writes.
func (w *scopeWorld) stateOf(project string) string {
	w.t.Helper()
	ctx := context.Background()
	bx := w.beads[project]
	head, err := bx.Head(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	items, err := bx.List(ctx, beadsx.Filter{})
	if err != nil {
		w.t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "head %d\nitems %+v\n", head, items)
	err = w.srv.Store.Read(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`SELECT id, state, count, finding FROM alerts WHERE project = ? ORDER BY id`,
			`SELECT n, status FROM faults WHERE project = ? ORDER BY n`,
			`SELECT id, state, closed_at FROM rooms WHERE project = ? ORDER BY id`,
			`SELECT stage, reason FROM stage_pauses WHERE project = ? ORDER BY stage`,
			`SELECT count(*) FROM settings_changes WHERE project = ?`,
			`SELECT count(*) FROM transitions WHERE project = ?`,
			`SELECT count(*) FROM questions WHERE ? <> ''`,
			`SELECT count(*) FROM schedule WHERE kind = 'inbox_defer' AND ? <> ''`,
		} {
			rs, err := tx.QueryContext(ctx, q, project)
			if err != nil {
				return fmt.Errorf("%s: %w", q, err)
			}
			cols, _ := rs.Columns()
			for rs.Next() {
				vals := make([]any, len(cols))
				ptrs := make([]any, len(cols))
				for i := range vals {
					ptrs[i] = &vals[i]
				}
				if err := rs.Scan(ptrs...); err != nil {
					_ = rs.Close()
					return err
				}
				fmt.Fprintf(&b, "%s %v\n", q, vals)
			}
			if err := rs.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return b.String()
}

// recordFree are the project-scoped verbs that name no record: no item,
// entry, decision, room, run, fault or project argument, and no --project
// or --entry flag. A list among them filters by the caller in spec work
// entries 4 and 6, and a fault report lands in the caller's own project.
var recordFree = []string{"overwatch sweep", "fault report", "inbox list", "inbox wait", "room list", "project list"}

// C5, spec work entry 3: for each verb of the project scope list, a token
// of project alpha on a record of project beta gets CAPABILITY_DENIED,
// rule other_project, with a hint that names beta. Beta's items and rows
// do not change.
func TestC5AProjectsCoordinatorActsOnNoRecordOfAnotherProject(t *testing.T) {
	w := newScopeWorld(t)
	tb, until := w.ticket["beta"], w.clock.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	alert := w.alertOf["beta"]
	calls := map[string][]string{
		"work peek":              {"--stage", "implementer", "--project", "beta"},
		"work show":              {tb, "--project", "beta"},
		"work list":              {"--project", "beta"},
		"work edit":              {tb, "--title", "taken over", "--project", "beta"},
		"work note":              {tb, "--body", "a note", "--project", "beta"},
		"work block":             {tb, "--by", tb, "--project", "beta"},
		"work unblock":           {tb, "--project", "beta"},
		"work cancel":            {tb, "--reason", "not wanted", "--project", "beta"},
		"work shape":             {tb, "--title", "t", "--body", "b", "--priority", "2", "--project", "beta"},
		"work ask":               {tb, "--body", "- Which page?", "--project", "beta"},
		"work duplicate":         {tb, "--of", tb, "--project", "beta"},
		"work supersede":         {tb, "--commit", "abc123", "--reason", "landed", "--project", "beta"},
		"work create":            {"--title", "t", "--request", "the operator's words", "--project", "beta"},
		"run show":               {w.runOf["beta"]},
		"run list":               {"--project", "beta"},
		"run events":             {w.runOf["beta"]},
		"pr pending":             {"--project", "beta"},
		"merge status":           {"--project", "beta"},
		"decision file":          {"--title", "q", "--trigger", "t", "--now", "--project", "beta"},
		"decision list":          {"--project", "beta"},
		"decision fire":          {tb, "--why", "w", "--project", "beta"},
		"decision defer":         {tb, "--why", "w", "--project", "beta"},
		"decision settle":        {tb, "--note", "n", "--project", "beta"},
		"decision moot":          {tb, "--note", "n", "--project", "beta"},
		"fault show":             {w.faultOf["beta"]},
		"fault list":             {"--project", "beta"},
		"inbox answer":           {alert, "--body", "done"},
		"inbox defer":            {alert, "--until", until, "--reason", "r"},
		"inbox prepare":          {"--body", "which?", "--entry", alert},
		"inbox note":             {alert, "--body", "the cause"},
		"room open":              {"--kind", "decision", "--project", "beta", "--item", tb, "--reason", "r", "--score", "4"},
		"room close":             {w.roomOfBeta},
		"epic create":            {"--title", "t", "--request", "the operator's words", "--project", "beta"},
		"epic adr":               {tb, "--request", "r", "--project", "beta"},
		"epic skip-design":       {tb, "--request", "r", "--project", "beta"},
		"epic accept-difference": {tb, "--state", "S1.2", "--request", "r", "--project", "beta"},
		"stage pause":            {"implementer", "--project", "beta", "--reason", "r"},
		"stage resume":           {"implementer", "--project", "beta"},
		"stage list":             {"--project", "beta"},
		"settings show":          {"beta"},
		"settings set":           {"beta", "stages.implementer.cap", "--value", "3", "--reason", "r"},
		"project show":           {"beta"},
	}
	for _, v := range cli.Surface().Verbs() {
		if v.Scope != cli.ScopeProject {
			continue
		}
		_, listed := calls[v.Path]
		if listed == slices.Contains(recordFree, v.Path) {
			t.Errorf("%s: name a call on a record of beta, or list it in recordFree", v.Path)
		}
	}
	before := w.stateOf("beta")
	for path, args := range calls {
		env, code := w.as(w.alpha, append(strings.Fields(path), args...)...)
		if code != 3 || env["code"] != cli.CodeCapabilityDenied || env["rule"] != "other_project" {
			t.Errorf("%s from alpha's coordinator = %d %v, want other_project", path, code, env)
			continue
		}
		if hint, _ := env["hint"].(string); !strings.Contains(hint, "beta") {
			t.Errorf("%s: the hint names no owner: %v", path, env)
		}
	}
	if after := w.stateOf("beta"); after != before {
		t.Fatalf("beta changed:\nbefore %s\nafter %s", before, after)
	}

	// The loop project's coordinator changes no setting of beta, also
	// while an entry of beta rings it.
	env, code := w.as(w.lp, "settings", "set", "beta", "stages.implementer.cap", "--value", "3", "--reason", "r")
	if code != 3 || env["rule"] != "other_project" {
		t.Fatalf("settings set beta from the loop project's coordinator = %d %v", code, env)
	}
}

// C7, spec work entry 3: the loop project's coordinator acts on a loop
// fault of alpha, the run that reported it and the fault's item. Once the
// fault is resolved, no entry names them, and the same reads refuse.
func TestC7TheLoopProjectsCoordinatorActsOnTheEntriesThatRingIt(t *testing.T) {
	w := newScopeWorld(t)
	f, run, item := w.faultOf["alpha"], w.runOf["alpha"], w.ticket["alpha"]
	for _, args := range [][]string{
		{"fault", "ack", f},
		{"fault", "show", f},
		{"run", "show", run},
		{"work", "show", item, "--project", "alpha"},
	} {
		if env, code := w.as(w.lp, args...); code != 0 {
			t.Fatalf("%v from the loop project's coordinator = %d %v", args, code, env)
		}
	}
	if env, code := w.as(w.lp, "fault", "resolve", f, "--fixed-in", "v9.9.9"); code != 0 {
		t.Fatalf("fault resolve = %d %v", code, env)
	}
	for _, args := range [][]string{
		{"run", "show", run},
		{"work", "show", item, "--project", "alpha"},
	} {
		env, code := w.as(w.lp, args...)
		if code != 3 || env["rule"] != "other_project" {
			t.Fatalf("%v after the resolve = %d %v, want other_project", args, code, env)
		}
	}
}

// C8, spec work entry 3: the loop project's coordinator files a bug in
// alpha only when its body names a loop fault of alpha. It creates no
// ticket, room or epic in alpha.
func TestC8TheLoopProjectsCoordinatorFilesABugOnlyForAFaultOfThatProject(t *testing.T) {
	w := newScopeWorld(t)
	bug := []string{"work", "create", "--kind", "bug", "--project", "alpha", "--title", "the CLI refuses a call", "--request", "the operator's words"}
	for _, body := range []string{"no fault named", "see " + w.faultOf["beta"]} {
		env, code := w.as(w.lp, append(bug, "--body", body)...)
		if code != 3 || env["rule"] != "fault_bug_only" {
			t.Fatalf("a bug whose body is %q = %d %v, want fault_bug_only", body, code, env)
		}
	}
	env, code := w.as(w.lp, append(bug, "--body", "loop fault "+w.faultOf["alpha"]+" names the cause")...)
	if code != 0 {
		t.Fatalf("a bug for alpha's fault = %d %v", code, env)
	}
	filed := env["data"].(map[string]any)["item"].(string)
	if it, ref := w.srv.Machine.Read(context.Background(), "alpha", filed); ref != nil || it.Kind != machine.KindBug {
		t.Fatalf("the bug in alpha = %+v %+v", it, ref)
	}
	for _, args := range [][]string{
		{"work", "create", "--project", "alpha", "--title", "t", "--request", "the operator's words"},
		{"room", "open", "--kind", "decision", "--project", "alpha", "--item", w.ticket["alpha"], "--reason", "r", "--score", "4"},
		{"epic", "create", "--project", "alpha", "--title", "t", "--request", "the operator's words"},
	} {
		env, code := w.as(w.lp, args...)
		if code != 3 || env["rule"] != "other_project" {
			t.Fatalf("%v from the loop project's coordinator = %d %v, want other_project", args, code, env)
		}
	}
}

// C9, spec work entry 3: the old session's token names no project, so it
// reaches the records of every project, as before this release. With no
// --project, its stage pause pauses the stage in every project. A token
// of alpha is refused on the same record of beta.
func TestC9TheOldSessionStaysUnscoped(t *testing.T) {
	w := newScopeWorld(t)
	beta := [][]string{
		{"work", "show", w.ticket["beta"], "--project", "beta"},
		{"run", "show", w.runOf["beta"]},
		{"fault", "show", w.faultOf["beta"]},
		{"inbox", "note", w.alertOf["beta"], "--body", "the run's host restarted"},
	}
	for _, args := range beta {
		if env, code := w.as(w.coord, args...); code != 0 {
			t.Fatalf("%v from the old session = %d %v", args, code, env)
		}
		if env, code := w.as(w.alpha, args...); code != 3 || env["rule"] != "other_project" {
			t.Fatalf("%v from alpha's coordinator = %d %v, want other_project", args, code, env)
		}
	}
	if env, code := w.as(w.coord, "stage", "pause", "implementer", "--reason", "the operator asked"); code != 0 {
		t.Fatalf("stage pause from the old session = %d %v", code, env)
	}
	if n := w.count(`SELECT count(*) FROM stage_pauses WHERE project = '' AND stage = 'implementer'`); n != 1 {
		t.Fatalf("pauses of every project = %d, want 1", n)
	}
}
