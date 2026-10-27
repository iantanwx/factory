package settings

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/sched"
)

// fixture is a repository whose main holds .loop/config.yaml, and a
// Service that reads it.
type fixture struct {
	t   *testing.T
	dir string
	lkg *memLKG
	svc *Service
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	f := &fixture{t: t, dir: t.TempDir(), lkg: &memLKG{}}
	f.git("init", "-q", "-b", "main")
	hc := config.DefaultHost("/var/lib/loop")
	f.svc = &Service{
		Host:   func(context.Context) (*config.HostConfig, error) { return hc, nil },
		Loader: &config.Loader{LKG: f.lkg, Alerts: noAlerts{}, Check: func(pc *config.ProjectConfig) error { return pc.CheckStages(config.KnownStages(hc)) }},
		Repo: func(ctx context.Context, project string) (Repo, error) {
			out, err := gitx.Run(ctx, f.dir, "rev-parse", "HEAD")
			return Repo{Dir: f.dir, Branch: "main", Head: strings.TrimSpace(string(out))}, err
		},
	}
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", append([]string{"-C", f.dir, "-c", "user.name=T", "-c", "user.email=t@example.com"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes the config and commits it. It returns the commit.
func (f *fixture) commit(cfg, author string) string {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.dir, ".loop"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, config.ProjectConfigPath), []byte(cfg), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", "-A")
	f.git("-c", "user.name="+author, "commit", "-q", "-m", "config")
	return f.git("rev-parse", "HEAD")
}

// other commits a file that is not the config.
func (f *fixture) other(name string) string {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(name), 0o644); err != nil {
		f.t.Fatal(err)
	}
	f.git("add", "-A")
	f.git("commit", "-q", "-m", name)
	return f.git("rev-parse", "HEAD")
}

func (f *fixture) show() View {
	f.t.Helper()
	v, ref := f.svc.Show(context.Background(), "alpha")
	if ref != nil {
		f.t.Fatalf("show = %+v", ref)
	}
	return v
}

func rowOf(t *testing.T, v View, key string) Row {
	t.Helper()
	for _, r := range v.Rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("no row %s in %+v", key, v.Rows)
	return Row{}
}

func wantRow(t *testing.T, v View, key, value string, src config.Source, commit string) {
	t.Helper()
	if r := rowOf(t, v, key); r.Value != value || r.Source != src || r.Commit != commit {
		t.Fatalf("%s = %+v, want %q from %s at %q", key, r, value, src, commit)
	}
}

// C15: the read lists one row for each setting of each known stage, pool
// and the merge section. A project row names the newest commit that
// changed its value, a person's hand commit too. A host or default row
// names no commit.
func TestC15SettingsReadListsValueSourceAndCommit(t *testing.T) {
	f := newFixture(t)
	first := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {}\n", "coordinator")
	v := f.show()
	wantRow(t, v, "stages.implementer.cap", "8", config.SourceHost, "")
	wantRow(t, v, "stages.implementer.enabled", "true", config.SourceProject, first)

	byHand := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n", "a person")
	f.other("README.md")
	pools := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\npools:\n  reviewers: {cap: 2}\n", "coordinator")
	head := f.other("NOTES.md")
	v = f.show()
	if v.Project != "alpha" || v.Branch != "main" || v.Head != head || v.Stale || v.Problem != "" {
		t.Fatalf("view = %+v", v)
	}
	wantRow(t, v, "stages.implementer.cap", "3", config.SourceProject, byHand)
	wantRow(t, v, "pools.reviewers.cap", "2", config.SourceProject, pools)
	wantRow(t, v, "stages.implementer.enabled", "true", config.SourceProject, first)
	wantRow(t, v, "merge.mechanism", "direct", config.SourceProject, first)
	wantRow(t, v, "stages.code-reviewer.cap", "8", config.SourceHost, "")
	wantRow(t, v, "stages.code-reviewer.enabled", "false", config.SourceDefault, "")
	wantRow(t, v, "merge.required_checks", "", config.SourceDefault, "")

	hc := config.DefaultHost("/var/lib/loop")
	want := config.Effective(hc, nil).Settings(config.KnownStages(hc), nil)
	if len(v.Rows) != len(want) {
		t.Fatalf("the read lists %d rows, want one for each of the %d settings", len(v.Rows), len(want))
	}
	for i, r := range v.Rows {
		if r.Key != want[i].Key.String() {
			t.Fatalf("row %d is %s, want %s", i, r.Key, want[i].Key)
		}
		if r.Source != config.SourceProject && r.Commit != "" {
			t.Fatalf("%s from %s names commit %s", r.Key, r.Source, r.Commit)
		}
	}
}

// C15: a commit that sets a value back to one an older commit held is the
// commit that changed it.
func TestC15SettingsReadNamesTheNewestChange(t *testing.T) {
	f := newFixture(t)
	f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n", "coordinator")
	f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 4}\n", "coordinator")
	back := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n", "coordinator")
	wantRow(t, f.show(), "stages.implementer.cap", "3", config.SourceProject, back)
}

