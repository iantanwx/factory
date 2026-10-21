package mergify

import (
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func fixture() Template {
	return Template{Bot: "loop-fixture[bot]", Base: "main", Checks: []string{"ci", "lint"}}
}

func render(t *testing.T, tp Template) map[string]any {
	t.Helper()
	raw, err := Render(tp)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the rendered file is not YAML: %v\n%s", err, raw)
	}
	return doc
}

func list(t *testing.T, v any, path ...string) []any {
	t.Helper()
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v is not a map at %q", v, k)
		}
		v = m[k]
	}
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("%v at %v is not a list", v, path)
	}
	return l
}

func strs(l []any) []string {
	var out []string
	for _, v := range l {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// The loop App's queue command passes only for the base branch, the queue
// label and loop/eligible (U11).
func TestTemplateLetsTheAppQueueOnlyForTheBaseTheLabelAndEligible(t *testing.T) {
	doc := render(t, fixture())
	conds := list(t, doc, "commands_restrictions", "queue", "conditions")
	if len(conds) != 1 {
		t.Fatalf("queue restriction conditions = %v; want one or-block", conds)
	}
	or := list(t, conds[0], "or")
	var and []string
	var others []string
	for _, c := range or {
		if m, ok := c.(map[string]any); ok {
			if and != nil {
				t.Fatalf("two and-blocks in %v", or)
			}
			and = strs(list(t, m, "and"))
			continue
		}
		others = append(others, c.(string))
	}
	want := []string{"sender = loop-fixture[bot]", "base = main", "label = " + QueueLabel, "check-success = loop/eligible"}
	if !slices.Equal(and, want) {
		t.Fatalf("the App's exception = %v; want exactly %v", and, want)
	}
	if !slices.Equal(others, []string{"sender-permission >= write"}) {
		t.Fatalf("the other senders = %v", others)
	}
	for _, o := range others {
		if strings.Contains(o, "[bot]") {
			t.Fatalf("the bot passes outside its exception: %q", o)
		}
	}
}

// Every rule that queues needs the label and the status, the queue merges
// by merge commit in batches, and the merge conditions name the required
// checks.
func TestTemplateQueuesOnTheLabelAndTheStatusWithMergeCommits(t *testing.T) {
	doc := render(t, fixture())
	rules := list(t, doc, "queue_rules")
	if len(rules) != 1 {
		t.Fatalf("queue rules = %d; want 1", len(rules))
	}
	rule := rules[0].(map[string]any)
	if rule["merge_method"] != "merge" {
		t.Fatalf("merge_method = %v", rule["merge_method"])
	}
	if b, _ := rule["batch_size"].(map[string]any); b == nil || b["max"].(int) < 2 {
		t.Fatalf("batch_size = %v; want batches", rule["batch_size"])
	}
	q := strs(list(t, rule, "queue_conditions"))
	for _, c := range []string{"base = main", "-draft", "-conflict", "label = " + QueueLabel, "check-success = loop/eligible",
		"check-success = ci", "check-success = lint"} {
		if !slices.Contains(q, c) {
			t.Fatalf("queue_conditions %v lack %q", q, c)
		}
	}
	if m := strs(list(t, rule, "merge_conditions")); !slices.Equal(m, []string{"check-success = ci", "check-success = lint"}) {
		t.Fatalf("merge_conditions = %v", m)
	}
	for _, r := range list(t, doc, "pull_request_rules") {
		c := strs(list(t, r, "conditions"))
		if !slices.Contains(c, "label = "+QueueLabel) || !slices.Contains(c, "check-success = loop/eligible") {
			t.Fatalf("a queueing rule lacks the label or the status: %v", c)
		}
	}
	if doc["merge_queue"].(map[string]any)["dequeued_label"] != DequeuedLabel {
		t.Fatalf("dequeued_label = %v", doc["merge_queue"])
	}
}

func TestTemplateRefusesUnsafeInput(t *testing.T) {
	for name, tp := range map[string]Template{
		"no bot":        {Base: "main"},
		"not a bot":     {Bot: "mallory", Base: "main"},
		"newline check": {Bot: "loop-fixture[bot]", Base: "main", Checks: []string{"ci\nqueue_rules: []"}},
		"bad base":      {Bot: "loop-fixture[bot]", Base: "main\n"},
	} {
		if _, err := Render(tp); err == nil {
			t.Errorf("%s: Render accepted %+v", name, tp)
		}
	}
	// A check name that YAML would read as syntax stays one string.
	tp := fixture()
	tp.Checks = []string{`ci: "x" # y`}
	doc := render(t, tp)
	q := strs(list(t, doc["queue_rules"].([]any)[0], "queue_conditions"))
	if !slices.Contains(q, `check-success = ci: "x" # y`) {
		t.Fatalf("queue_conditions = %v", q)
	}
}
