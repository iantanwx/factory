package pack_test

import (
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

// requiredFlags holds one rule for each verb that epic factorio-9wh gives a
// required flag: every command of the verb in the pack names one of the
// flags. Each work entry that adds a required flag adds its rule (spec C25).
var requiredFlags = []struct {
	verb  string
	flags []string
}{
	{"loop decision file", []string{"--by", "--waits-for-a-person"}},
}

// C25: the pack names each flag that this epic makes required, so a run
// that copies a pack command is never refused for a missing flag.
func TestPackCommandsNameTheirRequiredFlags(t *testing.T) {
	for _, r := range requiredFlags {
		n := 0
		err := fs.WalkDir(pack.Source(), ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
				return err
			}
			for _, cmd := range commands(read(t, path), r.verb) {
				n++
				if !slices.ContainsFunc(r.flags, func(f string) bool { return namesFlag(cmd, f) }) {
					t.Errorf("%s: %q names none of %v", path, cmd, r.flags)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			t.Errorf("no pack file runs %s", r.verb)
		}
	}
}

// commands returns each command of verb in text: the verb and its flags, up
// to the end of the code span or the line. A line that ends in a backslash
// goes on. A mention of the verb with no flag after it is prose.
func commands(text, verb string) []string {
	var cmds []string
	for rest := text; ; {
		i := strings.Index(rest, verb)
		if i < 0 {
			return cmds
		}
		rest = rest[i+len(verb):]
		var cmd strings.Builder
		for line := rest; ; {
			l, next, _ := strings.Cut(line, "\n")
			l, _, quoted := strings.Cut(l, "`")
			cont, ok := strings.CutSuffix(strings.TrimRight(l, " "), `\`)
			if quoted || !ok {
				cmd.WriteString(l)
				break
			}
			cmd.WriteString(cont + " ")
			line = next
		}
		if strings.HasPrefix(strings.TrimSpace(cmd.String()), "-") {
			cmds = append(cmds, verb+cmd.String())
		}
	}
}

// namesFlag reports whether cmd passes flag, as --flag value or --flag=value.
func namesFlag(cmd, flag string) bool {
	return slices.ContainsFunc(strings.Fields(cmd), func(f string) bool { return f == flag || strings.HasPrefix(f, flag+"=") })
}
