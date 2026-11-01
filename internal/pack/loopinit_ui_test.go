package pack_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

var (
	writeVerb = regexp.MustCompile(`(?i)\bwrit(e|es|ing)\b`)
	writeNo   = regexp.MustCompile(`(?i)\bwrit(e|es)\s+no\b`)
	yamlUIKey = regexp.MustCompile(`^\s*ui:`)
)

// uiSteps is each step of the ui proposal, in the order loop-init must
// take them. The last step writes the block.
var uiSteps = []struct {
	name string
	is   func(sentence string) bool
}{
	{"runs `loop ui detect`", func(s string) bool {
		return strings.Contains(s, "`loop ui detect`") && strings.Contains(strings.ToLower(s), "run")
	}},
	{"on NOTHING writes no `ui` block and asks nothing", func(s string) bool {
		return strings.Contains(s, "`NOTHING`") && strings.Contains(s, "ask nothing") && strings.Contains(s, "no `ui` block")
	}},
	{"shows the operator the proposal and its cost", func(s string) bool {
		l := strings.ToLower(s)
		return strings.Contains(l, "show the operator") && strings.Contains(l, "cost")
	}},
	{"recommends one answer", func(s string) bool { return strings.Contains(strings.ToLower(s), "recommend") }},
	{"asks the operator and waits", func(s string) bool { return strings.Contains(strings.ToLower(s), "wait for the answer") }},
	{"writes the block", writesUI},
}

// writesUI reports a unit that writes the ui block: a sentence, or a ui
// key in a fenced config. A sentence that writes no block writes nothing.
func writesUI(u string) bool {
	return yamlUIKey.MatchString(u) || writeVerb.MatchString(u) && !writeNo.MatchString(u) &&
		(strings.Contains(u, "`ui`") || strings.Contains(u, "block"))
}

// uiUnits returns § 5 as one unit for each prose sentence and one for
// each fenced line, in the order of the text.
func uiUnits(sec string) []string {
	var units, prose []string
	flush := func() {
		for _, s := range sentenceEnd.Split(strings.Join(strings.Fields(strings.Join(prose, " ")), " "), -1) {
			if s != "" {
				units = append(units, s)
			}
		}
		prose = nil
	}
	in := false
	for _, l := range strings.Split(sec, "\n") {
		switch {
		case strings.HasPrefix(strings.TrimSpace(l), "```"):
			flush()
			in = !in
		case in:
			units = append(units, l)
		default:
			prose = append(prose, l)
		}
	}
	flush()
	return units
}

// checkUIProposal returns why § 5 of a loop-init text does not propose
// the ui block before it writes it, or nil.
func checkUIProposal(sec string) error {
	units := uiUnits(sec)
	at := -1
	for _, step := range uiSteps {
		found := -1
		for i := at + 1; i < len(units); i++ {
			if step.is(units[i]) {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf("§ 5 has no step that %s after the step before it", step.name)
		}
		at = found
		if step.name == "asks the operator and waits" {
			for i := 0; i < at; i++ {
				if writesUI(units[i]) {
					return fmt.Errorf("§ 5 writes the ui block before the operator's answer: %s", units[i])
				}
			}
		}
	}
	return nil
}

func loopInitConfigSection(text string) string {
	return pack.Section(text, 2, func(title string) bool { return strings.HasPrefix(title, "5. ") })
}

// C25: loop-init § 5 runs `loop ui detect`, asks nothing on a NOTHING
// exit, shows the proposal with its cost and one recommendation, waits
// for the operator, and only then writes the block. The same check
// refuses a text that writes the block first, in prose or in the config
// template.
func TestC25LoopInitProposesTheUIBlockAndWritesItOnlyOnAYes(t *testing.T) {
	sec := loopInitConfigSection(read(t, "skills/loop-init/SKILL.md"))
	if sec == "" {
		t.Fatal("loop-init has no § 5")
	}
	if err := checkUIProposal(sec); err != nil {
		t.Fatal(err)
	}
	template := strings.Replace(sec, "merge:\n  mechanism: direct\n", "merge:\n  mechanism: direct\nui:\n  kind: web\n", 1)
	if template == sec {
		t.Fatal("§ 5 has no config template that starts with the merge block")
	}
	for name, text := range map[string]string{
		"a prose write first": "Write the proposed `ui` block into `.loop/config.yaml`.\n\n" + sec,
		"a template with ui":  template,
		"no NOTHING rule":     strings.ReplaceAll(sec, "`NOTHING`", "nothing"),
		"no question":         strings.ReplaceAll(sec, "wait for the answer", "go on"),
	} {
		if checkUIProposal(text) == nil {
			t.Errorf("the check passes a § 5 with %s", name)
		}
	}
}
