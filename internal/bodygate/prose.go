package bodygate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/iantanwx/loop/tools/prose"
)

// steRefuse is the ste-lint.py rules an author can always satisfy, as in
// alpha's STE_REFUSE (body-gate.ts). A semicolon splits into two
// sentences, a phrasal verb becomes one word, a marketing adjective
// deletes, and a nominalization becomes its verb. Every other ste-lint
// rule needs judgement, and alpha only warns on it. Every unslop-lint.py
// rule refuses, because each names the plain word that replaces the one it
// refuses.
var steRefuse = map[string]bool{"semicolon": true, "phrasal-verb": true, "marketing-adjective": true, "nominalization": true}

// unslopLine is one line unslop-lint.py prints: path:line rule: message.
var unslopLine = regexp.MustCompile(`^.*?:(\d+)\s+([\w-]+):\s+(.+)$`)

// ProseFaults runs the two prose linters the release embeds (tools/prose)
// over one body, and returns each finding that refuses, in line order.
// alpha runs its checkers over the body rather than port their rules,
// because one rule in two languages drifts. `loop gates` runs the same
// two files, so a host that passes the gates has python3.
//
// A linter that cannot run is an error, never a clean body.
func ProseFaults(ctx context.Context, body string) ([]Fault, error) {
	py, err := exec.LookPath("python3")
	if err != nil {
		return nil, errors.New("bodygate: python3 is not on PATH, so the prose rules cannot run; loop gates needs it too")
	}
	dir, err := os.MkdirTemp("", "loop-body-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ste, unslop, md := filepath.Join(dir, "ste-lint.py"), filepath.Join(dir, "unslop-lint.py"), filepath.Join(dir, "body.md")
	for path, data := range map[string][]byte{ste: prose.STELint, unslop: prose.UnslopLint, md: []byte(body)} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, err
		}
	}
	steOut, err := lint(ctx, py, ste, "--json", md)
	if err != nil {
		return nil, err
	}
	unslopOut, err := lint(ctx, py, unslop, md)
	if err != nil {
		return nil, err
	}
	var report struct {
		Violations *[]struct {
			Line    int    `json:"line"`
			Rule    string `json:"rule"`
			Match   string `json:"match"`
			Message string `json:"message"`
		} `json:"violations"`
	}
	if err := json.Unmarshal(steOut, &report); err != nil || report.Violations == nil {
		// A renamed field must not read as a clean body.
		return nil, fmt.Errorf("bodygate: ste-lint.py printed no violations array: %v", err)
	}
	var faults []Fault
	for _, v := range *report.Violations {
		if steRefuse[v.Rule] {
			faults = append(faults, Fault{Line: v.Line, Rule: v.Rule, Text: fmt.Sprintf("%s [%s]", v.Message, v.Match)})
		}
	}
	for _, raw := range strings.Split(string(unslopOut), "\n") {
		if m := unslopLine.FindStringSubmatch(raw); m != nil {
			var n int
			_, _ = fmt.Sscan(m[1], &n)
			faults = append(faults, Fault{Line: n, Rule: m[2], Text: m[3]})
		}
	}
	sort.SliceStable(faults, func(i, j int) bool { return faults[i].Line < faults[j].Line })
	return faults, nil
}

// lint runs one linter and returns its standard output. Both linters exit
// 1 when they find something and 0 when they do not. Any other end is the
// linter failing, and its findings would be short, not empty.
func lint(ctx context.Context, py, script string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, py, append([]string{"-I", script}, args...)...)
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, fmt.Errorf("bodygate: %s failed, so the body was not checked: %v: %s", filepath.Base(script), err, stderr.String())
	}
	return out, nil
}

// proseRefusal returns why a body's prose must not be written, or "".
func proseRefusal(faults []Fault) string {
	if len(faults) == 0 {
		return ""
	}
	lines := []string{"This body breaks a prose rule that an author can always satisfy.", ""}
	for _, f := range faults {
		lines = append(lines, fmt.Sprintf("  line %d  %s: %s", f.Line, f.Rule, f.Text))
	}
	return strings.Join(append(lines, "",
		"  Put a quotation that must hold the word in a code span or a fenced block. Both linters skip both."), "\n")
}
