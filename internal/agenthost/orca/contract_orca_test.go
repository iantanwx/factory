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
// to claude. LOOP_ORCA_EFFORT is optional.
package orca_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/agenthosttest"
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
// It serves agenthosttest.ArgvReader. A process that ends during the scan, or
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
	}
	n := 0
	agenthosttest.RunContract(t, func(t *testing.T) agenthosttest.ContractEnv {
		h, err := orca.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return agenthosttest.ContractEnv{
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

// An agent that the loop starts runs with no person at its terminal, so it
// must run shell commands without a permission prompt, whatever the
// operator's own harness settings deny. The agent runs one command that
// leaves a file, and the file proves the command ran.
func TestOrcaAgentRunsShellCommands(t *testing.T) {
	root, repo, model := os.Getenv("LOOP_ORCA_ROOT"), os.Getenv("LOOP_ORCA_REPO"), os.Getenv("LOOP_ORCA_MODEL")
	if root == "" || repo == "" || model == "" {
		t.Skip("set LOOP_ORCA_ROOT, LOOP_ORCA_REPO and LOOP_ORCA_MODEL to run the orca contract")
	}
	bin := envOr("LOOP_ORCA_BIN", devboxOrca)
	h, err := orca.New(orca.Config{Bin: bin, CheckoutRoot: root, Repo: repo, QuietPeriod: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	co, err := h.CreateCheckout(ctx, agenthost.CheckoutSpec{Name: fmt.Sprintf("loop-contract-shell-%d", time.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	var sess agenthost.Session
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if sess.ID.Valid() {
			_ = h.Close(ctx, sess.ID)
		}
		_ = exec.CommandContext(ctx, bin, "terminal", "close", "--worktree", "path:"+co.Path, "--all", "--json").Run()
		if err := h.RemoveCheckout(ctx, co.Path); err != nil {
			t.Errorf("RemoveCheckout %s: %v", co.Path, err)
		}
	})
	proof := filepath.Join(co.Path, "ran-a-shell-command")
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	body := "Run this one shell command with your shell tool, then stop: touch " + proof + "\n"
	if err := os.WriteFile(prompt, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sess, err = h.Start(ctx, agenthost.StartSpec{Kind: agenthost.KindStage, Checkout: co.Path,
		Harness: agenthost.Harness(envOr("LOOP_ORCA_HARNESS", string(agenthost.Claude))), Model: model, PromptFile: prompt})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		if _, err := os.Stat(proof); err == nil {
			return
		}
	}
	t.Fatalf("the agent ran no shell command in 3 minutes; %s is missing", proof)
}
