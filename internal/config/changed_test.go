package config

import (
	"errors"
	"strings"
	"testing"
)

const changedBase = `# the project's config
merge:
  mechanism: github
stages:
  implementer:
    model: claude-opus-5-5
gates:
  - name: lint
    run: make lint
`

func TestSettingsChangedComparesStagesPoolsAndMerge(t *testing.T) {
	cases := map[string]struct {
		new  string
		want bool
	}{
		"same bytes": {changedBase, false},
		"comments and layout": {"merge: {mechanism: github}\nstages: {implementer: {model: claude-opus-5-5}}\n" +
			"gates: [{name: lint, run: make lint}]\n", false},
		"gates":           {"merge:\n  mechanism: github\nstages:\n  implementer:\n    model: claude-opus-5-5\n", false},
		"setup":           {changedBase + "setup: make deps\n", false},
		"a stage cap":     {"merge:\n  mechanism: github\nstages:\n  implementer:\n    model: claude-opus-5-5\n    cap: 3\n", true},
		"a new pool":      {changedBase + "pools:\n  reviewers:\n    cap: 2\n", true},
		"merge mechanism": {"merge:\n  mechanism: mergify\nstages:\n  implementer:\n    model: claude-opus-5-5\n", true},
		"stages dropped":  {"merge:\n  mechanism: github\n", true},
		"empty file":      {"", true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := SettingsChanged([]byte(changedBase), []byte(c.new))
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("SettingsChanged = %v, want %v", got, c.want)
			}
		})
	}
}

// The error names no value of the file, because a misplaced value can be a
// secret.
func TestSettingsChangedRefusesAFileThatDoesNotParse(t *testing.T) {
	for name, raw := range map[string]string{"unclosed": "stages: [\n", "a list": "- secret-ish\n", "a scalar": "hunter22\n"} {
		t.Run(name, func(t *testing.T) {
			_, err := SettingsChanged([]byte(changedBase), []byte(raw))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			if _, err := SettingsChanged([]byte(raw), []byte(changedBase)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("old side: err = %v, want ErrInvalid", err)
			}
			if strings.Contains(err.Error(), "hunter22") {
				t.Fatalf("err %q holds a value of the file", err)
			}
		})
	}
}
