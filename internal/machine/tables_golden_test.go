package machine

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// goldenTables is loop schema's transition_tables before the vocabulary
// epic renamed a stage (docs/plans/factorio-vlle-loop-vocabulary-spec.md,
// C12). A rename never changes it: the test maps it through the rename
// table instead.
const goldenTables = "testdata/tables.golden.json"

// renameStages returns v with each string value that rename knows replaced
// by its new name. It drops each says field, the prose a refusal prints,
// since a rename may reword it. Object keys stay as they are.
func renameStages(v any, rename func(string) (string, bool)) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if k != "says" {
				out[k] = renameStages(e, rename)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = renameStages(e, rename)
		}
		return out
	case string:
		if n, ok := rename(x); ok {
			return n
		}
	}
	return v
}

// C12: the transition tables of the build are the golden tables with each
// stage name mapped through the rename table. The same kinds, states, rows,
// verbs, sources and targets, and no row added or removed.
func TestC12TransitionTablesChangeNamesOnly(t *testing.T) {
	raw, err := os.ReadFile(goldenTables)
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]any
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	s, err := cli.Surface().Schema()
	if err != nil {
		t.Fatal(err)
	}
	build := map[string]any{}
	for k, b := range s.TransitionTables {
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		build[k] = v
	}
	want := renameStages(golden, config.NewStageName).(map[string]any)
	got := renameStages(build, func(string) (string, bool) { return "", false }).(map[string]any)
	for kind := range want {
		if _, ok := got[kind]; !ok {
			t.Errorf("the build has no %s table", kind)
		}
	}
	for kind, table := range got {
		if !reflect.DeepEqual(table, want[kind]) {
			w, _ := json.MarshalIndent(want[kind], "", "  ")
			g, _ := json.MarshalIndent(table, "", "  ")
			t.Errorf("the %s table changed more than names:\nwant (golden, renamed):\n%s\ngot:\n%s", kind, w, g)
		}
	}
}

// renameStages maps a stage name in any field, keeps every other value,
// and drops the prose.
func TestRenameStagesMapsNamesOnly(t *testing.T) {
	in := map[string]any{"rows": []any{map[string]any{
		"verb": "claim", "stage": "fixer", "from": []any{"open"}, "says": "Ask the fixer.",
	}}}
	got := renameStages(in, func(s string) (string, bool) { return "triage", s == "fixer" })
	want := map[string]any{"rows": []any{map[string]any{"verb": "claim", "stage": "triage", "from": []any{"open"}}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("renameStages = %v, want %v", got, want)
	}
}

// C12: the pair prd-author to unattended-brainstorm is in the table, and
// the epic table's one prd-author row of the golden file now claims under
// unattended-brainstorm. The build names prd-author in no table.
func TestC12PRDAuthorRowNowNamesUnattendedBrainstorm(t *testing.T) {
	if n, ok := config.NewStageName("prd-author"); !ok || n != "unattended-brainstorm" {
		t.Fatalf("NewStageName(prd-author) = %q, %v, want unattended-brainstorm", n, ok)
	}
	raw, err := os.ReadFile(goldenTables)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), `"stage": "prd-author"`); n != 1 {
		t.Fatalf("the golden tables name prd-author %d times, want 1", n)
	}
	s, err := cli.Surface().Schema()
	if err != nil {
		t.Fatal(err)
	}
	epic := string(s.TransitionTables[string(KindEpic)])
	if strings.Contains(epic, `"prd-author"`) || strings.Count(epic, `"unattended-brainstorm"`) != 1 {
		t.Fatalf("the epic table names prd-author, or unattended-brainstorm other than once: %s", epic)
	}
}
