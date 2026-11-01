package orcalane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/agenthost/orca"
	"github.com/iantanwx/loop/internal/release"
)

// ExecRunner runs a lane binary as a process in Dir. The end of ctx kills
// it.
type ExecRunner struct{ Dir string }

func (r ExecRunner) Run(ctx context.Context, bin string, args, env []string, out io.Writer) (int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = r.Dir, env, out, out
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return -1, ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// worktreeListLimit is the row cap of `orca worktree list`. The fixture
// holds a few worktrees at most.
const worktreeListLimit = "1000"

// OrcaCLI is Orca over the orca binary.
type OrcaCLI struct{ Runner orca.Runner }

// envelope is orca's JSON answer, narrowed to what the job reads.
type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call runs one orca command. A refusal whose code is in ok reads as
// success with no result.
func (o OrcaCLI) call(ctx context.Context, ok []string, args ...string) (json.RawMessage, error) {
	verb := strings.Join(args[:2], " ")
	out, err := o.Runner.Run(ctx, args)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(out.Stdout, &env); err != nil {
		return nil, fmt.Errorf("orca %s printed no valid JSON (exit %d)", verb, out.Code)
	}
	if !env.OK {
		var code, msg string
		if env.Error != nil {
			code, msg = env.Error.Code, env.Error.Message
		}
		for _, c := range ok {
			if code == c {
				return nil, nil
			}
		}
		return nil, &orca.Error{Verb: verb, Code: code, Message: msg, Exit: out.Code}
	}
	return env.Result, nil
}

func (o OrcaCLI) Version(ctx context.Context) (string, error) {
	out, err := o.Runner.Run(ctx, []string{"--version"})
	if err != nil {
		return "", err
	}
	if out.Code != 0 {
		return "", fmt.Errorf("orca --version exited %d", out.Code)
	}
	return strings.TrimSpace(string(out.Stdout)), nil
}

func (o OrcaCLI) RepoKnown(ctx context.Context, path string) (bool, error) {
	res, err := o.call(ctx, []string{"repo_not_found"}, "repo", "show", "--repo", "path:"+path, "--json")
	return res != nil, err
}

func (o OrcaCLI) RepoAdd(ctx context.Context, path string) error {
	_, err := o.call(ctx, nil, "repo", "add", "--path", path, "--json")
	return err
}

// Worktrees lists the worktrees of the repository at repoPath, except its
// main worktree.
func (o OrcaCLI) Worktrees(ctx context.Context, repoPath string) ([]string, error) {
	res, err := o.call(ctx, nil, "worktree", "list", "--repo", "path:"+repoPath, "--limit", worktreeListLimit, "--json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Worktrees []struct {
			Path           string `json:"path"`
			IsMainWorktree bool   `json:"isMainWorktree"`
		} `json:"worktrees"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return nil, fmt.Errorf("orca worktree list: %w", err)
	}
	var paths []string
	for _, w := range list.Worktrees {
		if w.Path != "" && !w.IsMainWorktree && filepath.Clean(w.Path) != filepath.Clean(repoPath) {
			paths = append(paths, w.Path)
		}
	}
	return paths, nil
}

func (o OrcaCLI) CloseTerminals(ctx context.Context, worktree string) error {
	_, err := o.call(ctx, []string{orca.CodeNotFound}, "terminal", "close", "--worktree", "path:"+worktree, "--all", "--json")
	return err
}

func (o OrcaCLI) RemoveWorktree(ctx context.Context, worktree string) error {
	_, err := o.call(ctx, []string{orca.CodeNotFound}, "worktree", "rm", "--worktree", "path:"+worktree, "--force", "--json")
	return err
}

// ReleaseFetcher fetches the lane binaries from the release source, and
// verifies them with the release keys this binary embeds.
type ReleaseFetcher struct {
	Source release.Source
	Keys   []release.PublicKey
}

func (f ReleaseFetcher) FetchLane(ctx context.Context, version, platform, dir string) (bool, error) {
	return release.FetchLane(ctx, f.Source, f.Keys, version, platform, dir)
}
