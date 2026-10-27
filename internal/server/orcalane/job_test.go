package orcalane_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost/orca"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/fault"
	"github.com/iantanwx/loop/internal/release"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/server/orcalane"
)

const version = "v1.2.3"

const (
	contractRun = "^(TestOrcaContract|TestOrcaAgentRunsShellCommands)$"
	f1Run       = "^TestF1RealAgentsTakeATicketFromClaimToMerge$"
)

// script is one lane binary's scripted run.
type script func(ctx context.Context, out io.Writer) (int, error)

func exits(code int, output string) script {
	return func(_ context.Context, out io.Writer) (int, error) {
		_, _ = io.WriteString(out, output)
		return code, nil
	}
}

type call struct {
	Bin  string
	Args []string
	Env  []string
}

// world is one instance directory with a job over scripted parts. Every
// scripted part records into one event list, in call order.
type world struct {
	t       *testing.T
	layout  release.Layout
	host    *config.HostConfig
	job     *orcalane.Job
	mu      sync.Mutex
	events  []string
	runs    []call
	scripts map[string]script
	known   bool
	adds    int
	trees   map[string][]string
	fetch   func(dir string) (bool, error)
	fetches int
	faults  []reported
}

type reported struct {
	P    rpc.Principal
	Text string
}

func (w *world) event(format string, args ...any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.events = append(w.events, fmt.Sprintf(format, args...))
}

func (w *world) Run(ctx context.Context, bin string, args, env []string, out io.Writer) (int, error) {
	w.event("run %s", filepath.Base(bin))
	w.mu.Lock()
	w.runs = append(w.runs, call{bin, slices.Clone(args), slices.Clone(env)})
	s := w.scripts[filepath.Base(bin)]
	w.mu.Unlock()
	if s == nil {
		return 0, nil
	}
	return s(ctx, out)
}

func (w *world) Version(context.Context) (string, error) { return "1.4.241", nil }

func (w *world) RepoKnown(_ context.Context, path string) (bool, error) {
	w.event("repo show %s", path)
	return w.known, nil
}

func (w *world) RepoAdd(_ context.Context, path string) error {
	w.event("repo add %s", path)
	w.adds++
	w.known = true
	return nil
}

func (w *world) Worktrees(_ context.Context, repo string) ([]string, error) {
	w.event("worktrees %s", repo)
	return w.trees[repo], nil
}

func (w *world) CloseTerminals(_ context.Context, wt string) error {
	w.event("close %s", wt)
	return nil
}

func (w *world) RemoveWorktree(_ context.Context, wt string) error {
	w.event("remove %s", wt)
	return nil
}

func (w *world) FetchLane(_ context.Context, v, platform, dir string) (bool, error) {
	w.event("fetch %s %s", v, platform)
	w.fetches++
	if w.fetch == nil {
		return false, errors.New("no release source")
	}
	return w.fetch(dir)
}

func (w *world) Report(_ context.Context, p rpc.Principal, text string) (fault.Fault, error) {
	w.faults = append(w.faults, reported{p, text})
	return fault.Fault{ID: fmt.Sprintf("F-%d", len(w.faults))}, nil
}

// newWorld installs version with both lane binaries, so no tick fetches.
func newWorld(t *testing.T) *world {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "instance")
	l := release.Layout{Dir: dir}
	if err := os.MkdirAll(l.VersionDir(version), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{release.BinaryName, release.LaneContractFile, release.LaneAcceptanceFile} {
		writeFile(t, filepath.Join(l.VersionDir(version), name), "#!/bin/sh\n", 0o755)
	}
	if err := os.Symlink(filepath.Join(release.VersionsDir, version), filepath.Join(dir, release.CurrentLink)); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultHost(dir)
	cfg.AgentHost = config.AgentHostConfig{Kind: "orca", Binary: "/opt/orca/bin/orca", CheckoutRoot: "/srv/checkouts"}
	w := &world{t: t, layout: l, host: cfg, scripts: map[string]script{}, trees: map[string][]string{}}
	w.job = &orcalane.Job{
		Layout:  l,
		Host:    func(context.Context) (*config.HostConfig, error) { return w.host, nil },
		Runner:  w,
		Orca:    w,
		Fetcher: w,
		Faults:  w,
		Now:     clock(time.Date(2026, 10, 27, 17, 30, 0, 0, time.UTC)),
	}
	return w
}

