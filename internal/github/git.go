package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// Git runs every server git command (KTD14). The server never runs an
// authenticated git command inside a checkout: the agent controls the
// checkout's config and hooks. Each command runs in a project's bare mirror
// with an environment built from nothing:
//
//   - GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_NOSYSTEM=1, so no user or
//     system config applies (no insteadOf, no credential helper, no
//     pushurl);
//   - HOME is an empty directory of the server's, and no GIT_* variable of
//     the server's environment passes through;
//   - hooks off (core.hooksPath=/dev/null), no credential helper, no
//     redirect, and only the transport protocols the server allows;
//   - the App token reaches git only as an environment-scoped config entry
//     (GIT_CONFIG_COUNT) that sets http.<url>.extraHeader for the one URL.
//
// The token never enters argv, so `ps` shows no token. Residual risk: a
// process of the same OS user can read /proc/<pid>/environ of git and of
// git-remote-https while a push or a fetch runs. Agents share that user in
// v1 (KTD12). The token is short-lived, scoped to the one repository and
// to the smallest permission set (KTD15), which bounds the exposure.
type Git struct {
	Binary    string
	Home      string   // an empty directory the server owns
	Protocols []string // allowed transport protocols besides file
	// Config holds config entries that each command takes before the
	// hardening entries of env, so an entry here never weakens them.
	Config [][2]string
	// Timeout bounds a local command when ctx has no earlier deadline.
	// Zero means GitTimeout.
	Timeout time.Duration
	// TransferTimeout bounds a fetch, a push or an ls-remote the same
	// way. Zero means GitTransferTimeout.
	TransferTimeout time.Duration
}

// GitTimeout bounds a local git command. The reactor and the verbs pass a
// context with no deadline, and a hung git holds the per-key lock.
const GitTimeout = time.Minute

// GitTransferTimeout bounds a git command that moves objects.
const GitTransferTimeout = 5 * time.Minute

func (g *Git) timeout(args []string) time.Duration {
	if len(args) > 0 {
		switch args[0] {
		case "fetch", "push", "ls-remote":
			if g.TransferTimeout > 0 {
				return g.TransferTimeout
			}
			return GitTransferTimeout
		}
	}
	if g.Timeout > 0 {
		return g.Timeout
	}
	return GitTimeout
}

// gitRun is one git command.
type gitRun struct {
	dir   string
	args  []string
	stdin []byte
	// url and token set http.<url>.extraHeader for this one command.
	url   string
	token string
}

// env builds the environment of one git command from nothing.
func (g *Git) env(r gitRun) []string {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + g.Home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"SSH_ASKPASS=/bin/false",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_PROTOCOL_FROM_USER=0",
		"LC_ALL=C",
	}
	cfg := append([][2]string(nil), g.Config...)
	cfg = append(cfg, [][2]string{
		{"core.hooksPath", "/dev/null"},
		{"core.fsmonitor", "false"},
		{"credential.helper", ""},
		{"http.followRedirects", "false"},
		{"submodule.recurse", "false"},
		{"fetch.recurseSubmodules", "false"},
		{"push.recurseSubmodules", "no"},
		{"push.followTags", "false"},
		{"protocol.allow", "never"},
		{"protocol.file.allow", "always"},
	}...)
	for _, p := range g.Protocols {
		cfg = append(cfg, [2]string{"protocol." + p + ".allow", "always"})
	}
	if r.token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + r.token))
		cfg = append(cfg, [2]string{"http." + r.url + ".extraHeader", "Authorization: Basic " + basic})
	}
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(len(cfg)))
	for i, kv := range cfg {
		env = append(env, fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]), fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]))
	}
	return env
}

// GitError is a failed git command. Stderr never holds the token: git
// does not print an extra header, and the redactor still sees every line
// before it leaves the server.
type GitError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *GitError) Unwrap() error { return e.Err }

