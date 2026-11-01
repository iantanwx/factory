package gates

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	designDir    = "docs/design/factorio-x/"
	designRecord = designDir + "record.md"
	protoRef     = "refs/remotes/origin/loop/factorio-x-prototype"
)

func designFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "design", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// prototypeRepo makes a checkout whose base holds files. The remote's
// prototype branch holds one commit on top of the base, and the checkout
// holds one more commit that the remote branch lacks. It returns the
// branch tip and that commit, and leaves the checkout on main.
func prototypeRepo(t *testing.T, files map[string]string) (r *Repo, tip, off string) {
	t.Helper()
	r = testRepo(t, files)
	git(t, r.Root, "switch", "-q", "-c", "prototype")
	commit(t, r, "feat: prototype", map[string]string{"app.txt": "1"})
	tip = strings.TrimSpace(git(t, r.Root, "rev-parse", "HEAD"))
	commit(t, r, "feat: unpushed", map[string]string{"app.txt": "2"})
	off = strings.TrimSpace(git(t, r.Root, "rev-parse", "HEAD"))
	git(t, r.Root, "update-ref", protoRef, tip)
	git(t, r.Root, "switch", "-q", "main")
	return r, tip, off
}

// recordFiles is the fixture record, pinned at pin, and each picture it
// names.
func recordFiles(t *testing.T, pin string) map[string]string {
	return map[string]string{
		designRecord:           strings.ReplaceAll(designFixture(t, "record.md"), "<commit>", pin),
		designDir + "s1-1.png": "png",
		designDir + "s1-2.png": "png",
		designDir + "s2-1.png": "png",
	}
}

