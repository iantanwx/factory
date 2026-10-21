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
