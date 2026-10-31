package retro

import "testing"

func TestParseRefusesAnUnknownPrefix(t *testing.T) {
	for _, s := range []string{"run:r-1a2b", "round:factorio-x1y:2", "transition:418", "escalation:7", "fault:F-3",
		"comment:factorio-x1y:991", "signal:12", "learning:L-4"} {
		if c, err := Parse(s); err != nil || string(c) != s {
			t.Errorf("Parse(%q) = %q, %v", s, c, err)
		}
	}
	for _, s := range []string{"", "run", "run:", "pr:12", "Run:r-1", ":7"} {
		if c, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = %q, want an error", s, c)
		}
	}
}
