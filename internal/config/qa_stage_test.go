package config

import (
	"path/filepath"
	"slices"
	"testing"
)

// C2 of docs/plans/factorio-nhb5-qa-stage-spec.md: the qa stage runs after
// the code-reviewer, with cap 3 on the minutes 2, 7, ..., 57. A host config
// that an older release wrote holds no qa entry, and it still loads.
func TestC2QAStageIsOptional(t *testing.T) {
	stages := RunStages()
	cr := slices.Index(stages, "code-reviewer")
	if cr < 0 || cr+1 >= len(stages) || stages[cr+1] != "qa" {
		t.Fatalf("RunStages() = %v, want qa right after code-reviewer", stages)
	}
	want := Throughput{Cap: 3, Schedule: "2,7,12,17,22,27,32,37,42,47,52,57 * * * *"}
	if got := DefaultThroughput()["qa"]; got != want {
		t.Fatalf("DefaultThroughput()[qa] = %+v, want %+v", got, want)
	}
	cfg, err := LoadHost(filepath.Join("testdata", "host-v0.0.22.yaml"))
	if err != nil {
		t.Fatalf("a host config with no qa entry does not load: %v", err)
	}
	if got := cfg.ThroughputFor("qa"); got != want {
		t.Fatalf("ThroughputFor(qa) = %+v, want %+v", got, want)
	}
	if _, ok := cfg.Throughput["qa"]; ok {
		t.Fatal("ThroughputFor wrote the qa entry into the config")
	}
}

// C33: the qa stage is on by default, and a project turns it off with
// enabled: false. An unlisted stage outside defaultOn stays off.
func TestC33QAStageIsOnByDefaultAndAProjectTurnsItOff(t *testing.T) {
	for _, c := range []struct {
		name, stages string
		qa           bool
	}{
		{"unlisted", "  implementer: {}\n", true},
		{"listed", "  implementer: {}\n  qa: {}\n", true},
		{"turned on", "  implementer: {}\n  qa: {enabled: true}\n", true},
		{"turned off", "  implementer: {}\n  qa: {enabled: false}\n", false},
	} {
		pc, err := ParseProjectConfig([]byte("merge:\n  mechanism: direct\nstages:\n" + c.stages))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := pc.StageEnabled("qa"); got != c.qa {
			t.Fatalf("%s: StageEnabled(qa) = %t, want %t", c.name, got, c.qa)
		}
		for _, other := range []string{"reproducer", "code-reviewer"} {
			if pc.StageEnabled(other) {
				t.Fatalf("%s: StageEnabled(%s) = true for an unlisted stage", c.name, other)
			}
		}
	}
}

// C33: loop settings show reports stages.qa.enabled as true from default
// when the config does not list qa, and from the project when it does.
func TestC33SettingsReportTheSourceOfQAEnabled(t *testing.T) {
	hc, err := ParseHost([]byte(validHost + agentJobsHost))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		stages, value string
		source        Source
	}{
		{"  implementer: {}\n", "true", SourceDefault},
		{"  implementer: {}\n  qa: {}\n", "true", SourceProject},
		{"  implementer: {}\n  qa: {enabled: false}\n", "false", SourceProject},
	} {
		pc, err := ParseProjectConfig([]byte("merge:\n  mechanism: direct\nstages:\n" + c.stages))
		if err != nil {
			t.Fatal(err)
		}
		var got *Setting
		for _, r := range Effective(hc, pc).Settings(KnownStages(hc), func(string, string) (string, bool) { return "", false }) {
			if r.Key.String() == "stages.qa.enabled" {
				got = &r
			}
		}
		if got == nil || got.Value != c.value || got.Source != c.source {
			t.Fatalf("stages:\n%sstages.qa.enabled = %+v, want %q from %s", c.stages, got, c.value, c.source)
		}
	}
}