// clock starts at t0 and moves one minute on each read.
func clock(t0 time.Time) func() time.Time {
	var mu sync.Mutex
	now := t0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Minute)
		return now
	}
}

func writeFile(t *testing.T, path, data string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), perm); err != nil {
		t.Fatal(err)
	}
}

func (w *world) tick(ctx context.Context) {
	w.t.Helper()
	if err := w.job.Tick(ctx); err != nil {
		w.t.Fatalf("tick: %v", err)
	}
}

func (w *world) fixture() string { return filepath.Join(w.layout.Dir, "orca-acceptance", "fixture") }

func (w *world) logs() []string {
	w.t.Helper()
	paths, err := filepath.Glob(filepath.Join(w.layout.Dir, "orca-acceptance", "logs", "*.log"))
	if err != nil {
		w.t.Fatal(err)
	}
	return paths
}

func (w *world) binsRun() []string {
	var out []string
	for _, r := range w.runs {
		out = append(out, filepath.Base(r.Bin))
	}
	return out
}

// C16: each outcome maps to its faults, and each fault's reporter is the
// host principal.
func TestC16EachOutcomeMapsToItsFaults(t *testing.T) {
	quota := "=== RUN x\nquota: claude window session at 93% resets 2026-10-25T05:00:00Z\n"
	fail := "=== RUN TestF1RealAgentsTakeATicketFromClaimToMerge\n" +
		"    f1_orca_test.go:80: the ticket did not reach in_review\n" +
		"--- FAIL: TestF1RealAgentsTakeATicketFromClaimToMerge (1800.02s)\n" +
		"    --- FAIL: TestF1RealAgentsTakeATicketFromClaimToMerge/review (0.01s)\n" +
		"FAIL\n"
	cases := []struct {
		name     string
		contract script
		scenario script
		cancel   bool
		ran      []string
		prefix   string
		lines    []string
	}{
		{name: "green", contract: exits(0, "PASS\n"), scenario: exits(0, "PASS\n"),
			ran: []string{release.LaneContractFile, release.LaneAcceptanceFile}},
		{name: "spent", contract: exits(3, quota),
			ran:    []string{release.LaneContractFile},
			prefix: "orca-acceptance skipped on loop v1.2.3, orca 1.4.241.",
			lines:  []string{"quota: claude window session at 93% resets 2026-10-25T05:00:00Z"}},
		{name: "failed", contract: exits(0, "PASS\n"), scenario: exits(1, fail),
			ran:    []string{release.LaneContractFile, release.LaneAcceptanceFile},
			prefix: "orca-acceptance failed on loop v1.2.3, orca 1.4.241.",
			lines: []string{"--- FAIL: TestF1RealAgentsTakeATicketFromClaimToMerge",
				"--- FAIL: TestF1RealAgentsTakeATicketFromClaimToMerge/review"}},
		{name: "timed out", contract: exits(0, "PASS\n"),
			scenario: func(ctx context.Context, _ io.Writer) (int, error) {
				dl, ok := ctx.Deadline()
				if left := time.Until(dl); !ok || left < orcalane.LaneDeadline-time.Minute || left > orcalane.LaneDeadline {
					t.Errorf("the lane deadline is %v away, want %v", left, orcalane.LaneDeadline)
				}
				return -1, context.DeadlineExceeded
			},
			ran:    []string{release.LaneContractFile, release.LaneAcceptanceFile},
			prefix: "orca-acceptance timed out on loop v1.2.3, orca 1.4.241."},
		{name: "cancelled", contract: exits(0, "PASS\n"), cancel: true,
			ran: []string{release.LaneContractFile, release.LaneAcceptanceFile}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			w.scripts[release.LaneContractFile] = c.contract
			w.scripts[release.LaneAcceptanceFile] = c.scenario
			if c.cancel {
				w.scripts[release.LaneAcceptanceFile] = func(ctx context.Context, _ io.Writer) (int, error) {
					cancel()
					<-ctx.Done()
					return -1, ctx.Err()
				}
			}
			w.tick(ctx)
			if got := w.binsRun(); !slices.Equal(got, c.ran) {
				t.Fatalf("ran %v, want %v", got, c.ran)
			}
			if c.prefix == "" {
				if len(w.faults) != 0 {
					t.Fatalf("faults = %+v, want none", w.faults)
				}
				return
			}
			if len(w.faults) != 1 {
				t.Fatalf("faults = %+v, want one", w.faults)
			}
			f := w.faults[0]
			if f.P.Kind != cli.PrincipalHost {
				t.Fatalf("reporter = %q, want host", f.P.Kind)
			}
			logs := w.logs()
			if len(logs) != 1 {
				t.Fatalf("logs = %v", logs)
			}
			want := append(append([]string{c.prefix}, c.lines...), "Log: "+logs[0])
			if got := strings.Split(strings.TrimSuffix(f.Text, "\n"), "\n"); !slices.Equal(got[:1], want[:1]) || !slices.Equal(got[len(got)-1:], want[len(want)-1:]) {
				t.Fatalf("fault text:\n%s\nwant header %q and %q", f.Text, want[0], want[len(want)-1])
			}
			for _, line := range c.lines {
				if !slices.Contains(strings.Split(f.Text, "\n"), line) {
					t.Errorf("the fault does not hold the line %q:\n%s", line, f.Text)
				}
			}
		})
	}
}

