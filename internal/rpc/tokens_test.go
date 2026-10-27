package rpc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

func TestC10TheTokenFileNamesTheIssuersInstanceAndBinary(t *testing.T) {
	h := newHarness(t)
	h.tokens.Instance = "/srv/loop"
	h.tokens.LoopBinary = "/srv/loop/current/loop"
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.tokens.IssueToCheckout(context.Background(), IssueRequest{
		RunID: "run-1", Kind: cli.PrincipalRun, Project: "alpha", Item: "T-1", Checkout: checkout,
	}); err != nil {
		t.Fatal(err)
	}
	tf, err := cli.ReadTokenFile(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if tf.Instance != "/srv/loop" || tf.Binary != "/srv/loop/current/loop" {
		t.Fatalf("the token file names instance %q and binary %q", tf.Instance, tf.Binary)
	}
}
