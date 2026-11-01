package qa

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

const run = "r-1"

// webAPIPlan drives web and api, with visual QA when design is not "".
func webAPIPlan(design string) Plan {
	return MakePlan([]App{{Name: "api"}, {Name: "web"}}, nil, design)
}

func pass(kind, app string) Check {
	return Check{Kind: kind, App: app, Result: "pass", Goal: "Save a strategy", Steps: 14, Minutes: 6,
		Findings: []Finding{}}
}

// passing is a report that passes each check of webAPIPlan("").
func passing() Report {
	return Report{Head: "4f1c", Verify: "present", Checks: []Check{pass("fuzzy_uat", "web"), pass("fuzzy_uat", "api")},
		Bugs: []string{}}
}

// failing is passing with one fail finding on web that cites shot.
func failing(shot string) Report {
	r := passing()
	r.Checks[0].Result = "fail"
	r.Checks[0].Findings = []Finding{{Summary: "The save button does nothing", Severity: "fail",
		Evidence: []string{shot}, Steps: []string{"open /strategies/new", "press Save"}}}
	return r
}

// bugs answers for the bugs of the test runs: x-1 a QA bug of run, x-2 a QA
// bug of another run, x-3 a bug the coordinator filed.
func bugs(id string) (QABug, bool) {
	b, ok := map[string]QABug{"x-1": {"qa", run}, "x-2": {"qa", "r-2"}, "x-3": {"operator", ""}}[id]
	return b, ok
}

// rule is the rule of Validate's refusal, or "" when it accepts. A refusal
// with another code fails the test.
func rule(t *testing.T, r Report, p Plan, files map[string][]byte, filed ...string) string {
	t.Helper()
	ref := Validate(r, p, run, files, filed, bugs)
	if ref == nil {
		return ""
	}
	if ref.Code != cli.CodeQAReportRefused {
		t.Fatalf("code %s, want %s", ref.Code, cli.CodeQAReportRefused)
	}
	return ref.Rule
}

// C18: a report covers each planned app with fuzzy UAT, and holds no
// check the plan does not make.
func TestC18ValidateRefusesAReportThatDoesNotMatchThePlan(t *testing.T) {
	if got := rule(t, passing(), webAPIPlan(""), nil); got != "" {
		t.Fatalf("a report that covers the plan: rule %q, want none", got)
	}

	noAPI := passing()
	noAPI.Checks = noAPI.Checks[:1]
	ref := Validate(noAPI, webAPIPlan(""), run, nil, nil, bugs)
	if ref == nil || ref.Rule != "check_missing" || !strings.Contains(ref.Hint, "api") {
		t.Fatalf("no fuzzy_uat check for api: %+v, want check_missing that names api", ref)
	}

	cases := []struct {
		name string
		edit func(*Report)
	}{
		{"an app outside the plan", func(r *Report) { r.Checks = append(r.Checks, pass("fuzzy_uat", "ops")) }},
		{"a second check of web", func(r *Report) { r.Checks = append(r.Checks, pass("fuzzy_uat", "web")) }},
		{"a visual check with no design skill", func(r *Report) { r.Checks = append(r.Checks, pass("visual", "web")) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := passing()
			c.edit(&r)
			if got := rule(t, r, webAPIPlan(""), nil); got != "check_unplanned" {
				t.Fatalf("rule %q, want check_unplanned", got)
			}
		})
	}
}

// C18: with a design skill, each driven app needs a visual check too.
func TestC18ValidateNeedsAVisualCheckWithADesignSkill(t *testing.T) {
	p := webAPIPlan("loop-impeccable")
	ref := Validate(passing(), p, run, nil, nil, bugs)
	if ref == nil || ref.Rule != "check_missing" || !strings.Contains(ref.Hint, "visual") {
		t.Fatalf("no visual check: %+v, want check_missing that names visual", ref)
	}
	r := passing()
	r.Checks = append(r.Checks, pass("visual", "web"), pass("visual", "api"))
	if got := rule(t, r, p, nil); got != "" {
		t.Fatalf("each check: rule %q, want none", got)
	}
}

