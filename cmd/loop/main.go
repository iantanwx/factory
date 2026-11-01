// Command loop is the agent-facing CLI of the loop, and its server.
package main

import (
	"context"
	"io"
	"os"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
	"github.com/iantanwx/loop/internal/retro"
	"github.com/iantanwx/loop/internal/reviewproof"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run builds the binary's registry: the surface, the socket client for
// every server verb, the host verbs, serve, ui detect, qa plan, design capture, escalation
// judge, retro judge and review proof.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	r := cli.Surface()
	c := &cli.Client{Stdin: stdin}
	c.Attach(r)
	if err := registerRelease(r, c); err != nil {
		panic(err)
	}
	if err := registerGates(r); err != nil {
		panic(err)
	}
	if err := registerUI(r); err != nil {
		panic(err)
	}
	if err := registerQA(r); err != nil {
		panic(err)
	}
	if err := registerDesign(r); err != nil {
		panic(err)
	}
	if err := r.SetHandler("serve", serveVerb); err != nil {
		panic(err)
	}
	// The triage panel runs in the triage run's own process, where the harness
	// logins are (KTD22).
	// The judges read the operator's decisions recorded on the item, which
	// the server holds.
	judging := escalation.Live()
	judging.Recorded = escalation.ServerRecorded(r, c.Remote)
	if err := r.SetHandler("escalation judge", escalation.JudgeVerb(judging)); err != nil {
		panic(err)
	}
	// The retro panel reuses the triage judges and Jev, and reads the
	// record of the run's retro from the server.
	if err := r.SetHandler("retro judge", retro.JudgeVerb(retro.LiveJudging(r, c.Remote))); err != nil {
		panic(err)
	}
	// The proof runs the tests in the reviewer's own checkout, so its
	// output goes to stderr as the tests run.
	if err := r.SetHandler("review proof", reviewproof.Verb(stderr)); err != nil {
		panic(err)
	}
	return r.Run(ctx, args, stdout, stderr)
}
