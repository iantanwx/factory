package config

import "testing"

// C9: the simplify stage is on by default, and a project turns it off. No
// other stage changes: an unlisted reproducer stays off. `loop settings
// show` reports the default as the source of an unlisted simplify stage.
func TestC9SimplifyIsOnByDefaultAndAProjectTurnsItOff(t *testing.T) {
	hc, err := ParseHost([]byte(validHost))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, stages string
		on           bool
		source       Source
	}{
		{"unlisted", "  implementer: {}\n", true, SourceDefault},
		{"listed", "  implementer: {}\n  simplify: {}\n", true, SourceProject},
		{"turned off", "  implementer: {}\n  simplify: {enabled: false}\n", false, SourceProject},
	} {
		t.Run(c.name, func(t *testing.T) {
			pc, err := ParseProjectConfig([]byte("merge:\n  mechanism: direct\nstages:\n" + c.stages))
			if err != nil {
				t.Fatal(err)
			}
			if got := pc.StageEnabled(StageSimplify); got != c.on {
				t.Fatalf("StageEnabled(simplify) = %v, want %v", got, c.on)
			}
			if pc.StageEnabled(StageReproducer) {
				t.Fatal("StageEnabled(reproducer) = true for a config that does not list it")
			}
			want := map[bool]string{true: "true", false: "false"}[c.on]
			for _, s := range Effective(hc, pc).Settings(KnownStages(hc), func(string, string) (string, bool) { return "", false }) {
				if s.Key.String() != "stages.simplify.enabled" {
					continue
				}
				if s.Value != want || s.Source != c.source {
					t.Fatalf("stages.simplify.enabled = %q from %s, want %q from %s", s.Value, s.Source, want, c.source)
				}
				return
			}
			t.Fatal("settings list no stages.simplify.enabled")
		})
	}
}