func (e *GitError) Error() string {
	return fmt.Sprintf("github: git %s: %v: %s", strings.Join(e.Args, " "), e.Err, strings.TrimSpace(e.Stderr))
}

func (g *Git) run(ctx context.Context, r gitRun) ([]byte, error) {
	bin := g.Binary
	if bin == "" {
		bin = "git"
	}
	ctx, cancel := context.WithTimeout(ctx, g.timeout(r.args))
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, r.args...)
	cmd.Dir = r.dir
	cmd.Env = g.env(r)
	// git starts helpers such as git-remote-https. Kill the whole group, so
	// no helper outlives the call or holds its output open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	if r.stdin != nil {
		cmd.Stdin = bytes.NewReader(r.stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (%v)", ctx.Err(), err)
		}
		return stdout.Bytes(), &GitError{Args: r.args, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

func (g *Git) text(ctx context.Context, r gitRun) (string, error) {
	out, err := g.run(ctx, r)
	return strings.TrimSpace(string(out)), err
}

// Mirror is one project's bare mirror in the instance directory. The
// remote's branches live under refs/remotes/origin/, so config.DefaultRef
// ("origin/<branch>") names the remote default branch here too.
type Mirror struct {
	git *Git
	Dir string
	URL string // the explicit HTTPS URL of the repository
}

// Init creates the bare mirror once. It is safe to run again.
func (m *Mirror) Init(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(m.Dir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.Dir), 0o700); err != nil {
		return err
	}
	_, err := m.git.run(ctx, gitRun{dir: filepath.Dir(m.Dir), args: []string{"init", "--bare", "--quiet", m.Dir}})
	return err
}

// SetDefaultBranch records the remote default branch in the mirror.
func (m *Mirror) SetDefaultBranch(ctx context.Context, branch string) error {
	_, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"config", "loop.defaultBranch", branch}})
	return err
}

// DefaultBranch reads the recorded default branch, or "".
func (m *Mirror) DefaultBranch(ctx context.Context) string {
	b, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"config", "--get", "loop.defaultBranch"}})
	if err != nil {
		return ""
	}
	return b
}

// Fetch brings the named remote branches into refs/remotes/origin/ with
// the token. A branch the remote lacks is no fault: its local ref goes.
func (m *Mirror) Fetch(ctx context.Context, token string, branches ...string) error {
	args := []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--prune", m.URL}
	for _, b := range branches {
		args = append(args, "+refs/heads/"+b+":refs/remotes/origin/"+b)
	}
	_, err := m.git.run(ctx, gitRun{dir: m.Dir, args: args, url: m.URL, token: token})
	var ge *GitError
	if errors.As(err, &ge) && strings.Contains(ge.Stderr, "couldn't find remote ref") {
		// Fetch each branch alone, so one absent branch does not hide the
		// others.
		if len(branches) == 1 {
			_, _ = m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"update-ref", "-d", "refs/remotes/origin/" + branches[0]}})
			return nil
		}
		for _, b := range branches {
			if err := m.Fetch(ctx, token, b); err != nil {
				return err
			}
		}
		return nil
	}
	return err
}

// FetchCheckout copies one commit from a checkout into the mirror, with no
// token. The command runs in the mirror. git starts upload-pack in the
// checkout, which runs no hook, and no token is present to capture.
func (m *Mirror) FetchCheckout(ctx context.Context, checkout, sha, ref string) error {
	_, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"fetch", "--quiet", "--no-tags", "--no-recurse-submodules",
		"--no-write-fetch-head", checkout, "+" + sha + ":" + ref}})
	return err
}

// ServeLocalFetch lets a fetch from the mirror path ask for any object by
// its sha. The setting lives in the mirror's own config, which only the
// server writes, so no checkout's config changes. A checkout fetches with
// no credential, and the mirror holds only this project's objects.
func (m *Mirror) ServeLocalFetch(ctx context.Context) error {
	_, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"config", "uploadpack.allowAnySHA1InWant", "true"}})
	return err
}

