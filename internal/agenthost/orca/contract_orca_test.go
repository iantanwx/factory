//go:build orca

// The agent-host contract against real orca on the devbox. It starts real
// agent sessions, so it spends harness quota and creates and removes
// worktrees. Run it only with the operator's approval:
//
//	LOOP_ORCA_ROOT=<orca workspaces dir for test worktrees> \
//	LOOP_ORCA_REPO=path:<a checkout orca knows> \
//	LOOP_ORCA_MODEL=<the full model id the harness records> \
//	go test -tags orca -run TestOrcaContract -v -timeout 60m ./internal/agenthost/orca/
//
// LOOP_ORCA_BIN defaults to the devbox CLI path. LOOP_ORCA_HARNESS defaults
// to claude. LOOP_ORCA_EFFORT is optional. LOOP_ORCA_HARNESS_ARGS holds
// extra harness arguments, separated by spaces.
package orca_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/orca"
)

const devboxOrca = "/home/operator/.config/orca/cli/bin/orca"

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// procArgs reads the argument vector of every process on this Linux host.
// It serves agenthost.ArgvReader. A process that ends during the scan, or
// that this user cannot read, drops out.
type procArgs struct{}

func (procArgs) ProcessArgs() ([][]string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out [][]string
	for _, e := range entries {
		if !e.IsDir() || strings.Trim(e.Name(), "0123456789") != "" {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		out = append(out, strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"))
	}
	return out, nil
}

type orcaDriver struct{ procArgs }

func (orcaDriver) CheckoutExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func TestOrcaContract(t *testing.T) {
	root, repo, model := os.Getenv("LOOP_ORCA_ROOT"), os.Getenv("LOOP_ORCA_REPO"), os.Getenv("LOOP_ORCA_MODEL")
	if root == "" || repo == "" || model == "" {
		t.Skip("set LOOP_ORCA_ROOT, LOOP_ORCA_REPO and LOOP_ORCA_MODEL to run the orca contract")
	}
	harness := agenthost.Harness(envOr("LOOP_ORCA_HARNESS", string(agenthost.Claude)))
	cfg := orca.Config{
		Bin:          envOr("LOOP_ORCA_BIN", devboxOrca),
		CheckoutRoot: root,
		Repo:         repo,
		QuietPeriod:  20 * time.Second,
		HarnessArgs:  map[agenthost.Harness][]string{},
	}
	if extra := os.Getenv("LOOP_ORCA_HARNESS_ARGS"); extra != "" {
		cfg.HarnessArgs[harness] = strings.Fields(extra)
	}
	n := 0
	agenthost.RunContract(t, func(t *testing.T) agenthost.ContractEnv {
		h, err := orca.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return agenthost.ContractEnv{
			Host: h,
			NewCheckout: func(tb testing.TB) string {
				n++
				name := fmt.Sprintf("loop-contract-%d-%d", time.Now().Unix(), n)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				co, err := h.CreateCheckout(ctx, agenthost.CheckoutSpec{Name: name})
				if err != nil {
					tb.Fatalf("CreateCheckout: %v", err)
				}
				return co.Path
			},
			Harness:     harness,
			Model:       model,
			Effort:      os.Getenv("LOOP_ORCA_EFFORT"),
			Wait:        2 * time.Minute,
			EndedWithin: 5 * time.Minute,
			OutsideRoot: t.TempDir(),
			Driver:      orcaDriver{},
		}
	})
}
