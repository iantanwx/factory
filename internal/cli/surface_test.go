package cli

import "testing"

// C1 and C3: `loop work create --help` and `loop work shape --help` list
// blocked-by.
func TestWorkCreateAndWorkShapeHelpListBlockedBy(t *testing.T) {
	r := New()
	for _, verb := range []string{"create", "shape"} {
		help, code := runJSON(t, r, "work", verb, "--help")
		if code != 0 {
			t.Fatalf("work %s --help exited %d: %v", verb, code, help)
		}
		found := false
		for _, f := range help["data"].(map[string]any)["flags"].([]any) {
			found = found || f.(map[string]any)["name"] == "blocked-by"
		}
		if !found {
			t.Errorf("work %s --help lists no blocked-by", verb)
		}
	}
}

// C5 and C6: `loop work block --help` and `loop work unblock --help` list
// by. work block requires it, and work unblock takes it alone.
func TestWorkBlockAndWorkUnblockHelpListBy(t *testing.T) {
	r := New()
	for verb, required := range map[string]bool{"block": true, "unblock": false} {
		help, code := runJSON(t, r, "work", verb, "--help")
		if code != 0 {
			t.Fatalf("work %s --help exited %d: %v", verb, code, help)
		}
		var by map[string]any
		for _, f := range help["data"].(map[string]any)["flags"].([]any) {
			if f.(map[string]any)["name"] == "by" {
				by = f.(map[string]any)
			}
		}
		if by == nil {
			t.Errorf("work %s --help lists no by", verb)
			continue
		}
		if got, _ := by["required"].(bool); got != required {
			t.Errorf("work %s --by required = %v, want %v", verb, got, required)
		}
	}
}
