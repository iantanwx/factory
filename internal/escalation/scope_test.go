package escalation

import (
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/admission"
)

// tic70 is the shape of TIC-70 (alpha): the contract needs a file under a
// glob that the ticket forbids. Its "Must not change" bullet wraps.
const tic70 = "Do the work.\n\n" +
	"## Files\n\n" +
	"**May change:**\n" +
	"* `apps/web/src/**`\n\n" +
	"**Must not change:**\n" +
	"* `packages/**`, which other\n" +
	"  tickets own.\n" +
	"* `docs/adr/**`\n\n" +
	"## Out of scope\n\n" +
	"- `packages/db/` stays as it is.\n"

func release(path, forbids string) Release {
	return Release{Path: path, Forbids: forbids, Condition: "Add the one migration.", Escalation: 7}
}

// The edit lands both halves, keeps the ticket's bullet marker, appends
// after a wrapped bullet, and changes nothing outside "## Files".
func TestReleasePathMovesOnePathAndNothingElse(t *testing.T) {
	got, r := ReleasePath(tic70, release("packages/db/src/**", "packages/**"))
	if r != nil {
		t.Fatalf("refused: %+v", r)
	}
	want := "Do the work.\n\n" +
		"## Files\n\n" +
		"**May change:**\n" +
		"* `apps/web/src/**`\n" +
		"* `packages/db/src/**`: Add the one migration. Released from \"Must not change\" by the fixer for escalation 7.\n\n" +
		"**Must not change:**\n" +
		"* `packages/**`, which other\n" +
		"  tickets own. The fixer released `packages/db/src/**` into \"May change\" for escalation 7, and nothing else here.\n" +
		"* `docs/adr/**`\n\n" +
		"## Out of scope\n\n" +
		"- `packages/db/` stays as it is.\n"
	if got != want {
		t.Fatalf("ReleasePath =\n%s\nwant\n%s", got, want)
	}
	// The same release again writes nothing: the body changes once.
	if _, r := ReleasePath(got, release("packages/db/src/**", "packages/**")); r == nil || r.Rule != "already_released" {
		t.Fatalf("a second release = %+v", r)
	}
}

func TestReleasePathRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		rel  Release
		rule string
	}{
		{"never released", tic70, release("docs/adr/0009-x.md", "docs/adr/**"), RuleNeverReleased},
		{"a glob that reaches a never-released path", tic70, release("**", "packages/**"), RuleNeverReleased},
		{"a directory that reaches one", "", release("docs/", "docs/**"), RuleNeverReleased},
		{"the loop's config", "", release(".loop/config.yaml", ".loop/**"), RuleNeverReleased},
		{"no condition", tic70, Release{Path: "packages/db/x.go", Forbids: "packages/**", Condition: " "}, "no_condition"},
		{"another prefix", tic70, release("apps/marketing/x.ts", "packages/**"), "not_covered"},
		{"a file entry covers itself alone", tic70, release("packages/db/x.go", "packages/db.go"), "not_covered"},
		{"misspelled entry", tic70, release("packages/db/x.go", "packages/*"), "no_entry"},
		{"no files section", "Do the work.\n", release("packages/db/x.go", "packages/**"), "no_lists"},
		// A path is read as the loop reads it, so a spelling that climbs out
		// of the forbidden glob reaches the path it lands on.
		{"a climb into CI", "", release("lib/../.github/workflows/ci.yml", "lib/**"), RuleNeverReleased},
		{"a climb into the loop's config", "", release("lib/../.loop/config.yaml", "lib/**"), RuleNeverReleased},
		{"a ./ spelling of CI", "", release("./.github/workflows/ci.yml", "**"), RuleNeverReleased},
		{"a climb out of the glob", tic70, release("packages/../apps/marketing/x.ts", "packages/**"), "not_covered"},
		{"an absolute path", tic70, release("/packages/db/x.go", "packages/**"), "outside_checkout"},
		{"a climb out of the checkout", tic70, release("packages/../../x.go", "packages/**"), "outside_checkout"},
		{"the checkout itself", tic70, release("packages/..", "packages/**"), "outside_checkout"},
		// The loop reads every backticked token of "May change" as a path, so
		// the text the release writes holds no backtick and no line break of
		// its own.
		{"a backtick in the path", tic70, release("packages/x` `.github/workflows/ci.yml", "packages/**"), "path_not_plain"},
		{"a line break in the path", tic70, release("packages/x\n* `.loop/config.yaml`", "packages/**"), "path_not_plain"},
		{"a space in the path", tic70, release("packages/db/my file.go", "packages/**"), "path_not_plain"},
		// The loop's reader expands a brace group and leaves a ".." in it, so
		// a brace group names paths that no guard here reads.
		{"a brace group that climbs into CI", tic70, release("packages/{x,..}/.github/workflows/ci.yml", "packages/**"), "path_not_plain"},
		{"a brace group that climbs into the loop's config", tic70, release("packages/{db,..}/.loop/config.yaml", "packages/**"), "path_not_plain"},
		{"a brace group of two paths", tic70, release("packages/{db/x.go,../.github/workflows/ci.yml}", "packages/**"), "path_not_plain"},
		{"a backticked path in the condition", tic70, Release{Path: "packages/db/x.go", Forbids: "packages/**",
			Condition: "Edit only this file; never touch `.github/workflows/ci.yml`.", Escalation: 7}, "condition_not_plain"},
		{"a line break in the condition", tic70, Release{Path: "packages/db/x.go", Forbids: "packages/**",
			Condition: "Edit this file.\n* Then stop.", Escalation: 7}, "condition_not_plain"},
		// The plain text of a condition can still cite a Markdown path, which
		// the retry identity reads. A path the release adds there must be the
		// released one.
		{"a cited PRD in the condition", tic70, Release{Path: "packages/db/x.go", Forbids: "packages/**",
			Condition: "Edit it as docs/prd/0001-db.md asks.", Escalation: 7}, "reads_wider"},
		{"a cited path under the glob in the condition", tic70, Release{Path: "packages/db/x.go", Forbids: "packages/**",
			Condition: "Edit it as packages/db/README.md says.", Escalation: 7}, "reads_wider"},
	} {
		if _, r := ReleasePath(tc.body, tc.rel); r == nil || r.Rule != tc.rule {
			t.Errorf("%s: %+v, want %s", tc.name, r, tc.rule)
		}
	}
}

