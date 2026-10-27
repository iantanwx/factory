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
