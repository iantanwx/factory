// Package ci holds what the loop's own CI (.github/workflows/ci.yml)
// decides, and the tests that hold the workflow and action.yml to it.
//
// The gates and acceptance jobs each ask `go run ./tools/ci/docsonly`
// whether a change touched documents alone. Such a change runs only
// `loop gates --docs`, and skips the stack gates and the acceptance
// suite. The verify job then joins every job, because the loop counts a
// skipped check as green.
package ci

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// VerifyJob is the job in ci.yml that joins every other job's result.
// ReleaseJob is the job that releases a push to main, after VerifyJob.
const (
	VerifyJob  = "verify"
	ReleaseJob = "release"
)

// docDirs are the directories whose Markdown counts as documentation.
// docs/evidence/ is code: internal/release/provision_test.go reads the
// evidence of the beads contract, and TestNoGoTestReadsADocument holds
// every such path out of this list.
var docDirs = []string{"docs/"}

var notDocDirs = []string{"docs/evidence/"}

// IsDoc is true when a change to this one path can move no gate outside
// `loop gates --docs`. The list is narrow on purpose. The binary embeds
// pack/, and a run reads .loop/policy/, so their Markdown is code.
func IsDoc(file string) bool {
	if file == "README.md" {
		return true
	}
	if !strings.HasSuffix(file, ".md") || strings.Contains(file, "..") {
		return false
	}
	for _, dir := range notDocDirs {
		if strings.HasPrefix(file, dir) {
			return false
		}
	}
	for _, dir := range docDirs {
		if strings.HasPrefix(file, dir) {
			return true
		}
	}
	return false
}

// Event holds the fields of the Actions event that name a base. Actions
// passes each one as a string.
type Event struct {
	Name   string
	PRBase string
	Before string
	Forced string
}

// Base is the commit a change compares against. An empty SHA means no
// commit can serve, and Why says why.
type Base struct {
	SHA string
	Why string
}

var zeros = regexp.MustCompile(`^0*$`)

// BaseOf is the pull request's base for a pull request, and the commit
// before the push for a push. Any doubt names no base.
func BaseOf(e Event) Base {
	if e.Name == "pull_request" {
		if e.PRBase == "" {
			return Base{Why: "the event names no pull request base"}
		}
		return Base{SHA: e.PRBase}
	}
	if e.Name != "push" {
		return Base{Why: fmt.Sprintf("a %s event names no base", e.Name)}
	}
	if e.Forced == "true" {
		return Base{Why: "a force-push replaced history, so the commit before it proves nothing"}
	}
	if zeros.MatchString(e.Before) {
		return Base{Why: "the push created the branch, so no commit came before it"}
	}
	return Base{SHA: e.Before}
}

// Decision is whether a change runs the documents gates alone, and why.
type Decision struct {
	Docs bool
	Why  string
}

// Decide answers Docs only when every file is a document. files is nil
// when the diff could not be read. An empty diff runs every gate too.
func Decide(base Base, files []string) Decision {
	if base.SHA == "" {
		return Decision{Why: base.Why}
	}
	if files == nil {
		return Decision{Why: "could not list the files changed since " + base.SHA}
	}
	if len(files) == 0 {
		return Decision{Why: "no file changed since " + base.SHA}
	}
	for _, f := range files {
		if !IsDoc(f) {
			return Decision{Why: f + " changed, and it is not documentation"}
		}
	}
	return Decision{Docs: true, Why: fmt.Sprintf("all %d files changed since %s are documentation", len(files), base.SHA)}
}

// FilesSince lists the files changed from sha to HEAD in the checkout at
// dir, or nil when the checkout lacks sha or git fails. --no-renames
// lists both sides of a move, so a script moved into docs/ is still a
// change to code.
func FilesSince(sha, dir string) []string {
	cat := exec.Command("git", "cat-file", "-e", sha+"^{commit}")
	cat.Dir = dir
	if cat.Run() != nil {
		return nil
	}
	diff := exec.Command("git", "diff", "--name-only", "--no-renames", "-z", sha, "HEAD")
	diff.Dir = dir
	out, err := diff.Output()
	if err != nil {
		return nil
	}
	files := []string{}
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files
}