// designFindings runs the gate on a branch that adds the fixture record,
// pinned at the prototype branch's tip, after edit changes its files.
func designFindings(t *testing.T, edit func(files map[string]string)) []Finding {
	t.Helper()
	r, tip, _ := prototypeRepo(t, map[string]string{"docs/prd/fleet.md": designFixture(t, "prd.md")})
	files := recordFiles(t, tip)
	edit(files)
	commit(t, r, "docs: add the design record", files)
	got, err := Design(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// replace edits the record of files, and fails when it holds no old.
func replace(t *testing.T, old, new string) func(map[string]string) {
	return func(files map[string]string) {
		if !strings.Contains(files[designRecord], old) {
			t.Fatalf("the record holds no %q", old)
		}
		files[designRecord] = strings.Replace(files[designRecord], old, new, 1)
	}
}

// wantFinding asserts one finding of rule whose message holds each of
// words, and reports whether the one finding is there.
func wantFinding(t *testing.T, name string, got []Finding, rule string, words ...string) bool {
	t.Helper()
	if !slices.Equal(rules(got), []string{rule}) {
		t.Errorf("%s: findings = %v, want one %s", name, got, rule)
		return false
	}
	for _, w := range words {
		if !strings.Contains(got[0].Message, w) {
			t.Errorf("%s: %q does not name %q", name, got[0].Message, w)
		}
	}
	return true
}

// C6: the fixture record, with each picture present and its pin on its
// branch, gives no finding.
func TestDesignCompleteRecordPasses(t *testing.T) {
	if got := designFindings(t, func(map[string]string) {}); len(got) != 0 {
		t.Fatal(got)
	}
}

// C6: a repository with no docs/design/ gives no finding.
func TestDesignRepositoryWithNoRecordPasses(t *testing.T) {
	r := testRepo(t, map[string]string{"docs/prd/fleet.md": designFixture(t, "prd.md")})
	got, err := Design(context.Background(), r)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

// C1: a UI story that no screen cites fails the gate, and the message
// names the story. A UI: no story needs no screen.
func TestDesignUIStoryWithNoScreenFails(t *testing.T) {
	got := designFindings(t, replace(t, "**Stories:** US-02, US-03", "**Stories:** US-03"))
	if wantFinding(t, "US-02 uncited", got, "ui_story_no_screen", "US-02") && got[0].File != designRecord {
		t.Errorf("finding at %q", got[0].File)
	}
}

// C2: a screen that cites no story of the PRD fails the gate.
func TestDesignScreenThatCitesNoStoryFails(t *testing.T) {
	screen := func(stories string) func(map[string]string) {
		return func(files map[string]string) {
			files[designRecord] += "\n### S3. Settings\n\n" + stories + "\n- **S3.1 Open.** Picture `s1-1.png`, target `/settings`.\n"
		}
	}
	wantFinding(t, "no Stories line", designFindings(t, screen("")), "screen_no_story", "S3")
	wantFinding(t, "an empty Stories line", designFindings(t, screen("**Stories:**\n")), "screen_no_story", "S3")
	wantFinding(t, "an unknown label", designFindings(t, screen("**Stories:** US-01, US-07\n")), "screen_unknown_story", "US-07")
}

// C3: a state with no picture, or a picture absent from the record's
// directory, fails the gate, and the message names the state.
func TestDesignStateWithNoPictureFails(t *testing.T) {
	wantFinding(t, "no picture", designFindings(t, replace(t, "Picture `s2-1.png`, ", "")), "state_no_picture", "S2.1")
	missing := designFindings(t, func(files map[string]string) { delete(files, designDir+"s2-1.png") })
	wantFinding(t, "a missing picture", missing, "picture_missing", "S2.1", "s2-1.png")
}

// C4: a pin that the remote branch does not hold fails the gate.
func TestDesignPinThatTheRemoteBranchLacksFails(t *testing.T) {
	const branch = "loop/factorio-x-prototype"
	wantFinding(t, "no Prototype line", designFindings(t, func(files map[string]string) {
		files[designRecord] = dropLine(files[designRecord], "**Prototype:**")
	}), "pin_missing")
	wantFinding(t, "another branch", designFindings(t, replace(t, "`"+branch+"`", "`loop/factorio-y-prototype`")), "pin_branch", "loop/factorio-y-prototype")

	r, tip, off := prototypeRepo(t, map[string]string{"docs/prd/fleet.md": designFixture(t, "prd.md")})
	run := func(name, pin string) []Finding {
		commit(t, r, "docs: pin "+name, recordFiles(t, pin))
		got, err := Design(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	wantFinding(t, "a commit off the branch", run("off", off), "pin_unreachable", branch)
	if got := run("the tip", tip); len(got) != 0 {
		t.Errorf("the tip: %v", got)
	}
	base := strings.TrimSpace(git(t, r.Root, "rev-parse", "origin/main"))
	if got := run("an ancestor", base); len(got) != 0 {
		t.Errorf("an ancestor of the tip: %v", got)
	}
	git(t, r.Root, "update-ref", "-d", protoRef)
	got, err := Design(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	wantFinding(t, "no branch", got, "pin_unreachable", branch)
}

// C4: a record that the merge base holds unchanged passes with its branch
// absent, because a person can delete a prototype branch by hand.
func TestDesignUnchangedRecordPassesWithNoBranch(t *testing.T) {
	files := recordFiles(t, strings.Repeat("0", 40))
	files["docs/prd/fleet.md"] = designFixture(t, "prd.md")
	r := testRepo(t, files)
	commit(t, r, "feat: other work", map[string]string{"app.txt": "1"})
	got, err := Design(context.Background(), r)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

// C5: the pixel threshold is a percent from 0% to 5%, both ends
// inclusive, with its % sign.
func TestDesignPixelThresholdIsFromZeroToFivePercent(t *testing.T) {
	for _, mark := range []string{"pixel", "pixel 0%", "pixel 0.5%", "pixel 5%"} {
		if got := designFindings(t, replace(t, "pixel 1.5%", mark)); len(got) != 0 {
			t.Errorf("%q: %v", mark, got)
		}
	}
	for _, mark := range []string{"pixel -0.01%", "pixel 5.01%", "pixel 0.5", "pixel tight"} {
		wantFinding(t, mark, designFindings(t, replace(t, "pixel 1.5%", mark)), "pixel_threshold", "S2.1", "0% to 5%")
	}
}

func TestDesignRecordWhosePRDIsMissingFails(t *testing.T) {
	got := designFindings(t, replace(t, "docs/prd/fleet.md", "docs/prd/gone.md"))
	wantFinding(t, "a missing PRD", got, "prd_missing", "docs/prd/gone.md")
}

// dropLine drops the line of text that starts with prefix.
func dropLine(text, prefix string) string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
