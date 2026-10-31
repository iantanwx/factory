package config

import (
	"errors"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func strp(s string) *string { return &s }

// editFile runs SetSetting on raw with the key that s names.
func editFile(t *testing.T, raw, s string, value *string) string {
	t.Helper()
	key, err := ParseSettingKey(s, codeKnown())
	if err != nil {
		t.Fatal(err)
	}
	out, err := SetSetting([]byte(raw), key, value)
	if err != nil {
		t.Fatalf("SetSetting(%s): %v", s, err)
	}
	return string(out)
}

func decodeProject(t *testing.T, raw string) ProjectConfig {
	t.Helper()
	var pc ProjectConfig
	if err := yaml.Unmarshal([]byte(raw), &pc); err != nil {
		t.Fatalf("the edited file does not decode: %v\n%s", err, raw)
	}
	return pc
}

// C33: --unset of the last field leaves the stage's key, so the stage
// stays on.
func TestC33UnsetOfTheLastFieldLeavesTheStageKey(t *testing.T) {
	for name, raw := range map[string]string{
		"flow":  "stages:\n  reproducer: {cap: 1}\n",
		"block": "stages:\n  reproducer:\n    cap: 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			got := editFile(t, raw, "stages.reproducer.cap", nil)
			if want := "stages:\n  reproducer: {}\n"; got != want {
				t.Fatalf("got\n%s\nwant\n%s", got, want)
			}
			if pc := decodeProject(t, got); !pc.StageEnabled("reproducer") {
				t.Fatal("StageEnabled(reproducer) = false after --unset of cap")
			}
		})
	}
}

// C33: --unset of enabled removes enabled: false, so the stage turns on.
// That move is the change the key names.
func TestC33UnsetOfEnabledTurnsTheStageOn(t *testing.T) {
	got := editFile(t, "stages:\n  reproducer: {enabled: false, cap: 1}\n", "stages.reproducer.enabled", nil)
	if want := "stages:\n  reproducer: {cap: 1}\n"; got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if pc := decodeProject(t, got); !pc.StageEnabled("reproducer") {
		t.Fatal("StageEnabled(reproducer) = false after --unset of enabled")
	}
}

// C33: no --unset of a field other than enabled changes StageEnabled of
// any stage, or the names under stages.
func TestC33UnsetOfAnyOtherFieldKeepsEveryStage(t *testing.T) {
	const base = `stages:
  implementer:
    model: claude-opus-5-5
    harness: claude
    effort: high
    run_budget: 1h
    schedule: "*/5 * * * *"
    cap: 2
    runs_per_tick: 1
    escalated: {model: claude-opus-5-5, harness: claude, effort: max}
  triage: {enabled: false, cap: 1, model: claude-opus-5-5}
  reproducer:
    cap: 1
  brainstorm: {cap: 1}
  merge-handoff: {schedule: "*/2 * * * *"}
`
	before := decodeProject(t, base)
	names := slices.Concat(RunStages(), ProjectJobs())
	keys := []string{"stages.triage.cap", "stages.triage.model", "stages.reproducer.cap",
		"stages.brainstorm.cap", "stages.merge-handoff.schedule"}
	for _, f := range stageFields[KindItemStage] {
		if f != "enabled" {
			keys = append(keys, "stages.implementer."+f)
		}
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			got := editFile(t, base, key, nil)
			if got == base {
				t.Fatal("the file still holds the field")
			}
			after := decodeProject(t, got)
			for _, n := range names {
				if before.StageEnabled(n) != after.StageEnabled(n) {
					t.Errorf("StageEnabled(%s) = %v, want %v", n, after.StageEnabled(n), before.StageEnabled(n))
				}
			}
			if a, b := slices.Sorted(maps.Keys(before.Stages)), slices.Sorted(maps.Keys(after.Stages)); !slices.Equal(a, b) {
				t.Errorf("stage names = %v, want %v", b, a)
			}
		})
	}
}

