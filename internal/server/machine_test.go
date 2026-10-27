package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/beadsx/fake"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/store"
)

// showAsRun starts a server with opts, issues a run token for item tk-none
// and calls work show on that item from inside the run's checkout.
func showAsRun(t *testing.T, opts Options) map[string]any {
	t.Helper()
	dir := instanceDir(t)
	var srv *Server
	opts.Setup = []func(*Server) error{func(s *Server) error { srv = s; return nil }}
	start(t, dir, opts, true)
	checkout := t.TempDir()
	if _, err := srv.Tokens.IssueToCheckout(context.Background(), rpc.IssueRequest{
		RunID: "run-1", Kind: cli.PrincipalRun, Project: "alpha", Item: "tk-none", Checkout: checkout,
	}); err != nil {
		t.Fatal(err)
	}
	t.Chdir(checkout)
	r := cli.Surface()
	(&cli.Client{Instance: dir, Budget: 5 * time.Second, Getwd: func() (string, error) { return checkout, nil }}).Attach(r)
	var out bytes.Buffer
	r.Run(context.Background(), []string{"work", "show", "tk-none"}, &out, io.Discard)
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("no envelope: %v\n%s", err, out.String())
	}
	return env
}

// With Options.Beads the server serves the machine's verbs. Without it
// they stay NOT_IMPLEMENTED.
func TestBeadsWiresTheMachineVerbs(t *testing.T) {
	bx := fake.New("tk")
	env := showAsRun(t, Options{Beads: func(string) (beadsx.Client, error) { return bx, nil }})
	if env["code"] != cli.CodeNotFound || env["rule"] != "no_item" {
		t.Fatalf("work show on a missing item: %v", env)
	}
	env = showAsRun(t, Options{})
	if env["code"] != cli.CodeNotImplemented {
		t.Fatalf("work show without beads: %v", env)
	}
}

// C13: the server binds admission's wait to work show. A ticket that run-1
// holds, with a crashed and a stalled failure, answers both, the lease and
// a failure_limit of 2.
func TestWorkShowAnswersTheStoredWait(t *testing.T) {
	w := newGHWorld(t)
	id := w.claim()
	if err := w.srv.Store.Write(context.Background(), func(tx *store.Tx) error {
		_, err := tx.Exec(`INSERT INTO failures (run_id, project, item, stage, inputs_hash, class, note, recorded_at) VALUES
			('r-1', 'alpha', ?, 'implementer', 'h1', 'crashed', '', 100), ('r-2', 'alpha', ?, 'implementer', 'h1', 'stalled', '', 200)`, id, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	env := w.run("work", "show", id)
	data, _ := env["data"].(map[string]any)
	wait, _ := data["wait"].(map[string]any)
	if env["ok"] != true || data["item"] != id || wait == nil {
		t.Fatalf("work show = %v", env)
	}
	if l, _ := wait["lease"].(map[string]any); l["run"] != "run-1" {
		t.Fatalf("wait lease = %v", wait["lease"])
	}
	fs, _ := wait["failures"].([]any)
	if len(fs) != 2 || fs[0].(map[string]any)["class"] != "crashed" || fs[1].(map[string]any)["class"] != "stalled" ||
		fs[0].(map[string]any)["stage"] != "implementer" || fs[1].(map[string]any)["inputs"] != "h1" {
		t.Fatalf("wait failures = %v", wait["failures"])
	}
	if wait["failure_limit"] != float64(2) {
		t.Fatalf("wait failure_limit = %v", wait["failure_limit"])
	}
}