// C17: for any lane output, the fault holds only its header, the
// --- FAIL: lines, the quota line, the stop reason and the log path.
func TestC17AFaultNeverCarriesLaneOutputBeyondItsNamedLines(t *testing.T) {
	const marker = "SECRET-MARKER-7f3a"
	allowed := []*regexp.Regexp{
		regexp.MustCompile(`^orca-acceptance (skipped|failed|timed out) on loop v1\.2\.3, orca 1\.4\.241\.$`),
		regexp.MustCompile(`^--- FAIL: \S+$`),
		regexp.MustCompile(`^quota: \S+ window \S+ at [0-9.]+% (resets \S+|reset unknown)$`),
		regexp.MustCompile(`^orca-(contract|acceptance)\.test exited -?\d+$`),
		regexp.MustCompile(`^Log: /\S+\.log$`),
	}
	pieces := []string{
		marker,
		"--- FAIL: TestPlanted " + marker,
		"--- FAIL: TestNamed (0.10s)",
		"    --- FAIL: TestNamed/sub_" + "x (0.00s)",
		"quota: claude window session at 93% resets 2026-10-25T05:00:00Z " + marker,
		"quota: claude window weekly at 95% reset unknown",
		"quota: " + marker,
		"panic: " + marker,
		"=== RUN TestX",
		"    x_test.go:12: " + marker,
		"FAIL",
		"",
	}
	rng := rand.New(rand.NewPCG(1, 2))
	w := newWorld(t)
	for i := range 150 {
		var b strings.Builder
		for range rng.IntN(12) {
			b.WriteString(pieces[rng.IntN(len(pieces))])
			b.WriteString("\n")
		}
		code := []int{1, 2, 3, 137}[rng.IntN(4)]
		if rng.IntN(2) == 0 {
			w.scripts[release.LaneContractFile] = exits(code, b.String())
			w.scripts[release.LaneAcceptanceFile] = nil
		} else {
			w.scripts[release.LaneContractFile] = exits(0, "PASS\n")
			w.scripts[release.LaneAcceptanceFile] = exits(code, b.String())
		}
		w.faults = nil
		w.tick(context.Background())
		if len(w.faults) != 1 {
			t.Fatalf("case %d: faults = %+v, want one", i, w.faults)
		}
		text := w.faults[0].Text
		if strings.Contains(text, marker) {
			t.Fatalf("case %d: the planted line reached the fault:\n%s\noutput:\n%s", i, text, b.String())
		}
		for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			if !slices.ContainsFunc(allowed, func(re *regexp.Regexp) bool { return re.MatchString(line) }) {
				t.Fatalf("case %d: the fault holds the line %q, which is no named line:\n%s", i, line, text)
			}
		}
	}
}