// The claim on the verify skill matches the plan, and a field outside its
// values is a bad report.
func TestValidateRefusesABadShapeOrAWrongVerifyClaim(t *testing.T) {
	none := MakePlan(nil, nil, "")
	missing := Report{Head: "4f1c", Verify: "missing", Checks: []Check{}, Bugs: []string{}}
	if got := rule(t, missing, none, nil); got != "" {
		t.Fatalf("verify missing with no skill: rule %q, want none", got)
	}
	if got := rule(t, missing, webAPIPlan(""), nil); got != "verify_skill_present" {
		t.Fatalf("verify missing with skills: rule %q, want verify_skill_present", got)
	}
	if got := rule(t, passing(), none, nil); got != "verify_skill_missing" {
		t.Fatalf("verify present with no skill: rule %q, want verify_skill_missing", got)
	}

	cases := []struct {
		name string
		edit func(*Report)
	}{
		{"no head", func(r *Report) { r.Head = "" }},
		{"verify maybe", func(r *Report) { r.Verify = "maybe" }},
		{"kind smoke", func(r *Report) { r.Checks[0].Kind = "smoke" }},
		{"result skipped", func(r *Report) { r.Checks[0].Result = "skipped" }},
		{"severity warn", func(r *Report) {
			r.Checks[0].Findings = []Finding{{Summary: "x", Severity: "warn"}}
		}},
		{"negative steps", func(r *Report) { r.Checks[0].Steps = -1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := passing()
			c.edit(&r)
			if got := rule(t, r, webAPIPlan(""), nil); got != "bad_report" {
				t.Fatalf("rule %q, want bad_report", got)
			}
		})
	}
}

// C21: an unstartable app needs a bug of this run, and makes the report
// neutral unless a check fails.
func TestC21UnstartableAppNeedsABugAndConcludesNeutral(t *testing.T) {
	unstartable := func(r *Report, i int) {
		r.Checks[i] = Check{Kind: "fuzzy_uat", App: r.Checks[i].App, Result: "unstartable", Findings: []Finding{}}
	}

	all := passing()
	unstartable(&all, 0)
	unstartable(&all, 1)
	all.Bugs = []string{"x-1"}
	if got := rule(t, all, webAPIPlan(""), nil, "x-1"); got != "" {
		t.Fatalf("every check unstartable with a bug: rule %q, want none", got)
	}
	if got := all.Conclusion(); got != "neutral" {
		t.Fatalf("every check unstartable: Conclusion %q, want neutral", got)
	}
	all.Bugs = []string{}
	if got := rule(t, all, webAPIPlan(""), nil); got != "unstartable_without_bug" {
		t.Fatalf("every check unstartable with no bug: rule %q, want unstartable_without_bug", got)
	}

	withFinding := passing()
	unstartable(&withFinding, 1)
	withFinding.Bugs = []string{"x-1"}
	withFinding.Checks[1].Findings = []Finding{{Summary: "it hangs", Severity: "note"}}
	if got := rule(t, withFinding, webAPIPlan(""), nil, "x-1"); got != "result_mismatch" {
		t.Fatalf("an unstartable check with a finding: rule %q, want result_mismatch", got)
	}

	failed := failing("web/save.png")
	unstartable(&failed, 1)
	failed.Bugs = []string{"x-1"}
	if got := failed.Conclusion(); got != "failure" {
		t.Fatalf("one check unstartable and one fail: Conclusion %q, want failure", got)
	}

	mixed := passing()
	unstartable(&mixed, 1)
	mixed.Bugs = []string{"x-1"}
	if got := rule(t, mixed, webAPIPlan(""), nil, "x-1"); got != "" {
		t.Fatalf("web passes, api unstartable: rule %q, want none", got)
	}
	if got := mixed.Conclusion(); got != "neutral" {
		t.Fatalf("web passes, api unstartable: Conclusion %q, want neutral", got)
	}
	title, summary, text := Render(mixed, nil)
	if title != "QA did not drive every app" {
		t.Fatalf("title %q, want %q", title, "QA did not drive every app")
	}
	for _, want := range []string{"fuzzy_uat api: unstartable", "fuzzy_uat web: pass", "x-1"} {
		if !strings.Contains(summary+text, want) {
			t.Fatalf("summary and text hold no %q:\n%s\n%s", want, summary, text)
		}
	}
}

