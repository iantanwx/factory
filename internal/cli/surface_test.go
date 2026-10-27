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

// C18: `loop work shape` with no --priority, or a priority outside 1 to 4,
// exits 2 with USAGE before it reaches the server.
func TestC18WorkShapeRefusesAMissingOrUnknownPriority(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"no priority", nil},
		{"priority 0", []string{"--priority", "0"}},
		{"priority 5", []string{"--priority", "5"}},
		{"priority high", []string{"--priority", "high"}},
	} {
		args := append([]string{"work", "shape", "bug-1", "--title", "t", "--body", "b"}, c.args...)
		env, code := runJSON(t, New(), args...)
		if code != 2 || env["code"] != CodeUsage {
			t.Errorf("%s: exit %d, code %v; want exit 2 with USAGE", c.name, code, env["code"])
		}
	}
}