// signedRelease writes a release of version under base, signed with sk,
// whose checksum file names files. tamper changes an asset after the sum.
type signedRelease struct {
	sk     release.SecretKey
	assets map[string]string
	named  []string
	tamper string
}

func (r signedRelease) write(t *testing.T, base string) {
	t.Helper()
	dir := filepath.Join(base, version)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sums := release.Checksums{}
	for _, name := range r.named {
		sum := sha256hex(r.assets[name])
		sums[name] = sum
	}
	for name, data := range r.assets {
		if name == r.tamper {
			data += "tampered"
		}
		writeFile(t, filepath.Join(dir, name), data, 0o644)
	}
	raw := sums.Bytes()
	sig, err := release.Sign(r.sk, raw, release.TrustedComment(version))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, release.ChecksumsFile), string(raw), 0o644)
	writeFile(t, filepath.Join(dir, release.SignatureFile), string(sig), 0o644)
}

// C18: the job runs only lane binaries that the installed release signed.
func TestC18TheJobRunsOnlyLaneBinariesTheInstalledReleaseSigned(t *testing.T) {
	pub, sk, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	contract := release.OrcaContractAssetName(goos(), goarch())
	acceptance := release.OrcaAcceptanceAssetName(goos(), goarch())
	assets := map[string]string{
		release.AssetName(goos(), goarch()): "loop binary",
		contract:                            "contract binary",
		acceptance:                          "acceptance binary",
	}
	all := []string{release.AssetName(goos(), goarch()), contract, acceptance}
	cases := []struct {
		name   string
		rel    signedRelease
		prefix string
	}{
		{name: "good", rel: signedRelease{sk: sk, assets: assets, named: all}},
		{name: "tampered", rel: signedRelease{sk: sk, assets: assets, named: all, tamper: acceptance},
			prefix: "orca-acceptance failed"},
		{name: "unsigned", rel: signedRelease{sk: other, assets: assets, named: all},
			prefix: "orca-acceptance failed"},
		{name: "pre-lane", rel: signedRelease{sk: sk, assets: assets, named: all[:1]},
			prefix: "orca-acceptance skipped"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			dir := w.layout.VersionDir(version)
			for _, name := range []string{release.LaneContractFile, release.LaneAcceptanceFile} {
				if err := os.Remove(filepath.Join(dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			base := t.TempDir()
			c.rel.write(t, base)
			w.job.Fetcher = orcalane.ReleaseFetcher{Source: release.Source{Base: base}, Keys: []release.PublicKey{pub}}
			w.tick(context.Background())
			if c.prefix == "" {
				if len(w.faults) != 0 {
					t.Fatalf("faults = %+v", w.faults)
				}
				want := []string{filepath.Join(dir, release.LaneContractFile), filepath.Join(dir, release.LaneAcceptanceFile)}
				var got []string
				for _, r := range w.runs {
					got = append(got, r.Bin)
				}
				if !slices.Equal(got, want) {
					t.Fatalf("ran %v, want %v", got, want)
				}
				for name, data := range map[string]string{release.LaneContractFile: "contract binary", release.LaneAcceptanceFile: "acceptance binary"} {
					st, err := os.Stat(filepath.Join(dir, name))
					if err != nil || st.Mode().Perm() != 0o755 {
						t.Fatalf("%s: %v, %v", name, st, err)
					}
					if b, _ := os.ReadFile(filepath.Join(dir, name)); string(b) != data {
						t.Fatalf("%s holds %q", name, b)
					}
				}
				return
			}
			if len(w.runs) != 0 {
				t.Fatalf("the runner ran %v", w.binsRun())
			}
			if len(w.faults) != 1 || !strings.HasPrefix(w.faults[0].Text, c.prefix) {
				t.Fatalf("faults = %+v, want one that starts %q", w.faults, c.prefix)
			}
			for _, name := range []string{release.LaneContractFile, release.LaneAcceptanceFile} {
				if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
					t.Fatalf("%s was installed", name)
				}
			}
		})
	}
}

// C19: the first tick makes the fixture with its marker and registers it
// once. The second tick reuses it and registers nothing.
func TestC19TheJobMakesAndRegistersTheFixtureOnce(t *testing.T) {
	w := newWorld(t)
	w.tick(context.Background())
	if !orca.IsLaneFixture(w.fixture()) {
		t.Fatalf("%s is no lane fixture", w.fixture())
	}
	head := gitHead(t, w.fixture())
	if w.adds != 1 || !slices.Contains(w.events, "repo add "+w.fixture()) {
		t.Fatalf("repo adds = %d, events %v", w.adds, w.events)
	}
	w.events = nil
	w.tick(context.Background())
	if w.adds != 1 || !slices.Contains(w.events, "repo show "+w.fixture()) {
		t.Fatalf("the second tick: repo adds = %d, events %v", w.adds, w.events)
	}
	if got := gitHead(t, w.fixture()); got != head {
		t.Fatalf("the second tick made the fixture again: head %s, was %s", got, head)
	}
}

func gitHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "main").Output()
	if err != nil {
		t.Fatalf("git rev-parse main in %s: %v", dir, err)
	}
	return strings.TrimSpace(string(out))
}

