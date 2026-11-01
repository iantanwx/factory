package main

import (
	"context"
	"os"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/frontend"
	"github.com/iantanwx/loop/internal/gitx"
)

func registerUI(r *cli.Registry) error {
	return r.SetHandler("ui detect", uiDetectVerb)
}

// UIDetection is the JSON of `loop ui detect`.
type UIDetection struct {
	Signals  []frontend.Signal `json:"signals"`
	Proposed UIProposal        `json:"proposed"`
}

// UIProposal is the proposed ui block, each glob as the config spells it.
type UIProposal struct {
	Kind  config.UIKind `json:"kind"`
	Paths []string      `json:"paths"`
}

// uiDetectVerb finds the frontend signals in the tracked paths of the
// checkout around the working directory, at HEAD. An untracked file shows
// nothing.
func uiDetectVerb(ctx context.Context, _ cli.Invocation) cli.Result {
	cwd, err := os.Getwd()
	if err != nil {
		return cli.Internal(err)
	}
	if _, err := gitx.Run(ctx, cwd, "rev-parse", "--show-toplevel"); err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_checkout", "run loop ui detect inside a git checkout")
	}
	tree, err := frontend.GitTree(ctx, cwd, "HEAD")
	if err != nil {
		return cli.Reject(cli.CodeUsage, "no_commit", "the checkout has no commit at HEAD; commit the tree first")
	}
	d := frontend.Detect(tree)
	if d.Proposed == nil {
		return cli.Nothing()
	}
	out := UIDetection{Signals: d.Signals, Proposed: UIProposal{Kind: d.Proposed.Kind}}
	for _, g := range d.Proposed.Paths {
		out.Proposed.Paths = append(out.Proposed.Paths, g.String())
	}
	return cli.Found(out)
}
