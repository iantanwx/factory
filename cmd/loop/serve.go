package main

import (
	"context"
	"errors"
	"os/signal"
	"syscall"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/server"
)

// serveVerb runs the server until SIGTERM or SIGINT, then stops it
// gracefully. It prints its one envelope when the server has stopped.
func serveVerb(ctx context.Context, inv cli.Invocation) cli.Result {
	dir := inv.Flags["instance"]
	if dir == "" {
		var err error
		if dir, err = cli.InstanceDir(); err != nil {
			return cli.Refuse(cli.Refusal{Code: cli.CodeInternal, Rule: "instance_dir", Hint: err.Error()})
		}
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	err := server.Run(ctx, server.Options{Instance: dir})
	switch {
	case errors.Is(err, rpc.ErrInstanceLocked), errors.Is(err, rpc.ErrServerRunning):
		return cli.Refuse(cli.Refusal{Code: cli.CodeInternal, Rule: "instance_locked", Hint: err.Error()})
	case err != nil:
		return cli.Refuse(cli.Refusal{Code: cli.CodeInternal, Rule: "serve_failed", Hint: err.Error()})
	}
	return cli.Found(map[string]any{"instance": dir, "stopped": true})
}
