package gates

import (
	"context"
	"slices"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

func TestSelect(t *testing.T) {
	names := func(gs []Gate) []string {
		var out []string
		for _, g := range gs {
			out = append(out, g.Name)
		}
		return out
	}
	all, _ := Select(nil, false, false, "")
	fast, _ := Select(nil, false, true, "")
	docs, _ := Select(nil, true, false, "")
	if slices.Contains(names(fast), "test") || slices.Contains(names(fast), "build") || slices.Contains(names(fast), "pixel") || len(fast) != len(all)-3 {
		t.Fatalf("fast = %v", names(fast))
	}
	if slices.Contains(names(docs), "lint") || !slices.Contains(names(docs), "prose") {
		t.Fatalf("docs = %v", names(docs))
	}
	if one, err := Select(nil, false, false, "lint"); err != nil || len(one) != 1 {
		t.Fatalf("only lint = %v %v", one, err)
	}
	if _, err := Select(nil, false, false, "nope"); err == nil {
		t.Fatal("an unknown gate was selected")
	}
	// A gate that the config names beyond the stack gates runs before test,
	// and --fast keeps it.
	cfg := &config.ProjectConfig{Gates: []config.Command{{Name: "e2e", Run: "true"}, {Name: "test", Run: "true"}}}
	got, _ := Select(cfg, false, true, "")
	if i := slices.Index(names(got), "e2e"); i < 0 || names(got)[i-1] != "typecheck" {
		t.Fatalf("fast with e2e = %v", names(got))
	}
}

func TestStackGateRunsTheConfigCommand(t *testing.T) {
	r := testRepo(t, map[string]string{".loop/config.yaml": "merge:\n  mechanism: direct\ngates:\n  - name: test\n    run: exit 3\n  - name: audit\n    run: exit 1\n"})
	ctx := context.Background()
	if fs, err := stackGate("test")(ctx, r); err != nil || !slices.Equal(rules(fs), []string{"command_failed"}) {
		t.Fatalf("test = %v %v", fs, err)
	}
	if fs, err := stackGate("lint")(ctx, r); err != nil || !slices.Equal(rules(fs), []string{"gate_missing"}) {
		t.Fatalf("lint = %v %v", fs, err)
	}
	// audit runs only when a lock file changed.
	if fs, err := stackGate("audit")(ctx, r); err != nil || len(fs) != 0 {
		t.Fatalf("audit with no lock file change = %v %v", fs, err)
	}
	commit(t, r, "build: add a dependency", map[string]string{"go.sum": "x\n"})
	if fs, err := stackGate("audit")(ctx, r); err != nil || !slices.Equal(rules(fs), []string{"command_failed"}) {
		t.Fatalf("audit with go.sum changed = %v %v", fs, err)
	}
}