// Keep points ref at sha, or deletes ref when sha is "".
func (m *Mirror) Keep(ctx context.Context, ref, sha string) error {
	args := []string{"update-ref", ref, sha}
	if sha == "" {
		args = []string{"update-ref", "-d", ref}
	}
	_, err := m.git.run(ctx, gitRun{dir: m.Dir, args: args})
	return err
}

// Resolve returns the full sha of ref, or "" when the mirror lacks it.
func (m *Mirror) Resolve(ctx context.Context, ref string) string {
	sha, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"rev-parse", "--verify", "--quiet", ref + "^{commit}"}})
	if err != nil {
		return ""
	}
	return sha
}

// Reaches reports whether tip reaches the commit sha. git answers exit 1
// for a commit tip does not reach; any other failure is an error.
func (m *Mirror) Reaches(ctx context.Context, tip, sha string) (bool, error) {
	_, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"merge-base", "--is-ancestor", sha, tip}})
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// Conflicts reports whether head does not merge cleanly with base. git
// merge-tree answers exit 1 for a conflict; any other failure is an error.
func (m *Mirror) Conflicts(ctx context.Context, base, head string) (bool, error) {
	_, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"merge-tree", "--write-tree", "--name-only", "--no-messages", base, head}})
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// Lacking lists the commits of remote whose change head lacks, by patch
// id, as `git cherry` reads it. A rebase of the same commits lacks none.
// Ported from push in scripts/sdlc/machinery-publish-flow.ts:105-125
// (alpha).
func (m *Mirror) Lacking(ctx context.Context, head, remote string) ([]string, error) {
	out, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"cherry", head, remote}})
	if err != nil {
		return nil, err
	}
	var lacking []string
	for _, line := range strings.Split(out, "\n") {
		if sha, ok := strings.CutPrefix(line, "+ "); ok {
			lacking = append(lacking, sha)
		}
	}
	return lacking, nil
}

// Push sends sha to refs/heads/<branch> over the explicit URL with the
// token. It pushes one refspec and no tag, and it leases on old: "" means
// the branch must not exist yet. sha must name a commit: a push of no
// commit would delete the branch, and the loop never deletes one.
func (m *Mirror) Push(ctx context.Context, token, sha, branch, old string) error {
	if !shaRe.MatchString(sha) {
		return fmt.Errorf("github: a push to %s names %q and no commit; the loop never deletes a branch", branch, sha)
	}
	ref := "refs/heads/" + branch
	_, err := m.git.run(ctx, gitRun{dir: m.Dir, url: m.URL, token: token, args: []string{
		"push", "--quiet", "--porcelain", "--no-follow-tags", "--no-recurse-submodules", "--no-verify",
		"--force-with-lease=" + ref + ":" + old, m.URL, sha + ":" + ref,
	}})
	return err
}

// RemoteHead reads refs/heads/<branch> on the remote with the token, or ""
// when the branch is absent.
func (m *Mirror) RemoteHead(ctx context.Context, token, branch string) (string, error) {
	out, err := m.git.text(ctx, gitRun{dir: m.Dir, url: m.URL, token: token,
		args: []string{"ls-remote", "--heads", m.URL, "refs/heads/" + branch}})
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	return strings.TrimSpace(sha), nil
}

// Show reads path at ref. A ref or a path the mirror lacks matches
// fs.ErrNotExist.
func (m *Mirror) Show(ctx context.Context, ref, path string) ([]byte, error) {
	if strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") {
		return nil, fmt.Errorf("github: %q is not a repository path", path)
	}
	sha := m.Resolve(ctx, ref)
	if sha == "" {
		return nil, fmt.Errorf("github: the mirror holds no %s: %w", ref, fs.ErrNotExist)
	}
	out, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"cat-file", "blob", sha + ":" + path}})
	if err != nil {
		var ge *GitError
		if errors.As(err, &ge) {
			return nil, fmt.Errorf("github: %s:%s: %w", ref, path, fs.ErrNotExist)
		}
		return nil, err
	}
	return out, nil
}

