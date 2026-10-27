package pack_test

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

// requiredFlags holds one C25 rule for each loop command that epic
// factorio-9wh makes take a flag: every such command in the pack names one
// of flags.
var requiredFlags = []struct {
	command string
	flags   []string
}{
	{"loop work cancel", []string{"--reason"}},
}

// codeSpan is one inline code span of a markdown line.
var codeSpan = regexp.MustCompile("`[^`]+`")

// command is one command of the pack and the file and line it starts at.
type command struct{ at, text string }

// packCommands returns each command of the pack: each inline code span,
// and each line of a fenced block with its continuation lines joined.
func packCommands(t *testing.T) []command {
	t.Helper()
	var out []command
	err := fs.WalkDir(pack.Source(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return err
		}
		raw, err := fs.ReadFile(pack.Source(), path)
		if err != nil {
			return err
		}
		fenced, held, start := false, "", 0
		for i, l := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "```") {
				fenced = !fenced
				continue
			}
			if !fenced {
				for _, s := range codeSpan.FindAllString(l, -1) {
					out = append(out, command{fmt.Sprintf("%s:%d", path, i+1), s})
				}
				continue
			}
			if held == "" {
				start = i + 1
			}
			held += l
			if !strings.HasSuffix(l, `\`) {
				out = append(out, command{fmt.Sprintf("%s:%d", path, start), held})
				held = ""
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestC25PackNamesEachRequiredFlag proves C25 of factorio-9wh: each command
// of the pack that a rule names carries that rule's flag, so a run that
// copies it is not refused with USAGE.
func TestC25PackNamesEachRequiredFlag(t *testing.T) {
	cmds := packCommands(t)
	for _, rule := range requiredFlags {
		found := 0
		for _, c := range cmds {
			if !strings.Contains(c.text, rule.command) {
				continue
			}
			found++
			if !slices.ContainsFunc(rule.flags, func(f string) bool { return strings.Contains(c.text, f) }) {
				t.Errorf("%s: %s names none of %v", c.at, c.text, rule.flags)
			}
		}
		if found == 0 {
			t.Errorf("the pack holds no %s command, so the rule reads nothing", rule.command)
		}
	}
}
