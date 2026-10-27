// Package orcalane is the orca-acceptance native job (ADR 0008). Each day it
// runs the orca lane binaries that the installed release ships against the
// host's real orca, and it reports a lane that does not pass as one loop
// fault.
package orcalane

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/agenthost/orca"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/rpc"
)

const (
	JobName = "orca-acceptance"
	// LaneModel is the contract's LOOP_ORCA_MODEL: a full model id, because
	// the receipt never verifies an alias.
	LaneModel    = "claude-opus-5-5"
	LaneDeadline = 120 * time.Minute
	KeepLogs     = 30
)

// The run pattern of each lane binary. No lane binary runs unfiltered.
const (
	ContractRun = "^(TestOrcaContract|TestOrcaAgentRunsShellCommands)$"
	ScenarioRun = "^TestF1RealAgentsTakeATicketFromClaimToMerge$"
)

// laneExitSpent is a lane binary's exit on a spent quota window
// (agenthosttest.LaneExitSpent).
const laneExitSpent = 3

// sweepBound bounds the sweep after the lane, which runs even when the
// lane's context has ended. A server stop waits for it, and the next tick
// sweeps first, so it stays short.
const sweepBound = 30 * time.Second

// Runner runs one lane binary. Production runs a process. Tests script it.
type Runner interface {
	Run(ctx context.Context, bin string, args, env []string, out io.Writer) (exit int, err error)
}

// Orca is the slice of the orca CLI the job calls.
type Orca interface {
	Version(ctx context.Context) (string, error)
	RepoKnown(ctx context.Context, path string) (bool, error)
	RepoAdd(ctx context.Context, path string) error
	Worktrees(ctx context.Context, repoPath string) ([]string, error)
	CloseTerminals(ctx context.Context, worktree string) error
	RemoveWorktree(ctx context.Context, worktree string) error
}

// Fetcher puts the lane binaries of version into dir, verified, or says the
// release has none.
type Fetcher interface {
	FetchLane(ctx context.Context, version, platform, dir string) (shipped bool, err error)
}

// Faults is fault.Service's Report.
type Faults interface {
	Report(ctx context.Context, p rpc.Principal, text string) (fault.Fault, error)
}

// Job is the orca-acceptance instance job.
type Job struct {
	Layout  release.Layout
	Host    func(context.Context) (*config.HostConfig, error)
	Runner  Runner
	Orca    Orca
	Fetcher Fetcher
	Faults  Faults
	Now     func() time.Time
}

// Outcome is the result of one tick: Green, Skipped, Failed, TimedOut or
// Cancelled. Each tick yields exactly one, and at most one fault.
type Outcome interface{ outcome() }

// Green is a lane whose two binaries exited 0. It adds no fault.
type Green struct{}

// Skipped is a lane binary that exited 3 on a spent quota window, or a
// release that predates the lane. Why is the quota line or the reason.
type Skipped struct{ Why string }

// Failed is a lane binary that exited other than 0 or 3, or a fetch or
// setup step that failed. Tests are its --- FAIL: names, and Why the stop
// reason when it names none.
type Failed struct {
	Tests []string
	Exit  int
	Why   string
}

// TimedOut is a lane that passed LaneDeadline.
type TimedOut struct{}

// Cancelled is a tick whose context ended before the deadline, such as a
// server stop. It adds no fault: the next tick runs the lane again.
type Cancelled struct{}

func (Green) outcome()     {}
func (Skipped) outcome()   {}
func (Failed) outcome()    {}
func (TimedOut) outcome()  {}
func (Cancelled) outcome() {}

// tick is the state of one Tick: the installed version, and the log.
type tick struct {
	version string
	log     bytes.Buffer
}

func (t *tick) logf(format string, args ...any) {
	fmt.Fprintf(&t.log, format+"\n", args...)
}

// Tick is the instance job. It returns an error only when it cannot report.
func (j *Job) Tick(ctx context.Context) error {
	var t tick
	out := j.lane(ctx, &t)
	path, logErr := j.writeLog(&t)
	text := j.faultText(ctx, out, t.version, path)
	if text == "" {
		return logErr
	}
	_, err := j.Faults.Report(ctx, rpc.Principal{Kind: cli.PrincipalHost}, text)
	return errors.Join(err, logErr)
}