// Tree lists the entries tracked directly under dir at ref. A ref or a
// dir the mirror lacks returns no entry and no error.
func (m *Mirror) Tree(ctx context.Context, ref, dir string) ([]config.TreeEntry, error) {
	if strings.HasPrefix(dir, "/") || strings.Contains(dir, "\x00") {
		return nil, fmt.Errorf("github: %q is not a repository path", dir)
	}
	sha := m.Resolve(ctx, ref)
	if sha == "" {
		return nil, nil
	}
	out, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"ls-tree", "-z", sha, "--", dir + "/"}})
	if err != nil {
		return nil, err
	}
	return config.ParseTree(out), nil
}

// AddedBy is the newest first-parent commit reachable from tip whose diff
// against its first parent adds path, or "" when none does. A rename
// into path counts as an add.
func (m *Mirror) AddedBy(ctx context.Context, tip, path string) (string, error) {
	return m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"log", "--first-parent", "--diff-merges=first-parent", "--no-renames",
		"--diff-filter=A", "--no-patch", "--format=%H", "-n", "1", "--end-of-options", tip, "--", path}})
}

// FirstParents lists the first-parent commits after from, through to,
// oldest first. A commit that a merge brought in on another parent is not
// listed.
func (m *Mirror) FirstParents(ctx context.Context, from, to string) ([]string, error) {
	out, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"rev-list", "--first-parent", "--reverse", "--end-of-options",
		to, "^" + from}})
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}

// Commit is one commit as the base audit reads it.
type Commit struct {
	SHA     string
	Parents []string
	Subject string
	Author  string
}

// Commit reads one commit of the mirror.
func (m *Mirror) Commit(ctx context.Context, sha string) (Commit, error) {
	out, err := m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"show", "--no-patch", "--format=%H%x00%P%x00%an%x00%s",
		"--end-of-options", sha}})
	if err != nil {
		return Commit{}, err
	}
	f := strings.SplitN(out, "\x00", 4)
	if len(f) != 4 {
		return Commit{}, fmt.Errorf("github: commit %s reads %q", sha, out)
	}
	return Commit{SHA: f[0], Parents: strings.Fields(f[1]), Author: f[2], Subject: f[3]}, nil
}

// rawTree lists the entries directly in the tree of rev, each name to its
// "<mode> <type> <object>". An empty rev lists none.
func (m *Mirror) rawTree(ctx context.Context, rev string) (map[string]string, error) {
	entries := map[string]string{}
	if rev == "" {
		return entries, nil
	}
	out, err := m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"ls-tree", "-z", rev}})
	if err != nil {
		return nil, err
	}
	for _, rec := range strings.Split(string(out), "\x00") {
		if meta, name, ok := strings.Cut(rec, "\t"); ok {
			entries[name] = meta
		}
	}
	return entries, nil
}

// mkTree writes a tree of entries, as rawTree lists them, and returns it.
func (m *Mirror) mkTree(ctx context.Context, entries map[string]string) (string, error) {
	var b strings.Builder
	for name, meta := range entries {
		fmt.Fprintf(&b, "%s\t%s\x00", meta, name)
	}
	return m.git.text(ctx, gitRun{dir: m.Dir, args: []string{"mktree", "-z"}, stdin: []byte(b.String())})
}

// blob reads the object of one tree entry, as rawTree lists it, or nil
// when the entry is no blob.
func (m *Mirror) blob(ctx context.Context, meta string) ([]byte, error) {
	f := strings.Fields(meta)
	if len(f) != 3 || f[1] != "blob" {
		return nil, nil
	}
	return m.git.run(ctx, gitRun{dir: m.Dir, args: []string{"cat-file", "blob", f[2]}})
}
