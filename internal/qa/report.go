package qa

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/cli"
)

// Report is what one QA run hands in. Verify is "present" or "missing".
type Report struct {
	Head   string   `json:"head"`
	Verify string   `json:"verify"`
	Checks []Check  `json:"checks"`
	Bugs   []string `json:"bugs"`
	Notes  string   `json:"notes"`
}

// Check is one fuzzy UAT pass or one visual QA pass over one app. Kind is
// "fuzzy_uat" or "visual", and Result "pass", "fail" or "unstartable".
type Check struct {
	Kind     string    `json:"kind"`
	App      string    `json:"app"`
	Result   string    `json:"result"`
	Goal     string    `json:"goal"`
	Steps    int       `json:"steps"`
	Minutes  int       `json:"minutes"`
	Screens  int       `json:"screens"`
	Findings []Finding `json:"findings"`
}

// Finding is one fault or remark of a check. Severity is "fail" or "note",
// and Evidence holds the paths of its evidence files.
type Finding struct {
	Summary  string   `json:"summary"`
	Severity string   `json:"severity"`
	Evidence []string `json:"evidence"`
	Steps    []string `json:"steps"`
}

// QABug is what Validate needs to know of one bug id.
type QABug struct{ Source, Run string }

const (
	verifyPresent     = "present"
	verifyMissing     = "missing"
	kindFuzzyUAT      = "fuzzy_uat"
	kindVisual        = "visual"
	resultPass        = "pass"
	resultFail        = "fail"
	resultUnstartable = "unstartable"
	severityFail      = "fail"
	severityNote      = "note"
	sourceQA          = "qa"
)

// The bounds of one report's evidence.
const (
	maxFiles    = 40
	maxFileSize = 2 << 20
	maxEvidence = 20 << 20
)

// evidenceTypes holds each extension an evidence file may have, true for a
// text file.
var evidenceTypes = map[string]bool{".png": false, ".jpeg": false, ".jpg": false, ".webp": false,
	".txt": true, ".log": true, ".json": true}

// name is the check's kind and app, as each hint and the check run's text
// write it.
func (c Check) name() string { return c.Kind + " " + c.App }

// Conclusion is "failure" when a check fails, then "neutral" when no
// verify skill exists or a check is unstartable, and "success" otherwise.
func (r Report) Conclusion() string {
	neutral := r.Verify == verifyMissing
	for _, c := range r.Checks {
		switch c.Result {
		case resultFail:
			return "failure"
		case resultUnstartable:
			neutral = true
		}
	}
	if neutral {
		return "neutral"
	}
	return "success"
}

// Validate refuses r with cli.CodeQAReportRefused and the first rule it
// breaks. p is the plan of the run, files the evidence by path, filed the
// bugs the run filed, and bug answers for any bug id.
func Validate(r Report, p Plan, run string, files map[string][]byte, filed []string, bug func(id string) (QABug, bool)) *cli.Refusal {
	if ref := validateShape(r); ref != nil {
		return ref
	}
	if ref := validatePlan(r, p); ref != nil {
		return ref
	}
	if ref := validateChecks(r, p.Bounds); ref != nil {
		return ref
	}
	if ref := validateEvidence(r, files); ref != nil {
		return ref
	}
	return validateBugs(r, run, filed, bug)
}

func refuse(rule, format string, a ...any) *cli.Refusal {
	return &cli.Refusal{Code: cli.CodeQAReportRefused, Rule: rule, Hint: fmt.Sprintf(format, a...)}
}

// validateShape refuses a field outside its values.
func validateShape(r Report) *cli.Refusal {
	if r.Head == "" {
		return refuse("bad_report", "the report names no head")
	}
	if r.Verify != verifyPresent && r.Verify != verifyMissing {
		return refuse("bad_report", "verify is %q; want present or missing", r.Verify)
	}
	for _, c := range r.Checks {
		switch {
		case c.Kind != kindFuzzyUAT && c.Kind != kindVisual:
			return refuse("bad_report", "the check of %s has the kind %q; want fuzzy_uat or visual", c.App, c.Kind)
		case c.Result != resultPass && c.Result != resultFail && c.Result != resultUnstartable:
			return refuse("bad_report", "the %s check has the result %q; want pass, fail or unstartable", c.name(), c.Result)
		case c.Steps < 0 || c.Minutes < 0 || c.Screens < 0:
			return refuse("bad_report", "the %s check counts less than 0 steps, minutes or screens", c.name())
		}
		for _, f := range c.Findings {
			if f.Severity != severityFail && f.Severity != severityNote {
				return refuse("bad_report", "a finding of the %s check has the severity %q; want fail or note", c.name(), f.Severity)
			}
		}
	}
	return nil
}

// validatePlan refuses a verify claim the tree does not hold, a check the
// plan does not make, and a planned check the report leaves out.
func validatePlan(r Report, p Plan) *cli.Refusal {
	switch {
	case r.Verify == verifyMissing && len(p.Drive) > 0:
		return refuse("verify_skill_present", "the tree at the head holds the verify skills of %s", strings.Join(p.Drive, ", "))
	case r.Verify == verifyPresent && len(p.Drive) == 0:
		return refuse("verify_skill_missing", "the tree at the head holds no .claude/skills/verify-<app>/SKILL.md")
	}
	var planned []string
	for _, app := range p.Drive {
		planned = append(planned, Check{Kind: kindFuzzyUAT, App: app}.name())
		if p.Visual {
			planned = append(planned, Check{Kind: kindVisual, App: app}.name())
		}
	}
	seen := map[string]bool{}
	for _, c := range r.Checks {
		if !slices.Contains(planned, c.name()) {
			return refuse("check_unplanned", "the plan makes no %s check", c.name())
		}
		if seen[c.name()] {
			return refuse("check_unplanned", "the report holds the %s check twice", c.name())
		}
		seen[c.name()] = true
	}
	for _, n := range planned {
		if !seen[n] {
			return refuse("check_missing", "the report holds no %s check", n)
		}
	}
	return nil
}