// C20: the sweep closes and removes each worktree of the fixture, once
// before the first binary and once after the last, and asks for no other
// repository's worktrees. It never removes the fixture's own checkout. A
// timed-out lane is swept too.
func TestC20TheSweepRemovesOnlyTheFixturesWorktreesBeforeAndAfter(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprintf("timed out %v", timeout), func(t *testing.T) {
			w := newWorld(t)
			fx := w.fixture()
			w.trees[fx] = []string{fx, "/srv/checkouts/lane-a", "/srv/checkouts/lane-b"}
			w.trees["/home/operator/projects/loop"] = []string{"/srv/checkouts/production"}
			if timeout {
				w.scripts[release.LaneAcceptanceFile] = func(context.Context, io.Writer) (int, error) {
					return -1, context.DeadlineExceeded
				}
			}
			w.tick(context.Background())
			sweep := []string{"worktrees " + fx,
				"close /srv/checkouts/lane-a", "remove /srv/checkouts/lane-a",
				"close /srv/checkouts/lane-b", "remove /srv/checkouts/lane-b"}
			want := slices.Concat(sweep, []string{"run " + release.LaneContractFile, "run " + release.LaneAcceptanceFile}, sweep)
			var got []string
			for _, e := range w.events {
				if !strings.HasPrefix(e, "repo ") {
					got = append(got, e)
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		})
	}
}

// C22: the job sets every lane value itself, whatever the server's own
// environment holds, and runs each binary with its run pattern.
func TestC22TheJobSetsEveryLaneValueItself(t *testing.T) {
	t.Setenv("LOOP_ORCA_BIN", "/stray/orca")
	t.Setenv("LOOP_ORCA_MODEL", "opus")
	t.Setenv("LOOP_ACCEPTANCE_LOOP", "/stray/loop")
	for _, bin := range []string{"/opt/orca/bin/orca", ""} {
		t.Run("binary "+bin, func(t *testing.T) {
			w := newWorld(t)
			w.host.AgentHost.Binary = bin
			w.tick(context.Background())
			if len(w.runs) != 2 {
				t.Fatalf("runs = %v", w.binsRun())
			}
			want := map[string]string{
				"LOOP_ORCA_ROOT":       "/srv/checkouts",
				"LOOP_ORCA_REPO":       "path:" + w.fixture(),
				"LOOP_ORCA_MODEL":      "claude-opus-5-5",
				"LOOP_BD":              filepath.Join(w.layout.Dir, "bin", "bd"),
				"LOOP_DOLT":            filepath.Join(w.layout.Dir, "bin", "dolt"),
				"LOOP_ACCEPTANCE_LOOP": filepath.Join(w.layout.Dir, "versions", version, "loop"),
			}
			if bin != "" {
				want["LOOP_ORCA_BIN"] = bin
			}
			for i, r := range w.runs {
				got := map[string]string{}
				for _, kv := range r.Env {
					k, v, _ := strings.Cut(kv, "=")
					if _, dup := got[k]; dup {
						t.Fatalf("run %d sets %s twice", i, k)
					}
					if strings.HasPrefix(k, "LOOP_") {
						got[k] = v
					}
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("run %d: LOOP_ values\n%v\nwant\n%v", i, got, want)
				}
				if !slices.Contains(r.Env, "PATH="+os.Getenv("PATH")) {
					t.Fatalf("run %d does not keep PATH", i)
				}
			}
			if got := w.runs[0].Args; !slices.Equal(got, []string{"-test.run", contractRun}) {
				t.Fatalf("contract args = %q", got)
			}
			if got := w.runs[1].Args; !slices.Equal(got, []string{"-test.run", f1Run}) {
				t.Fatalf("scenario args = %q", got)
			}
		})
	}
}

