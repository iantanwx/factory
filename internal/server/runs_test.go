package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// runsWorld is a server with no beads, the project alpha, and a
// coordinator checkout and a run checkout that each hold a token.
type runsWorld struct {
	t             *testing.T
	dir           string
	srv           *Server
	coord, runDir string
}

func newRunsWorld(t *testing.T) *runsWorld {
	t.Helper()
	w := &runsWorld{t: t, dir: instanceDir(t), coord: t.TempDir(), runDir: t.TempDir()}
	start(t, w.dir, Options{Setup: []func(*Server) error{func(s *Server) error { w.srv = s; return nil }}}, true)
	ctx := context.Background()
	if err := config.StoreProjectRow(w.srv.Store)(ctx, config.Project{ID: "alpha", Repo: "iantanwx/alpha"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []rpc.IssueRequest{
		{RunID: "coord-1", Kind: cli.PrincipalCoordinator, Checkout: w.coord},
		{RunID: "r-live", Kind: cli.PrincipalRun, Project: "alpha", Item: "tk-2", Stage: "implementer", Checkout: w.runDir},
	} {
		if _, err := w.srv.Tokens.IssueToCheckout(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func (w *runsWorld) exec(q string, args ...any) {
	w.t.Helper()
	if err := w.srv.Store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(q, args...)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
}

// run writes one run of alpha started at ms. A zero ended leaves it live.
func (w *runsWorld) run(id, item string, started, ended int64) {
	w.t.Helper()
	var end any
	if ended != 0 {
		end = ended
	}
	w.exec(`INSERT INTO runs (run_id, project, stage, item, state, started_at, ended_at) VALUES (?, 'alpha', 'implementer', ?, 'running', ?, ?)`,
		id, item, started, end)
}

// call runs one verb from the checkout dir and returns its envelope and
// exit code.
func (w *runsWorld) call(dir string, args ...string) (map[string]any, int) {
	w.t.Helper()
	r := cli.Surface()
	(&cli.Client{Instance: w.dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return dir, nil }}).Attach(r)
	var out bytes.Buffer
	code := r.Run(context.Background(), args, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		w.t.Fatalf("loop %v printed no envelope: %v\n%s", args, err, out.String())
	}
	return env, code
}

func refused(env map[string]any, code, rule string) bool {
	return env["code"] == code && (rule == "" || env["rule"] == rule)
}

// C2: run show of a finished run prints its failure, its checkout and its
// alerts. An unknown run refuses NOT_FOUND no_run, and a run may not read
// another run.
func TestRunShowOverTheSocket(t *testing.T) {
	w := newRunsWorld(t)
	w.t.Chdir(w.coord)
	w.run("r-1", "tk-1", 1000, 5000)
	w.exec(`INSERT INTO checkouts (id, project, item, path, run_id, state, created_at, updated_at)
		VALUES ('c-1', 'alpha', 'tk-1', '/w/loop-alpha-implementer-tk-1-fix-r-1', 'r-1', 'settled', 1000, 5000)`)
	w.exec(`INSERT INTO failures (run_id, project, item, stage, inputs_hash, class, note, recorded_at)
		VALUES ('r-1', 'alpha', 'tk-1', 'implementer', 'h1', 'failed', 'red', 5000)`)
	w.exec(`INSERT INTO alerts (project, kind, subject, dedupe_key, first_at, last_at)
		VALUES ('alpha', 'run_blocked', 'implementer run r-1 on tk-1 finished blocked', 'run_blocked:alpha/tk-1/implementer/h1', 5000, 5000)`)

	env, code := w.call(w.coord, "run", "show", "r-1")
	data, _ := env["data"].(map[string]any)
	if code != 0 || data["run"] != "r-1" || data["checkout"] != "/w/loop-alpha-implementer-tk-1-fix-r-1" {
		t.Fatalf("run show r-1 = %d %v", code, env)
	}
	if f, _ := data["failure"].(map[string]any); f["class"] != "failed" || f["note"] != "red" {
		t.Fatalf("run show failure = %v", data["failure"])
	}
	alerts, _ := data["alerts"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["entry"] != "alert/1" || alerts[0].(map[string]any)["state"] != "open" {
		t.Fatalf("run show alerts = %v", data["alerts"])
	}
	if _, ok := data["lease"]; ok {
		t.Fatalf("a run with no lease answers lease %v", data["lease"])
	}

	if env, _ := w.call(w.coord, "run", "show", "r-9"); !refused(env, cli.CodeNotFound, "no_run") {
		t.Fatalf("run show r-9 = %v", env)
	}
	if env, _ := w.call(w.runDir, "run", "show", "r-1"); !refused(env, cli.CodeCapabilityDenied, "") {
		t.Fatalf("a run's run show = %v", env)
	}
}

func listed(t *testing.T, env map[string]any) []string {
	t.Helper()
	list, ok := env["data"].([]any)
	if !ok {
		t.Fatalf("run list = %v", env)
	}
	out := []string{}
	for _, x := range list {
		out = append(out, x.(map[string]any)["run"].(string))
	}
	return out
}

// C3: run list answers 50 runs when --limit is absent, the live runs with
// --live, and refuses a limit outside 1 to 500. No match exits 1 with [].
func TestRunListOverTheSocket(t *testing.T) {
	w := newRunsWorld(t)
	w.t.Chdir(w.coord)
	for i := 1; i <= 51; i++ {
		w.run(fmt.Sprintf("r-%02d", i), "tk-1", int64(i), int64(i)+1)
	}
	w.run("r-live", "tk-2", 100, 0)

	env, code := w.call(w.coord, "run", "list")
	if got := listed(t, env); code != 0 || len(got) != 50 || got[0] != "r-live" || got[49] != "r-03" {
		t.Fatalf("run list = %d %v", code, got)
	}
	env, code = w.call(w.coord, "run", "list", "--live")
	if got := listed(t, env); code != 0 || len(got) != 1 || got[0] != "r-live" {
		t.Fatalf("run list --live = %d %v", code, got)
	}
	env, code = w.call(w.coord, "run", "list", "--limit", "1", "--stage", "implementer", "--project", "alpha")
	if got := listed(t, env); code != 0 || len(got) != 1 || got[0] != "r-live" {
		t.Fatalf("run list --limit 1 = %d %v", code, got)
	}
	env, code = w.call(w.coord, "run", "list", "--limit", "500")
	if got := listed(t, env); code != 0 || len(got) != 52 {
		t.Fatalf("run list --limit 500 = %d %d runs", code, len(got))
	}
	for _, limit := range []string{"0", "501", "x"} {
		if env, _ := w.call(w.coord, "run", "list", "--limit", limit); !refused(env, cli.CodeUsage, "") {
			t.Fatalf("run list --limit %s = %v", limit, env)
		}
	}
	env, code = w.call(w.coord, "run", "list", "--project", "gamma")
	if got := listed(t, env); code != int(cli.ExitNothing) || len(got) != 0 {
		t.Fatalf("run list --project gamma = %d %v", code, env)
	}
}

// C4: run events answers the run's transitions and audit rows, oldest
// first. A known run with no event exits 1 with [], and an unknown run
// refuses NOT_FOUND no_run.
func TestRunEventsOverTheSocket(t *testing.T) {
	w := newRunsWorld(t)
	w.t.Chdir(w.coord)
	w.run("r-1", "tk-1", 1000, 0)
	w.run("r-2", "tk-2", 1000, 0)
	w.exec(`INSERT INTO audit (at, actor, project, action) VALUES (3000, 'run:r-1', 'alpha', 'host_verb_from_checkout')`)
	w.exec(`INSERT INTO intents (project, item, seq, kind, verb, epoch, state, created_at) VALUES ('alpha', 'tk-1', 1, 'ticket', 'claim', 0, 'applied', 2000)`)
	w.exec(`INSERT INTO transitions (intent_id, project, item, seq, kind, verb, from_status, to_status, epoch, run_id, at)
		VALUES (1, 'alpha', 'tk-1', 1, 'ticket', 'claim', 'todo', 'in_progress', 0, 'r-1', 2000)`)

	env, code := w.call(w.coord, "run", "events", "r-1")
	list, _ := env["data"].([]any)
	if code != 0 || len(list) != 2 {
		t.Fatalf("run events r-1 = %d %v", code, env)
	}
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if first["source"] != "transition" || first["verb"] != "claim" || second["source"] != "audit" || second["action"] != "host_verb_from_checkout" {
		t.Fatalf("run events r-1 = %v", list)
	}
	if env, code := w.call(w.coord, "run", "events", "r-2"); code != int(cli.ExitNothing) || len(env["data"].([]any)) != 0 {
		t.Fatalf("run events r-2 = %d %v", code, env)
	}
	if env, _ := w.call(w.coord, "run", "events", "r-9"); !refused(env, cli.CodeNotFound, "no_run") {
		t.Fatalf("run events r-9 = %v", env)
	}
}
