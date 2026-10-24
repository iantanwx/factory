package escalation

import (
	"strings"
	"testing"
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