// C22's rules in Validate: the report names each bug this run filed, and
// only those.
func TestValidateNamesEachBugTheRunFiled(t *testing.T) {
	for _, id := range []string{"x-2", "x-3", "x-9"} {
		r := passing()
		r.Bugs = []string{id}
		if got := rule(t, r, webAPIPlan(""), nil); got != "bug_not_filed" {
			t.Fatalf("bugs %s: rule %q, want bug_not_filed", id, got)
		}
	}
	if got := rule(t, passing(), webAPIPlan(""), nil, "x-1"); got != "bug_not_named" {
		t.Fatalf("a filed bug left out: rule %q, want bug_not_named", got)
	}
}

// C23: a check stays inside its bounds, its result is the one its
// findings make, and one failing check fails the report.
func TestC23ReportInsideItsBoundsWithTheVerdictItsFindingsMake(t *testing.T) {
	visual := webAPIPlan("loop-impeccable")
	cases := []struct {
		name string
		edit func(*Report)
		want string
	}{
		{"25 steps, 20 minutes, 8 screens", func(r *Report) {
			r.Checks[0].Steps, r.Checks[0].Minutes, r.Checks[0].Screens = 25, 20, 8
		}, ""},
		{"26 steps", func(r *Report) { r.Checks[0].Steps = 26 }, "over_bound"},
		{"21 minutes", func(r *Report) { r.Checks[0].Minutes = 21 }, "over_bound"},
		{"9 screens", func(r *Report) { r.Checks[2].Screens = 9 }, "over_bound"},
		{"pass with a fail finding", func(r *Report) {
			r.Checks[0].Findings = []Finding{{Summary: "x", Severity: "fail", Evidence: []string{"a.png"}}}
		}, "result_mismatch"},
		{"fail with no finding", func(r *Report) { r.Checks[0].Result = "fail" }, "result_mismatch"},
		{"fail with a note only", func(r *Report) {
			r.Checks[0].Result = "fail"
			r.Checks[0].Findings = []Finding{{Summary: "x", Severity: "note"}}
		}, "result_mismatch"},
		{"pass with a note", func(r *Report) {
			r.Checks[0].Findings = []Finding{{Summary: "the label wraps", Severity: "note"}}
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := passing()
			r.Checks = append(r.Checks, pass("visual", "web"), pass("visual", "api"))
			c.edit(&r)
			if got := rule(t, r, visual, nil); got != c.want {
				t.Fatalf("rule %q, want %q", got, c.want)
			}
		})
	}

	r := passing()
	r.Checks = append(r.Checks, pass("visual", "web"), pass("visual", "api"))
	if got := r.Conclusion(); got != "success" {
		t.Fatalf("every check passes: Conclusion %q, want success", got)
	}
	r.Checks[2].Result = "fail"
	r.Checks[2].Findings = []Finding{{Summary: "The save button leaves the screen at 390x844", Severity: "fail",
		Evidence: []string{"web/visual/save-390.png"}}}
	if got := rule(t, r, visual, map[string][]byte{"web/visual/save-390.png": png}); got != "" {
		t.Fatalf("a failing visual check: rule %q, want none", got)
	}
	if got := r.Conclusion(); got != "failure" {
		t.Fatalf("fuzzy UAT passes and visual fails: Conclusion %q, want failure", got)
	}
	missing := Report{Head: "4f1c", Verify: "missing"}
	if got := missing.Conclusion(); got != "neutral" {
		t.Fatalf("verify missing: Conclusion %q, want neutral", got)
	}
}

// png is the head of a PNG file.
var png = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