func TestSetSettingWritesTheOneKey(t *testing.T) {
	cases := map[string]struct{ raw, key, value, want string }{
		"a field of a flow entry": {"stages:\n  implementer: {}\n", "stages.implementer.cap", "2",
			"stages:\n  implementer: {cap: 2}\n"},
		"a value with its comment": {"stages:\n  implementer:\n    cap: 1 # why\n    model: m\n", "stages.implementer.cap", "3",
			"stages:\n  implementer:\n    cap: 3 # why\n    model: m\n"},
		"a new stage entry": {"stages:\n  implementer: {}\n", "stages.reproducer.enabled", "true",
			"stages:\n  implementer: {}\n  reproducer:\n    enabled: true\n"},
		"a new section": {"merge:\n  mechanism: direct\n", "pools.reviewers.cap", "2",
			"merge:\n  mechanism: direct\npools:\n  reviewers:\n    cap: 2\n"},
		"an escalated field": {"stages:\n  implementer: {}\n", "stages.implementer.escalated.model", "claude-opus-5-5",
			"stages:\n  implementer: {escalated: {model: claude-opus-5-5}}\n"},
		"the required checks": {"merge:\n  mechanism: direct\n", "merge.required_checks", "ci,audit",
			"merge:\n  mechanism: direct\n  required_checks:\n    - ci\n    - audit\n"},
		"a cron": {"stages:\n  merge-handoff: {}\n", "stages.merge-handoff.schedule", "*/5 * * * *",
			"stages:\n  merge-handoff: {schedule: '*/5 * * * *'}\n"},
		"a stage with no value": {"stages:\n  implementer:\n", "stages.implementer.cap", "2",
			"stages:\n  implementer:\n    cap: 2\n"},
		"an empty file": {"", "stages.implementer.cap", "2", "stages:\n  implementer:\n    cap: 2\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := editFile(t, c.raw, c.key, strp(c.value)); got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

// The edit writes the value that --value names, so the parsed config reads
// it back.
func TestSetSettingValuesDecode(t *testing.T) {
	pc := decodeProject(t, editFile(t, "stages:\n  merge-handoff: {}\n", "stages.merge-handoff.schedule", strp("*/5 * * * *")))
	if got := pc.Stages["merge-handoff"].Schedule; got != "*/5 * * * *" {
		t.Fatalf("schedule = %q", got)
	}
	pc = decodeProject(t, editFile(t, "", "merge.required_checks", strp("ci,audit")))
	if got := pc.Merge.RequiredChecks; !slices.Equal(got, []string{"ci", "audit"}) {
		t.Fatalf("required_checks = %q", got)
	}
}

// An --unset of a field the file does not hold changes no byte.
func TestSetSettingUnsetOfAnAbsentFieldKeepsTheFile(t *testing.T) {
	const raw = "merge:\n  mechanism: direct   # wide\nstages:\n  implementer: {}\n"
	for _, key := range []string{"stages.implementer.cap", "stages.triage.cap", "pools.reviewers.cap", "stages.implementer.escalated.model"} {
		if got := editFile(t, raw, key, nil); got != raw {
			t.Fatalf("%s: got\n%s\nwant\n%s", key, got, raw)
		}
	}
}

// The comments of a removed field stay in the file, next to where it was.
func TestSetSettingUnsetKeepsTheFieldsComments(t *testing.T) {
	cases := map[string]struct{ raw, key, want string }{
		"before the next field": {
			"stages:\n  implementer:\n    # head\n    cap: 1 # line\n    model: m\n",
			"stages.implementer.cap",
			"stages:\n  implementer:\n    # head\n    # line\n    model: m\n"},
		"the last field": {
			"stages:\n  reproducer:\n    # head\n    cap: 1 # line\n  triage: {}\n",
			"stages.reproducer.cap",
			"stages:\n  reproducer: {}\n  # head\n  # line\n\n  triage: {}\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := editFile(t, c.raw, c.key, nil); got != c.want {
				t.Fatalf("got\n%s\nwant\n%s", got, c.want)
			}
		})
	}
}

func TestSetSettingRefusesAFileItCannotEdit(t *testing.T) {
	key, err := ParseSettingKey("stages.implementer.cap", codeKnown())
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"not YAML":         "stages: [\n",
		"stages as a list": "stages: [implementer]\n",
		"a scalar stage":   "stages:\n  implementer: 3\n",
		"a list file":      "- stages\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SetSetting([]byte(raw), key, strp("2")); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

// Spec Assumption 3: a node edit keeps every comment and every other line
// of this repository's config and of the loop-init template.
func TestSetSettingRoundTripKeepsTheShippedConfigs(t *testing.T) {
	repo, err := os.ReadFile("../../.loop/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := os.ReadFile("../../pack/skills/loop-init/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	_, tmpl, ok := strings.Cut(string(skill), "Write `.loop/config.yaml`:\n\n```yaml\n")
	tmpl, _, ok2 := strings.Cut(tmpl, "```\n")
	if !ok || !ok2 {
		t.Fatal("the loop-init skill holds no .loop/config.yaml template")
	}
	for name, raw := range map[string]string{".loop/config.yaml": string(repo), "loop-init template": tmpl} {
		t.Run(name, func(t *testing.T) {
			set := roundTrip(t, raw)
			// The file that a settings pull request writes holds the same
			// proof, so the test stays green after that edit.
			roundTrip(t, set)
		})
	}
}

// roundTrip sets a field that raw does not hold, and checks that every
// comment stays in order and that the unset gives raw back. It returns the
// file after the set.
func roundTrip(t *testing.T, raw string) string {
	t.Helper()
	key := absentStageField(t, raw)
	set := editFile(t, raw, key, strp("2"))
	if set == raw {
		t.Fatalf("set %s: the file did not change", key)
	}
	if a, b := comments(raw), comments(set); !slices.Equal(a, b) {
		t.Fatalf("set %s: comments\n%q\nwant\n%q", key, b, a)
	}
	if got := editFile(t, set, key, nil); got != raw {
		t.Fatalf("unset %s: got\n%s\nwant\n%s", key, got, raw)
	}
	return set
}

// absentStageField returns the key of the first field, in file order,
// that a mapping entry under stages does not hold.
func absentStageField(t *testing.T, raw string) string {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil || doc.Kind == 0 {
		t.Fatalf("the file does not decode: %v", err)
	}
	if j := keyIndex(doc.Content[0], "stages"); j >= 0 {
		stages := doc.Content[0].Content[j+1]
		for i := 0; i < len(stages.Content); i += 2 {
			name, entry := stages.Content[i].Value, stages.Content[i+1]
			kind, _ := KindOf(name, codeKnown())
			for _, f := range stageFields[kind] {
				if entry.Kind == yaml.MappingNode && !strings.Contains(f, ".") && keyIndex(entry, f) < 0 {
					return "stages." + name + "." + f
				}
			}
		}
	}
	t.Fatal("no mapping entry under stages lacks a field")
	return ""
}

// comments returns the text of each comment in s, in order.
func comments(s string) []string {
	var cs []string
	for line := range strings.Lines(s) {
		if i := strings.Index(line, "#"); i == 0 || i > 0 && line[i-1] == ' ' {
			cs = append(cs, strings.TrimSpace(line[i:]))
		}
	}
	return cs
}