// C27: each tick leaves one log at mode 0600, with the contract binary's
// output before the scenario binary's, each under a header that names the
// binary and its exit. A tick that fails at the fetch still writes a log
// that names the error. With 30 logs present, a tick leaves 30.
func TestC27EachTickLeavesOneLogAndTheJobKeepsThe30Newest(t *testing.T) {
	w := newWorld(t)
	w.scripts[release.LaneContractFile] = exits(0, "contract marker\n")
	w.scripts[release.LaneAcceptanceFile] = exits(1, "scenario marker\n")
	w.tick(context.Background())
	logs := w.logs()
	if len(logs) != 1 {
		t.Fatalf("logs = %v", logs)
	}
	st, err := os.Stat(logs[0])
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("log mode: %v, %v", st, err)
	}
	b, _ := os.ReadFile(logs[0])
	text := string(b)
	order := []string{"== orca-contract.test exit 0 ==", "contract marker", "== orca-acceptance.test exit 1 ==", "scenario marker"}
	at := -1
	for _, s := range order {
		i := strings.Index(text, s)
		if i <= at {
			t.Fatalf("the log does not hold %q after the line before it:\n%s", s, text)
		}
		at = i
	}
	if len(w.faults) != 1 || !strings.HasSuffix(strings.TrimSpace(w.faults[0].Text), "Log: "+logs[0]) {
		t.Fatalf("the fault does not name the log: %+v", w.faults)
	}

	// A failed fetch.
	w = newWorld(t)
	if err := os.Remove(filepath.Join(w.layout.VersionDir(version), release.LaneAcceptanceFile)); err != nil {
		t.Fatal(err)
	}
	w.fetch = func(string) (bool, error) { return false, errors.New("signature refused by the planted key") }
	w.tick(context.Background())
	logs = w.logs()
	if len(logs) != 1 {
		t.Fatalf("logs after a failed fetch = %v", logs)
	}
	if b, _ := os.ReadFile(logs[0]); !strings.Contains(string(b), "signature refused by the planted key") {
		t.Fatalf("the log does not name the fetch error:\n%s", b)
	}
	if len(w.runs) != 0 {
		t.Fatalf("ran %v after a failed fetch", w.binsRun())
	}

	// 30 logs present.
	w = newWorld(t)
	dir := filepath.Join(w.layout.Dir, "orca-acceptance", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var seeded []string
	for i := range orcalane.KeepLogs {
		name := filepath.Join(dir, fmt.Sprintf("202601%02dT000000.000Z.log", i+1))
		writeFile(t, name, "old\n", 0o600)
		seeded = append(seeded, name)
	}
	w.tick(context.Background())
	logs = w.logs()
	if len(logs) != orcalane.KeepLogs {
		t.Fatalf("%d logs remain, want %d", len(logs), orcalane.KeepLogs)
	}
	if slices.Contains(logs, seeded[0]) || !slices.Contains(logs, seeded[1]) {
		t.Fatalf("the job did not remove the oldest name: %v", logs)
	}
	if slices.Contains(seeded, logs[len(logs)-1]) {
		t.Fatalf("the newest log is a seeded one: %v", logs)
	}
}

func goos() string   { return runtime.GOOS }
func goarch() string { return runtime.GOARCH }

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