// C25: each evidence file is cited, each citation names a file, the files
// keep their types and sizes, and no text holds an image.
func TestC25EvidenceIsCitedTypedBoundedAndHoldsNoImageText(t *testing.T) {
	pngB64 := base64.StdEncoding.EncodeToString(append(png, bytes.Repeat([]byte{7}, 30)...))
	jpegB64 := base64.StdEncoding.EncodeToString(append([]byte{0xff, 0xd8, 0xff, 0xe0}, bytes.Repeat([]byte{7}, 30)...))
	many := map[string][]byte{}
	var cited []string
	for i := range 41 {
		p := fmt.Sprintf("web/%02d.png", i)
		many[p], cited = png, append(cited, p)
	}
	cases := []struct {
		name   string
		report Report
		files  map[string][]byte
		want   string
	}{
		{"a cited screenshot", failing("web/save.png"), map[string][]byte{"web/save.png": png}, ""},
		{"a cited log", failing("web/console.log"), map[string][]byte{"web/console.log": []byte("TypeError: x is undefined\n")}, ""},
		{"a file no finding cites", failing("web/save.png"),
			map[string][]byte{"web/save.png": png, "web/extra.png": png}, "evidence_uncited"},
		{"a cited path with no file", failing("web/save.png"), map[string][]byte{}, "evidence_missing"},
		{"a fail finding with no evidence", func() Report {
			r := failing("x")
			r.Checks[0].Findings[0].Evidence = nil
			return r
		}(), nil, "fail_without_evidence"},
		{"a gif", failing("web/save.gif"), map[string][]byte{"web/save.gif": []byte("GIF89a")}, "evidence_type"},
		{"a file of 3 MiB", failing("web/save.png"),
			map[string][]byte{"web/save.png": make([]byte, 3<<20)}, "evidence_size"},
		{"41 files", func() Report {
			r := failing("x")
			r.Checks[0].Findings[0].Evidence = cited
			return r
		}(), many, "evidence_size"},
		{"21 MiB in all", func() Report {
			r := failing("x")
			r.Checks[0].Findings[0].Evidence = cited[:11]
			return r
		}(), func() map[string][]byte {
			m := map[string][]byte{}
			for _, p := range cited[:11] {
				m[p] = make([]byte, 2<<20-1)
			}
			return m
		}(), "evidence_size"},
		{"a data URI in a summary", func() Report {
			r := failing("web/save.png")
			r.Checks[0].Findings[0].Summary = "see data:image/png;base64,AAAA"
			return r
		}(), map[string][]byte{"web/save.png": png}, "image_data"},
		{"base64 JPEG in the notes", func() Report {
			r := passing()
			r.Notes = "screenshot: " + jpegB64
			return r
		}(), nil, "image_data"},
		{"base64 PNG in a text file", failing("web/dom.json"),
			map[string][]byte{"web/dom.json": []byte(`{"shot":"` + pngB64 + `"}`)}, "image_data"},
		{"base64 that is no image in a text file", failing("web/dom.json"),
			map[string][]byte{"web/dom.json": []byte(`{"token":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("ab"), 20)) + `"}`)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rule(t, c.report, webAPIPlan(""), c.files); got != c.want {
				t.Fatalf("rule %q, want %q", got, c.want)
			}
		})
	}
}

// Render titles the check run by its conclusion, and links each evidence
// file of each finding.
func TestRenderLinksEachEvidenceFile(t *testing.T) {
	r := failing("web/save.png")
	r.Checks[0].Findings = append(r.Checks[0].Findings,
		Finding{Summary: "The console logs an error", Severity: "note", Evidence: []string{"web/console.log"}})
	links := map[string]string{
		"web/save.png":    "https://github.com/o/r/blob/c0ffee/web/save.png",
		"web/console.log": "https://github.com/o/r/blob/c0ffee/web/console.log",
	}
	title, summary, text := Render(r, links)
	if title != "QA failed" {
		t.Fatalf("title %q, want %q", title, "QA failed")
	}
	if !strings.Contains(summary, "fuzzy_uat web: fail") || !strings.Contains(summary, "fuzzy_uat api: pass") {
		t.Fatalf("summary names no result of each check:\n%s", summary)
	}
	for _, want := range []string{
		"[web/save.png](https://github.com/o/r/blob/c0ffee/web/save.png)",
		"[web/console.log](https://github.com/o/r/blob/c0ffee/web/console.log)",
		"The save button does nothing",
		"press Save",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text holds no %q:\n%s", want, text)
		}
	}

	if title, _, _ := Render(passing(), nil); title != "QA passed" {
		t.Fatalf("a passing report: title %q, want %q", title, "QA passed")
	}
	_, summary, _ = Render(Report{Head: "4f1c", Verify: "missing"}, nil)
	if !strings.Contains(summary, "no verify skill") {
		t.Fatalf("verify missing: summary %q names no missing verify skill", summary)
	}
}