// lane does steps 1 to 6 of the tick, and returns its outcome.
func (j *Job) lane(ctx context.Context, t *tick) Outcome {
	failed := func(format string, args ...any) Outcome {
		why := fmt.Sprintf(format, args...)
		t.logf("%s", why)
		return Failed{Why: why}
	}
	v, err := j.Layout.CurrentVersion()
	if err != nil {
		return failed("read the installed version: %v", err)
	}
	t.version = v
	host, err := j.Host(ctx)
	if err != nil {
		return failed("read host config: %v", err)
	}
	dir := j.Layout.VersionDir(v)
	contract := filepath.Join(dir, release.LaneContractFile)
	scenario := filepath.Join(dir, release.LaneAcceptanceFile)
	if !exists(contract) || !exists(scenario) {
		platform := runtime.GOOS + "/" + runtime.GOARCH
		t.logf("fetch the lane binaries of loop %s for %s", v, platform)
		shipped, err := j.Fetcher.FetchLane(ctx, v, platform, dir)
		if err != nil {
			return failed("fetch the lane binaries of loop %s: %v", v, err)
		}
		if !shipped {
			why := fmt.Sprintf("loop %s ships no orca lane", v)
			t.logf("%s", why)
			return Skipped{Why: why}
		}
	}
	fixture := filepath.Join(j.Layout.Dir, JobName, "fixture")
	if err := j.fixture(ctx, t, fixture); err != nil {
		return failed("make the lane fixture: %v", err)
	}
	if err := j.sweep(ctx, t, fixture); err != nil {
		return failed("sweep the lane fixture: %v", err)
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sweepBound)
		defer cancel()
		if err := j.sweep(sctx, t, fixture); err != nil {
			t.logf("sweep the lane fixture after the lane: %v", err)
		}
	}()

	env := laneEnv(j.Layout, host, v, fixture)
	lctx, cancel := context.WithTimeout(ctx, LaneDeadline)
	defer cancel()
	for _, b := range []struct{ bin, run string }{{contract, ContractRun}, {scenario, ScenarioRun}} {
		name := filepath.Base(b.bin)
		var out bytes.Buffer
		exit, err := j.Runner.Run(lctx, b.bin, []string{"-test.run", b.run}, env, &out)
		t.logf("== %s exit %d ==", name, exit)
		t.log.Write(out.Bytes())
		switch {
		case ctx.Err() != nil:
			return Cancelled{}
		case errors.Is(err, context.DeadlineExceeded) || lctx.Err() != nil:
			return TimedOut{}
		case err != nil:
			return failed("run %s: %v", name, err)
		case exit == laneExitSpent:
			if line := quotaLine(out.Bytes()); line != "" {
				return Skipped{Why: line}
			}
			return Skipped{Why: fmt.Sprintf("%s exited %d", name, exit)}
		case exit != 0:
			return Failed{Tests: failedTests(out.Bytes()), Exit: exit, Why: fmt.Sprintf("%s exited %d", name, exit)}
		}
	}
	return Green{}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// fixture makes the lane fixture when it is absent, and registers it with
// orca when orca does not know it. A new fixture is made in a temporary
// directory and renamed into place, so the path holds a whole fixture or
// nothing.
func (j *Job) fixture(ctx context.Context, t *tick, path string) error {
	if !orca.IsLaneFixture(path) {
		if exists(path) {
			return fmt.Errorf("%s exists and holds no .git/%s marker", path, orca.LaneFixtureMarker)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		tmp, err := os.MkdirTemp(filepath.Dir(path), ".fixture-")
		if err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(tmp) }()
		if err := initFixture(ctx, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
		t.logf("made the lane fixture %s", path)
	}
	known, err := j.Orca.RepoKnown(ctx, path)
	if err != nil {
		return err
	}
	if !known {
		if err := j.Orca.RepoAdd(ctx, path); err != nil {
			return err
		}
		t.logf("registered the lane fixture with orca")
	}
	return nil
}

// initFixture makes a repository with one commit on main and the marker,
// with no global or system git config.
func initFixture(ctx context.Context, dir string) error {
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=loop", "-c", "user.email=loop@localhost", "-c", "commit.gpgsign=false",
			"commit", "-q", "--allow-empty", "-m", "orca acceptance lane fixture"},
	} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[len(args)-1], err, bytes.TrimSpace(out))
		}
	}
	return os.WriteFile(filepath.Join(dir, ".git", orca.LaneFixtureMarker), nil, 0o600)
}

