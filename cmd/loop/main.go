// Command loop is the agent-facing CLI of the loop, and its server.
package main

import (
	"context"
	"io"
	"os"

	"github.com/iantanwx/loop/internal/cli"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run builds the binary's registry: the surface, the socket client for
// every server verb, and serve.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	r := cli.Surface()
	(&cli.Client{Stdin: stdin}).Attach(r)
	if err := r.SetHandler("serve", serveVerb); err != nil {
		panic(err)
	}
	return r.Run(ctx, args, stdout, stderr)
}
