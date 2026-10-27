package pack_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

// requiredFlags holds the C25 rules of spec factorio-9wh: each command
// whose flag the epic makes required, and the flags of which each such
// command in the pack names one. Each spec entry that adds a required flag
// adds its rule.
var requiredFlags = []struct {
	command string
	anyOf   []string
}{
	{"loop inbox defer", []string{"--reason"}},
}

// commandsIn returns each command in text that starts with command, up to
// the end of its code span or its line. A line that ends in a backslash
// continues the command.
func commandsIn(text, command string) []string {
	var out []string
	for rest := text; ; {
		i := strings.Index(rest, command+" ")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		end := len(rest)
		for j := 0; j < len(rest); j++ {
			if rest[j] == '`' || rest[j] == '\n' && (j == 0 || rest[j-1] != '\\') {
				end = j
				break
			}
		}
		out = append(out, rest[:end])
		rest = rest[end:]
	}
}

// C25: every command in the pack that this epic changed names the flag
// that the epic makes required.
func TestC25ThePackNamesEachRequiredFlag(t *testing.T) {
	err := fs.WalkDir(pack.Source(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".md") {
			return err
		}
		text := read(t, name)
		for _, rule := range requiredFlags {
			for _, c := range commandsIn(text, rule.command) {
				if !namesOne(c, rule.anyOf) {
					t.Errorf("%s: %q names none of %v", name, c, rule.anyOf)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func namesOne(command string, flags []string) bool {
	for _, word := range strings.Fields(command) {
		for _, f := range flags {
			if word == f || strings.HasPrefix(word, f+"=") {
				return true
			}
		}
	}
	return false
}
