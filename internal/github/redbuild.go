package github

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/iantanwx/loop/internal/brief"
)

// ExcerptLines is the most lines of one failed job's log that a repair
// brief quotes (alpha implementer-ci.ts EXCERPT_LINES).
const ExcerptLines = 40

const (
	// mostTests is the most failing tests a brief names for one job.
	mostTests = 20
	// lineWidth is the longest line a brief quotes. A minified line would
	// otherwise fill the brief.
	lineWidth = 300
	// mostChecks is the most failed jobs whose log one brief reads.
	mostChecks = 3
)

var (
	// logStamp is the timestamp the jobs API writes before each line, with
	// the byte-order mark of the first.
	logStamp = regexp.MustCompile(`^\x{FEFF}?\d{4}-\d{2}-\d{2}T[\d:.]+Z ?`)
	// logColour is a terminal colour code, as the escape byte or as the
	// two characters `^[`.
	logColour = regexp.MustCompile(`(?:\x1b|\^\[)\[[0-9;]*m`)
	// goFail is the line `go test` writes for a failed test or subtest.
	goFail = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	// goPackage is the line `go test` writes for a failed package.
	goPackage = regexp.MustCompile(`^FAIL\t(\S+)`)
	// vitestFail is the line vitest writes for a failed test file, with
	// the test when one ran.
	vitestFail = regexp.MustCompile(`^\s*FAIL\s+(\S+\.test\.[cm]?[jt]sx?)(?:\s+>\s+(.+))?$`)
	// gateFail is the line `loop gates` writes for the gate that failed.
	gateFail = regexp.MustCompile(`^FAIL ([A-Za-z0-9_.-]+)$`)
)

// plainLine is one line of a job log as a person reads it.
func plainLine(raw string) string {
	line := strings.TrimRight(logColour.ReplaceAllString(logStamp.ReplaceAllString(raw, ""), ""), " \t\r")
	if utf8.RuneCountInString(line) > lineWidth {
		line = string([]rune(line)[:lineWidth]) + "…"
	}
	return line
}

// ParseLog reads one failed job's log: each failing test once, with its
// package or file, the gate that failed, and an excerpt of at most
// ExcerptLines lines. The excerpt starts at the first failing test, or is
// the end of the log when the log names none: a lint, a type check or
// another gate. Name and URL stay empty.
func ParseLog(log string) brief.FailedCheck {
	lines := strings.Split(log, "\n")
	for i, l := range lines {
		lines[i] = plainLine(l)
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var out brief.FailedCheck
	add := func(t brief.FailedTest) {
		if !slices.Contains(out.Tests, t) {
			out.Tests = append(out.Tests, t)
		}
	}
	first := -1
	// pending holds the Go tests whose package line has not come yet.
	var pending []string
	for i, l := range lines {
		if m := goFail.FindStringSubmatch(l); m != nil {
			first = cmpFirst(first, i)
			pending = append(pending, m[1])
			continue
		}
		if m := goPackage.FindStringSubmatch(l); m != nil {
			if len(pending) == 0 {
				add(brief.FailedTest{Package: m[1], Name: "(the package failed before any test ran)"})
			}
			for _, name := range pending {
				add(brief.FailedTest{Package: m[1], Name: name})
			}
			pending = nil
			continue
		}
		if m := vitestFail.FindStringSubmatch(l); m != nil {
			name := m[2]
			if name == "" {
				name = "(the file failed before any test ran)"
			} else {
				first = cmpFirst(first, i)
			}
			add(brief.FailedTest{Package: m[1], Name: name})
			continue
		}
		if m := gateFail.FindStringSubmatch(l); m != nil {
			out.Gate = m[1]
		}
	}
	// A test whose package line never came, as when the job was cut off.
	for _, name := range pending {
		add(brief.FailedTest{Name: name})
	}
	if len(out.Tests) > mostTests {
		out.Tests = out.Tests[:mostTests]
	}
	if first < 0 {
		out.Excerpt = lines[max(0, len(lines)-ExcerptLines):]
	} else {
		out.Excerpt = lines[first:min(len(lines), first+ExcerptLines)]
	}
	return out
}

func cmpFirst(first, i int) int {
	if first < 0 {
		return i
	}
	return first
}

// RedBuild reads the failed jobs of the item's open pull request at its
// head, and parses the log of the first mostChecks of them, for a repair
// brief. It returns nil when the item has no open pull request or its
// head has no failed job. A log that does not read is named unreadable,
// and the read goes on. Each quoted line passes the redactor.
func (s *Service) RedBuild(ctx context.Context, project, item string) (*brief.RedBuild, error) {
	prj, err := s.project(project)
	if err != nil {
		return nil, err
	}
	branch, ref := s.itemBranch(ctx, project, item)
	if ref != nil {
		return nil, &RefusedError{Code: ref.Code, Rule: ref.Rule, Hint: ref.Hint}
	}
	pr, err := s.FindPR(ctx, prj, branch)
	if err != nil {
		return nil, err
	}
	if pr == nil || pr.State != "open" {
		return nil, nil
	}
	jobs, err := s.failedJobs(ctx, prj, pr.Head.SHA)
	if err != nil {
		return &brief.RedBuild{PR: pr.Number, Head: pr.Head.SHA, Unreadable: firstLine(err.Error())}, nil
	}
	if len(jobs) == 0 {
		return nil, nil
	}
	out := &brief.RedBuild{PR: pr.Number, Head: pr.Head.SHA}
	for _, j := range jobs[:min(len(jobs), mostChecks)] {
		text, err := s.jobLog(ctx, prj, j.ID)
		if err != nil {
			out.Checks = append(out.Checks, brief.FailedCheck{Name: j.Name, URL: j.HTMLURL, Unreadable: firstLine(err.Error())})
			continue
		}
		c := ParseLog(text)
		c.Name, c.URL = j.Name, j.HTMLURL
		for i, l := range c.Excerpt {
			c.Excerpt[i] = s.cfg.Redactor.Redact(l)
		}
		out.Checks = append(out.Checks, c)
	}
	return out, nil
}

// firstLine is the first line of an error, at most 200 characters.
func firstLine(msg string) string {
	msg, _, _ = strings.Cut(msg, "\n")
	if utf8.RuneCountInString(msg) > 200 {
		msg = string([]rune(msg)[:200])
	}
	return msg
}
