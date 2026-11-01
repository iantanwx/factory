package ci

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/gitx/gittest"
)

func TestIsDocCountsOnlyMarkdownThatNoBuildReads(t *testing.T) {
	for file, want := range map[string]bool{
		"README.md":                         true,
		"docs/operating.md":                 true,
		"docs/adr/0001-x.md":                true,
		"docs/evidence/beads-contract.md":   false, // internal/release/provision_test.go reads it
		"docs/plans/notes.txt":              false,
		"pack/skills/loop-x/SKILL.md":       false, // the binary embeds the pack
		".loop/policy/implementer.md":       false,
		"internal/gates/README.md":          false,
		"docs.md":                           false,
		".github/workflows/ci.yml":          false,
		"cmd/loop/main.go":                  false,
		"docs/../cmd/loop/main.go":          false,
		"deploy/mergify.yml.tmpl":           false,
		"docs/solutions/some-fix/README.md": true,
	} {
		if got := IsDoc(file); got != want {
			t.Errorf("IsDoc(%q) = %v, want %v", file, got, want)
		}
	}
}

func TestBaseOfRefusesEveryEventThatNamesNoTrustedBase(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		name  string
		event Event
		want  Base
	}{
		{"pull request", Event{Name: "pull_request", PRBase: sha}, Base{SHA: sha}},
		{"pull request with no base", Event{Name: "pull_request"}, Base{Why: "the event names no pull request base"}},
		{"push", Event{Name: "push", Before: sha, Forced: "false"}, Base{SHA: sha}},
		{"force-push", Event{Name: "push", Before: sha, Forced: "true"}, Base{Why: "a force-push replaced history, so the commit before it proves nothing"}},
		{"new branch", Event{Name: "push", Before: strings.Repeat("0", 40)}, Base{Why: "the push created the branch, so no commit came before it"}},
		{"push with no before", Event{Name: "push"}, Base{Why: "the push created the branch, so no commit came before it"}},
		{"other event", Event{Name: "workflow_dispatch", Before: sha}, Base{Why: "a workflow_dispatch event names no base"}},
	} {
		if got := BaseOf(c.event); got != c.want {
			t.Errorf("%s: BaseOf = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDecideRunsEveryGateUnlessEveryFileIsADocument(t *testing.T) {
	base := Base{SHA: "abc"}
	for _, c := range []struct {
		name  string
		base  Base
		files []string
		want  Decision
	}{
		{"no base", Base{Why: "no base here"}, []string{"README.md"}, Decision{Docs: false, Why: "no base here"}},
		{"unreadable diff", base, nil, Decision{Docs: false, Why: "could not list the files changed since abc"}},
		{"empty diff", base, []string{}, Decision{Docs: false, Why: "no file changed since abc"}},
		{"one code file", base, []string{"docs/a.md", "cmd/loop/main.go"}, Decision{Docs: false, Why: "cmd/loop/main.go changed, and it is not documentation"}},
		{"documents alone", base, []string{"docs/a.md", "README.md"}, Decision{Docs: true, Why: "all 2 files changed since abc are documentation"}},
	} {
		if got := Decide(c.base, c.files); got != c.want {
			t.Errorf("%s: Decide = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestFilesSinceListsBothSidesOfAMove(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := gittest.Command(append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("echo hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-qm", "one")
	base := git("rev-parse", "HEAD")
	if err := os.Mkdir(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	git("mv", "run.sh", "docs/run.md")
	git("commit", "-qm", "two")

	got := FilesSince(base, dir)
	if want := []string{"docs/run.md", "run.sh"}; !slices.Equal(got, want) {
		t.Fatalf("FilesSince = %q, want %q", got, want)
	}
	if got := FilesSince(strings.Repeat("1", 40), dir); got != nil {
		t.Fatalf("FilesSince of a missing commit = %q, want nil", got)
	}
}

// joinArgs matches a filepath.Join whose arguments are string literals.
var joinArgs = regexp.MustCompile(`filepath\.Join\(((?:"[^"]*"\s*,\s*)*"[^"]*")\)`)

// A documents-only change runs no Go test. So a test that reads a
// document would miss the change that breaks it. Each such path must be
// code to IsDoc, as docs/evidence/ is.
func TestNoGoTestReadsADocument(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "gen") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range joinArgs.FindAllStringSubmatch(string(src), -1) {
			var parts []string
			for _, lit := range strings.Split(m[1], ",") {
				s, err := strconv.Unquote(strings.TrimSpace(lit))
				if err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				parts = append(parts, s)
			}
			// A path out of the package directory to the repository root.
			up := 0
			for up < len(parts) && parts[up] == ".." {
				up++
			}
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			if up == 0 || up != len(strings.Split(filepath.ToSlash(rel), "/")) {
				continue
			}
			if file := strings.Join(parts[up:], "/"); IsDoc(file) {
				t.Errorf("%s reads %s, which a documents-only change skips the tests for", path, file)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
