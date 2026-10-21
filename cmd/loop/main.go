// Command loop is the agent-facing CLI of the loop.
package main

import (
	"context"
	"os"

	"github.com/iantanwx/loop/internal/cli"
)

func main() {
	os.Exit(cli.New().Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
