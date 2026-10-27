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
// --update lowers a baseline, so it names one gate that has one, and the
// verb refuses it otherwise before any gate runs.
func gatesVerb(ctx context.Context, inv cli.Invocation) cli.Result {
	update, only := inv.Flags["update"] == "true", inv.Flags["only"]
	if update && only == "" {
		return cli.Reject(cli.CodeUsage, "update_needs_only", "--update lowers the baseline of one gate; name it with --only <gate>")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return cli.Internal(err)
	}
	repo, err := gates.Open(ctx, cwd, inv.Flags["base"], os.Stderr)
	if err != nil {
		return cli.Reject(cli.CodeUsage, "not_a_checkout", err.Error())
	}
	selected, err := gates.Select(repo.Config, inv.Flags["docs"] == "true", inv.Flags["fast"] == "true", only)
	if err != nil {
		return cli.Reject(cli.CodeUsage, "unknown_gate", err.Error())
	}
	if update && !gates.Updatable(repo.Config, selected[0]) {
		return cli.Reject(cli.CodeUsage, "no_baseline", "the "+only+" gate has no baseline to lower; --update applies to standards and to a gate whose command names a baseline in .loop/config.yaml")
	}
	repo.Update = update
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
