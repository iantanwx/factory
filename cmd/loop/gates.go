package main

import (
	"context"
	"os"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/gates"
)

func registerGates(r *cli.Registry) error {
	return r.SetHandler("gates", gatesVerb)
}

// GatesReport is the JSON of `loop gates`.
type GatesReport struct {
	Root    string         `json:"root"`
	Base    string         `json:"base"`
	Gates   []string       `json:"gates"`
	Results []gates.Result `json:"results,omitempty"`
}

// gatesVerb runs the gates in the checkout around the working directory.
// Each gate's output goes to stderr as it runs, so a CI log shows it.
func gatesVerb(ctx context.Context, inv cli.Invocation) cli.Result {
	cwd, err := os.Getwd()
	if err != nil {
		return cli.Internal(err)
	}
	repo, err := gates.Open(ctx, cwd, inv.Flags["base"], os.Stderr)
	if err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_checkout", err.Error())
	}
	selected, err := gates.Select(repo.Config, inv.Flags["docs"] == "true", inv.Flags["fast"] == "true", inv.Flags["only"])
	if err != nil {
		return cli.Reject(cli.CodeUsage, "unknown_gate", err.Error())
	}
	rep := GatesReport{Root: repo.Root, Base: repo.Base}
	for _, g := range selected {
		rep.Gates = append(rep.Gates, g.Name)
	}
	if inv.Flags["list"] == "true" {
		return cli.Found(rep)
	}
	rep.Results = gates.Run(ctx, repo, selected)
	if last := rep.Results[len(rep.Results)-1]; !last.OK {
		return cli.Reject(cli.CodeGatesFailed, last.Gate, "the "+last.Gate+" gate failed; stderr names each finding")
	}
	return cli.Found(rep)
}
