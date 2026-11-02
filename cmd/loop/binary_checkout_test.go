package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
)

// TestBinaryBelowACheckout runs TestBinary from a directory below a run's
// checkout. The checkout's .loop/token names a live instance. TestBinary
// must reach no server from there, so its no-server case still waits out
// its budget and exits 4.
func TestBinaryBelowACheckout(t *testing.T) {
	bin := binary(t, testVersion)
	live := filepath.Join(t.TempDir(), "live")
	if err := os.Mkdir(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.EnsureAdminCredential(live); err != nil {
		t.Fatal(err)
	}
	serve(t, bin, live)
	want(t, loop(t, bin, live, t.TempDir(), "", "status"), 0, "")

	checkout := t.TempDir()
	if err := cli.WriteTokenFile(checkout, cli.TokenFile{Token: "copied-run-token", RunID: "r-copied",
		Kind: cli.PrincipalRun, Project: "alpha", Item: "T-A", Instance: live}); err != nil {
		t.Fatal(err)
	}
	below := filepath.Join(checkout, "cmd", "loop")
	if err := os.MkdirAll(below, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(below)
	TestBinary(t)
}
