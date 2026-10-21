// Command loop is the agent-facing CLI of the loop, and its server.
package main

import (
	"context"
	"io"
	"os"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/escalation"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run builds the binary's registry: the surface, the socket client for
// every server verb, the host verbs, serve and escalation judge.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	r := cli.Surface()
	c := &cli.Client{Stdin: stdin}
	c.Attach(r)
	if err := registerRelease(r, c); err != nil {
		panic(err)
	}
	if err := r.SetHandler("serve", serveVerb); err != nil {
		panic(err)
	}
	// The triage panel runs in the fixer's own process, where the harness
	// logins are (KTD22).
	if err := r.SetHandler("escalation judge", escalation.JudgeVerb(escalation.Live())); err != nil {
		panic(err)
	}
	return r.Run(ctx, args, stdout, stderr)
}