// C15: a change merged from a pull request branch names the branch's
// commit, not the merge commit.
func TestC15SettingsReadNamesTheBranchCommitOfAMerge(t *testing.T) {
	f := newFixture(t)
	f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {}\n", "coordinator")
	f.git("switch", "-q", "-c", "loop/settings/1")
	change := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n", "coordinator")
	f.git("switch", "-q", "main")
	f.other("README.md")
	f.git("merge", "-q", "--no-ff", "-m", "merge the settings change", "loop/settings/1")
	wantRow(t, f.show(), "stages.implementer.cap", "3", config.SourceProject, change)
}

// C15: a field that the project and host config leave empty lists the
// loop's own default, the one the scheduler starts a run with. An
// escalated default follows the harness the project names.
func TestC15SettingsReadListsTheLoopDefaults(t *testing.T) {
	f := newFixture(t)
	f.svc.Defaults = sched.Defaults
	harness := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {}\n  code-reviewer: {harness: codex}\n", "coordinator")
	v := f.show()
	wantRow(t, v, "stages.implementer.harness", "claude", config.SourceDefault, "")
	wantRow(t, v, "stages.implementer.model", "claude-opus-5-5", config.SourceDefault, "")
	wantRow(t, v, "stages.implementer.effort", "high", config.SourceDefault, "")
	wantRow(t, v, "stages.implementer.escalated.model", "claude-opus-5-5", config.SourceDefault, "")
	wantRow(t, v, "stages.implementer.run_budget", "2h0m0s", config.SourceDefault, "")
	wantRow(t, v, "stages.doc-reviewer.effort", "medium", config.SourceDefault, "")
	wantRow(t, v, "stages.code-reviewer.harness", "codex", config.SourceProject, harness)
	wantRow(t, v, "stages.code-reviewer.model", "gpt-5-codex", config.SourceDefault, "")
	wantRow(t, v, "stages.code-reviewer.escalated.harness", "codex", config.SourceDefault, "")
	wantRow(t, v, "stages.code-reviewer.escalated.model", "gpt-5-codex", config.SourceDefault, "")
}

// C17: when the head does not load, the read says stale, names the failing
// field, and lists the last good copy's rows with commits from that copy's
// history.
func TestC17SettingsReadSaysTheLastGoodCopyIsInForce(t *testing.T) {
	f := newFixture(t)
	mech := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {}\n", "coordinator")
	good := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n", "coordinator")
	if v := f.show(); v.Stale {
		t.Fatalf("a valid head reads stale: %+v", v)
	}
	bad := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 0}\n", "a person")
	v := f.show()
	if !v.Stale || v.Head != bad || !strings.Contains(v.Problem, "stages.implementer.cap") {
		t.Fatalf("view = stale %v head %s problem %q, want stale at %s naming stages.implementer.cap", v.Stale, v.Head, v.Problem, bad)
	}
	wantRow(t, v, "stages.implementer.cap", "3", config.SourceProject, good)
	wantRow(t, v, "merge.mechanism", "direct", config.SourceProject, mech)
}

// C17: a stale read names the commits of the last good copy's history,
// not the newer commits of the head that does not load, even when the head
// holds the same value.
func TestC17SettingsReadNamesCommitsFromTheLastGoodCopy(t *testing.T) {
	f := newFixture(t)
	good := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n", "coordinator")
	if v := f.show(); v.Stale {
		t.Fatalf("a valid head reads stale: %+v", v)
	}
	f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 4}\n  bogus: {}\n", "a person")
	bad := f.commit("merge:\n  mechanism: direct\nstages:\n  implementer: {cap: 3}\n  bogus: {}\n", "a person")
	v := f.show()
	if !v.Stale || v.Head != bad || !strings.Contains(v.Problem, "bogus") {
		t.Fatalf("view = stale %v head %s problem %q, want stale at %s naming bogus", v.Stale, v.Head, v.Problem, bad)
	}
	wantRow(t, v, "stages.implementer.cap", "3", config.SourceProject, good)
}

// A project with no config yet lists the host values, and is not stale.
func TestSettingsReadOfAProjectWithNoConfig(t *testing.T) {
	f := newFixture(t)
	f.other("README.md")
	v := f.show()
	if v.Stale || v.Problem != "" {
		t.Fatalf("view = %+v", v)
	}
	wantRow(t, v, "stages.implementer.cap", "8", config.SourceHost, "")
}

type memLKG struct{ config, manifest []byte }

func (m *memLKG) LoadLKG(context.Context, string) ([]byte, []byte, error) {
	return m.config, m.manifest, nil
}

func (m *memLKG) SaveLKG(_ context.Context, _ string, c, mf []byte) error {
	m.config, m.manifest = c, mf
	return nil
}

type noAlerts struct{}

func (noAlerts) Raise(context.Context, config.Alert) error { return nil }
func (noAlerts) Clear(context.Context, string) error       { return nil }
