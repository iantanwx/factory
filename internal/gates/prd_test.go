package gates

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/pack"
)

const prdFile = "docs/prd/loop-a3f8-engine.md"

// sizingBlock is a sizing block that passes, so each case changes one line.
func sizingBlock(over map[string]string) string {
	parts := []struct{ key, text string }{
		{"stories", "**Stories:** 6"},
		{"total", "**Estimated total:** 4,900 lines"},
		{"lands", "**Lands independently?** yes"},
		{"provable", "**Promises provable alone?** yes"},
		{"verdict", "**Verdict:** proceed"},
	}
	var out []string
	for _, p := range parts {
		t, ok := over[p.key]
		if !ok {
			t = p.text
		}
		if t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func TestPRDCompleteSizingReportsNothing(t *testing.T) {
	if got := checkPRD(prdFile, sizingBlock(nil)); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestPRDEachMissingLineIsAFinding(t *testing.T) {
	for _, k := range []string{"stories", "lands", "verdict"} {
		if got := rules(checkPRD(prdFile, sizingBlock(map[string]string{k: ""}))); !slices.Equal(got, []string{"sizing"}) {
			t.Errorf("%s missing = %v", k, got)
		}
	}
}

func TestPRDQuestionMarkInTheFieldName(t *testing.T) {
	// `Lands independently?` holds a regexp metacharacter. Unescaped, the
	// line reads as absent in every PRD.
	if v, ok := fieldValue("**Lands independently?** yes", "Lands independently?"); !ok || v != "yes" {
		t.Fatalf("fieldValue = %q %v", v, ok)
	}
}

func TestPRDPlaceholderIsNoAnswer(t *testing.T) {
	if got := rules(checkPRD(prdFile, sizingBlock(map[string]string{"lands": "**Lands independently?** <yes / no>"}))); !slices.Equal(got, []string{"placeholder"}) {
		t.Fatalf("placeholder = %v", got)
	}
	if got := rules(checkPRD(prdFile, sizingBlock(map[string]string{"stories": "**Stories:**"}))); !slices.Equal(got, []string{"placeholder"}) {
		t.Fatalf("empty = %v", got)
	}
}

func TestPRDTooLargeVerdictStopsThePRD(t *testing.T) {
	got := checkPRD(prdFile, sizingBlock(map[string]string{"verdict": "**Verdict:** too large, split into two epics"}))
	if !slices.Equal(rules(got), []string{"verdict"}) {
		t.Fatalf("too large = %v", got)
	}
	// The finding quotes the verdict and names who acts.
	if !strings.Contains(got[0].Message, "too large") || !strings.Contains(got[0].Message, "doc-reviewer") {
		t.Fatalf("message = %q", got[0].Message)
	}
}

func TestPRDPlaceholderVerdictReportsOnce(t *testing.T) {
	// Two findings would send the author looking for two problems.
	got := rules(checkPRD(prdFile, sizingBlock(map[string]string{"verdict": "**Verdict:** proceed / too large, <recommended split>"})))
	if !slices.Equal(got, []string{"placeholder"}) {
		t.Fatalf("got %v", got)
	}
}

func TestPRDProceedIsReadFromTheStart(t *testing.T) {
	for v, want := range map[string]bool{"proceed": true, "Proceed.": true, "do not proceed": false, "too large": false, "": false} {
		if got := checkPRD(prdFile, sizingBlock(map[string]string{"verdict": "**Verdict:**   " + v + "  "})); (len(got) == 0) != want {
			t.Errorf("verdict %q = %v", v, got)
		}
	}
}

// The pack's PRD template and the two PRD gates agree: the raw template
// fails on its sizing placeholders alone, and passes once they are filled.
func TestPRDTemplateAgreesWithTheGates(t *testing.T) {
	raw, err := fs.ReadFile(pack.Files, "skills/loop-brainstorm/prd.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	got := rules(checkPRD(prdFile, text))
	if len(got) != len(sizing) || slices.ContainsFunc(got, func(r string) bool { return r != "placeholder" }) {
		t.Fatalf("raw template = %v", got)
	}
	r := testRepo(t, map[string]string{prdFile: text})
	if got, err := Citations(context.Background(), r); err != nil || len(got) != 0 {
		t.Fatalf("citations over the template = %v %v", got, err)
	}
	var kept []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, "**") {
			kept = append(kept, l)
		}
	}
	if got := checkPRD(prdFile, strings.Join(kept, "\n")+"\n"+sizingBlock(nil)); len(got) != 0 {
		t.Fatalf("filled template = %v", got)
	}
}

func TestPRDGateReadsEachPRD(t *testing.T) {
	r := testRepo(t, map[string]string{
		prdFile:                     sizingBlock(nil),
		"docs/prd/loop-b1-other.md": sizingBlock(map[string]string{"verdict": "**Verdict:** too large"}),
		"docs/prd/notes.txt":        "no sizing",
	})
	got, err := PRD(context.Background(), r)
	if err != nil || !slices.Equal(rules(got), []string{"verdict"}) || got[0].File != "docs/prd/loop-b1-other.md" {
		t.Fatalf("got %v %v", got, err)
	}
	if got, err := PRD(context.Background(), testRepo(t, nil)); err != nil || len(got) != 0 {
		t.Fatalf("no docs/prd = %v %v", got, err)
	}
}

// uiConfig is a project config that declares a UI.
const uiConfig = "merge:\n  mechanism: direct\nui:\n  kind: web\n  paths: [web/**]\n"

// storyPRD is a PRD that passes the sizing check, with one story whose
// lines under the heading are ui.
func storyPRD(ui string) string {
	return "## Stories\n\n### US-01 Run a backtest\n\n" + ui + "As a trader, I can run a backtest.\n\n## Scope check\n\n" + sizingBlock(nil)
}

// TestC9StoryWithNoUILineFailsInAUIProject proves C9: in a repository whose
// config declares a UI, a story with no UI line, with two of them, or with
// a value other than yes and no fails with rule ui_line, and the message
// names the story.
func TestC9StoryWithNoUILineFailsInAUIProject(t *testing.T) {
	for _, c := range []struct{ name, ui string }{
		{"no line", ""},
		{"two lines", "**UI:** yes\n\n**UI:** no\n\n"},
		{"other value", "**UI:** maybe\n\n"},
	} {
		r := testRepo(t, map[string]string{".loop/config.yaml": uiConfig, prdFile: storyPRD(c.ui)})
		got, err := PRD(context.Background(), r)
		if err != nil || !slices.Equal(rules(got), []string{"ui_line"}) {
			t.Errorf("%s = %v %v", c.name, got, err)
			continue
		}
		if !strings.Contains(got[0].Message, "US-01") || got[0].File != prdFile || got[0].Line != 3 {
			t.Errorf("%s: finding %+v names no story", c.name, got[0])
		}
	}
	for _, ui := range []string{"**UI:** yes\n\n", "**UI:** no\n\n"} {
		r := testRepo(t, map[string]string{".loop/config.yaml": uiConfig, prdFile: storyPRD(ui)})
		if got, err := PRD(context.Background(), r); err != nil || len(got) != 0 {
			t.Errorf("%q = %v %v", ui, got, err)
		}
	}
}

// TestC10ProjectWithNoUIAsksForNoUILine proves C10: in a repository whose
// config declares no UI, or that holds no config, a story with no UI line
// passes the prd gate. The same PRD fails once the config gains a ui key,
// so the key alone asks for the line.
func TestC10ProjectWithNoUIAsksForNoUILine(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no ui key": {".loop/config.yaml": "merge:\n  mechanism: direct\n", prdFile: storyPRD("")},
		"no config": {prdFile: storyPRD("")},
	} {
		if got, err := PRD(context.Background(), testRepo(t, files)); err != nil || len(got) != 0 {
			t.Errorf("%s = %v %v", name, got, err)
		}
	}
	r := testRepo(t, map[string]string{".loop/config.yaml": uiConfig, prdFile: storyPRD("")})
	if got, err := PRD(context.Background(), r); err != nil || !slices.Equal(rules(got), []string{"ui_line"}) {
		t.Fatalf("ui key = %v %v", got, err)
	}
}
