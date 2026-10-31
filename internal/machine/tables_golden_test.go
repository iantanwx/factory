package machine

import (
	"encoding/json"
	"os"
	"reflect"
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

// C12, work entry 3: the pair amender to reviser moves each golden row of
// the amender to the reviser, and the build's row is that row.
func TestC12TheReviserTakesTheAmenderRows(t *testing.T) {
	raw, err := os.ReadFile(goldenTables)
	if err != nil {
		t.Fatal(err)
	}
	var golden map[string]struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	s, err := cli.Surface().Schema()
	if err != nil {
		t.Fatal(err)
	}
	var build struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(s.TransitionTables["epic"], &build); err != nil {
		t.Fatal(err)
	}
	var from []any
	for i, row := range golden["epic"].Rows {
		if row["stage"] != "amender" {
			continue
		}
		want := renameStages(row, config.NewStageName)
		got := renameStages(build.Rows[i], func(string) (string, bool) { return "", false })
		if !reflect.DeepEqual(got, want) || build.Rows[i]["stage"] != "reviser" {
			t.Errorf("epic row %d: got %v, want %v under stage reviser", i, got, want)
		}
		from = append(from, row["from"].([]any)...)
	}
	if !reflect.DeepEqual(from, []any{"prd_amending", "spec_amending"}) {
		t.Fatalf("the golden amender rows start from %v, want prd_amending and spec_amending", from)
	}
}