// The new "May change" bullet names no forbidden entry in backticks, so
// the retry identity does not start to read the whole forbidden glob.
func TestReleasedBulletNamesOnlyTheReleasedPath(t *testing.T) {
	got, r := ReleasePath(tic70, release("packages/db/src/**", "packages/**"))
	if r != nil {
		t.Fatal(r)
	}
	for line := range strings.SplitSeq(got, "\n") {
		if strings.Contains(line, "Released from") && strings.Contains(line, "`packages/**`") {
			t.Fatalf("the released bullet names the forbidden glob: %q", line)
		}
	}
}

// The body holds the clean spelling of a path, the one the loop's reader
// of "May change" reads, and a second spelling of a released path
// releases nothing.
func TestReleasePathWritesTheCleanPath(t *testing.T) {
	want, r := ReleasePath(tic70, release("packages/db/src/**", "packages/**"))
	if r != nil {
		t.Fatal(r)
	}
	got, r := ReleasePath(tic70, release("./packages//db/./src/**", "packages/**"))
	if r != nil || got != want {
		t.Fatalf("ReleasePath = %+v\n%s\nwant\n%s", r, got, want)
	}
	if _, r := ReleasePath(got, release("./packages/db/src/**", "packages/**")); r == nil || r.Rule != "already_released" {
		t.Fatalf("a second spelling = %+v", r)
	}
}

// ReleasedBy finds this escalation's release of this path, and no other
// escalation's, and no other path's.
func TestReleasedByNamesOneEscalationAndOnePath(t *testing.T) {
	body, r := ReleasePath(tic70, release("packages/db/src/**", "packages/**"))
	if r != nil {
		t.Fatal(r)
	}
	for _, tc := range []struct {
		path string
		id   int64
		want bool
	}{
		{"packages/db/src/**", 7, true},
		{"./packages/db/src/**", 7, true},
		{"packages/db/src/**", 70, false},
		{"packages/db/src/**", 8, false},
		{"apps/web/src/**", 7, false},
	} {
		if got := ReleasedBy(body, tc.path, tc.id); got != tc.want {
			t.Errorf("ReleasedBy(%q, %d) = %v", tc.path, tc.id, got)
		}
	}
	if ReleasedBy(tic70, "packages/db/src/**", 7) {
		t.Error("a body with no release holds one")
	}
}

// The paths the loop reads in "May change" after a release are the paths
// it read before, and the released one. The guard and the reader agree.
func TestReleasePathAddsOnlyTheReleasedPathToWhatTheLoopReads(t *testing.T) {
	got, r := ReleasePath(tic70, Release{Path: "packages/db/x.go", Forbids: "packages/**",
		Condition: "Add the one migration, and nothing else.", Escalation: 7})
	if r != nil {
		t.Fatalf("refused: %+v", r)
	}
	want := []string{"apps/web/src/", "packages/db/x.go"}
	if paths := admission.DeclaredPaths(got); !slices.Equal(paths, want) {
		t.Fatalf("DeclaredPaths = %q, want %q", paths, want)
	}
}