// validateChecks refuses a check over its bounds, a result its findings do
// not make, and a fail finding with no evidence.
func validateChecks(r Report, b Bounds) *cli.Refusal {
	for _, c := range r.Checks {
		for _, n := range []struct {
			what       string
			got, bound int
		}{{"steps", c.Steps, b.Steps}, {"minutes", c.Minutes, b.Minutes}, {"screens", c.Screens, b.Screens}} {
			if n.got > n.bound {
				return refuse("over_bound", "the %s check counts %d %s, over the bound of %d", c.name(), n.got, n.what, n.bound)
			}
		}
		failed := slices.ContainsFunc(c.Findings, func(f Finding) bool { return f.Severity == severityFail })
		if c.Result == resultPass && failed || c.Result == resultFail && !failed ||
			c.Result == resultUnstartable && len(c.Findings) > 0 {
			return refuse("result_mismatch", "the %s check is %s; a check fails exactly when a finding fails, and an unstartable check holds no finding",
				c.name(), c.Result)
		}
		for _, f := range c.Findings {
			if f.Severity == severityFail && len(f.Evidence) == 0 {
				return refuse("fail_without_evidence", "the fail finding %q of the %s check cites no evidence file", f.Summary, c.name())
			}
		}
	}
	return nil
}

// validateEvidence refuses evidence of a wrong type or size, a citation
// with no file, a file no finding cites, and text that holds an image.
func validateEvidence(r Report, files map[string][]byte) *cli.Refusal {
	if len(files) > maxFiles {
		return refuse("evidence_size", "the evidence holds %d files, over the bound of %d", len(files), maxFiles)
	}
	paths := slices.Sorted(maps.Keys(files))
	total := 0
	for _, p := range paths {
		if _, ok := evidenceTypes[strings.ToLower(path.Ext(p))]; !ok {
			return refuse("evidence_type", "%s is no png, jpeg, webp, txt, log or json file", p)
		}
		if len(files[p]) > maxFileSize {
			return refuse("evidence_size", "%s holds %d bytes, over the bound of %d", p, len(files[p]), maxFileSize)
		}
		total += len(files[p])
	}
	if total > maxEvidence {
		return refuse("evidence_size", "the evidence holds %d bytes, over the bound of %d", total, maxEvidence)
	}

	cited := map[string]bool{}
	texts := []string{r.Notes}
	for _, c := range r.Checks {
		texts = append(texts, c.Goal)
		for _, f := range c.Findings {
			for _, p := range f.Evidence {
				if _, ok := files[p]; !ok {
					return refuse("evidence_missing", "the %s check cites %s, and the evidence holds no such file", c.name(), p)
				}
				cited[p] = true
			}
			texts = append(texts, f.Summary)
			texts = append(texts, f.Steps...)
		}
	}
	for _, p := range paths {
		if !cited[p] {
			return refuse("evidence_uncited", "no finding cites %s", p)
		}
	}

	for _, t := range texts {
		if holdsImage(t) {
			return refuse("image_data", "the report's text holds image data; put each image in an evidence file")
		}
	}
	for _, p := range paths {
		if evidenceTypes[strings.ToLower(path.Ext(p))] && holdsImage(string(files[p])) {
			return refuse("image_data", "%s holds image data; put each image in a file of its own", p)
		}
	}
	return nil
}

// base64Run is a run of base64 long enough to decode an image's header.
var base64Run = regexp.MustCompile(`[A-Za-z0-9+/]{12,}`)

// imageHeads are the first bytes of a PNG and of a JPEG file.
var imageHeads = [][]byte{[]byte("\x89PNG\r\n\x1a\n"), {0xff, 0xd8, 0xff}}

// holdsImage reports whether text holds a data:image/ URI, or a base64 run
// that decodes to the header of a PNG or a JPEG.
func holdsImage(text string) bool {
	if strings.Contains(strings.ToLower(text), "data:image/") {
		return true
	}
	for _, run := range base64Run.FindAllString(text, -1) {
		head, err := base64.StdEncoding.DecodeString(run[:12])
		if err != nil {
			continue
		}
		for _, h := range imageHeads {
			if bytes.HasPrefix(head, h) {
				return true
			}
		}
	}
	return false
}

// validateBugs refuses a bug this run did not file, a filed bug the report
// leaves out, and an unstartable check with no bug.
func validateBugs(r Report, run string, filed []string, bug func(id string) (QABug, bool)) *cli.Refusal {
	for _, id := range r.Bugs {
		if b, ok := bug(id); !ok || b.Source != sourceQA || b.Run != run {
			return refuse("bug_not_filed", "%s is no bug that this run filed with --source qa", id)
		}
	}
	for _, id := range filed {
		if !slices.Contains(r.Bugs, id) {
			return refuse("bug_not_named", "the report leaves out %s, a bug this run filed", id)
		}
	}
	for _, c := range r.Checks {
		if c.Result == resultUnstartable && len(r.Bugs) == 0 {
			return refuse("unstartable_without_bug", "the %s check is unstartable, and the report names no bug", c.name())
		}
	}
	return nil
}