// sweep closes the terminals of and removes each worktree of the fixture
// repository. The fixture belongs to the lane alone, so each of its
// worktrees is the lane's. It never removes the fixture's own checkout.
func (j *Job) sweep(ctx context.Context, t *tick, fixture string) error {
	trees, err := j.Orca.Worktrees(ctx, fixture)
	if err != nil {
		return err
	}
	for _, wt := range trees {
		if filepath.Clean(wt) == fixture {
			continue
		}
		if err := j.Orca.CloseTerminals(ctx, wt); err != nil {
			return err
		}
		if err := j.Orca.RemoveWorktree(ctx, wt); err != nil {
			return err
		}
		t.logf("swept the worktree %s", wt)
	}
	return nil
}

// laneEnv is the server's environment with every LOOP_ value removed, and
// the lane's values set from host config, the layout and the constants.
func laneEnv(l release.Layout, host *config.HostConfig, v, fixture string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "LOOP_") })
	env = append(env,
		"LOOP_ORCA_ROOT="+host.AgentHost.CheckoutRoot,
		"LOOP_ORCA_REPO=path:"+fixture,
		"LOOP_ORCA_MODEL="+LaneModel,
		"LOOP_BD="+l.Bin("bd"),
		"LOOP_DOLT="+l.Bin("dolt"),
		"LOOP_ACCEPTANCE_LOOP="+l.Binary(v),
	)
	if host.AgentHost.Binary != "" {
		env = append(env, "LOOP_ORCA_BIN="+host.AgentHost.Binary)
	}
	return env
}

var (
	failLine  = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	quotaRe   = regexp.MustCompile(`^quota: \S+ window \S+ at [0-9.]+% (resets \S+|reset unknown)$`)
	safeOrcaV = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,64}$`)
)

// failedTests is the name of each --- FAIL: line of out, and nothing else
// of the line.
func failedTests(out []byte) []string {
	var names []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if m := failLine.FindStringSubmatch(sc.Text()); m != nil {
			names = append(names, m[1])
		}
	}
	return names
}

// quotaLine is the first line of out in the lane gate's quota form.
func quotaLine(out []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if quotaRe.MatchString(sc.Text()) {
			return sc.Text()
		}
	}
	return ""
}

// writeLog writes the tick's log to orca-acceptance/logs/<UTC stamp>.log at
// mode 0600, then removes the oldest logs by name so KeepLogs remain.
func (j *Job) writeLog(t *tick) (string, error) {
	dir := filepath.Join(j.Layout.Dir, JobName, "logs")
	path := filepath.Join(dir, j.Now().UTC().Format("20060102T150405.000Z")+".log")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return path, err
	}
	if err := os.WriteFile(path, t.log.Bytes(), 0o600); err != nil {
		return path, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return path, err
	}
	logs, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil {
		return path, err
	}
	slices.Sort(logs)
	for _, old := range logs[:max(0, len(logs)-KeepLogs)] {
		if err := os.Remove(old); err != nil {
			return path, err
		}
	}
	return path, nil
}

// faultText is the fault of a non-green outcome, or "" for Green and
// Cancelled. It holds the header, the --- FAIL: lines or the quota line or
// the stop reason, and the log path. It never holds other lane output.
func (j *Job) faultText(ctx context.Context, out Outcome, version, log string) string {
	var word string
	var lines []string
	switch o := out.(type) {
	case Skipped:
		word, lines = "skipped", []string{o.Why}
	case Failed:
		word = "failed"
		for _, name := range o.Tests {
			lines = append(lines, "--- FAIL: "+name)
		}
		if len(lines) == 0 {
			lines = []string{o.Why}
		}
	case TimedOut:
		word, lines = "timed out", []string{fmt.Sprintf("the lane passed its %v deadline", LaneDeadline)}
	default:
		return ""
	}
	if version == "" {
		version = "unknown"
	}
	ov, err := j.Orca.Version(ctx)
	if ov = strings.TrimSpace(ov); err != nil || !safeOrcaV.MatchString(ov) {
		ov = "unknown"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "orca-acceptance %s on loop %s, orca %s.\n", word, version, ov)
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("Log: " + log + "\n")
	return b.String()
}
