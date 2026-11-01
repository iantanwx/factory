package config

import (
	"path/filepath"
	"slices"
	"testing"
)

// C2 of docs/plans/factorio-nhb5-qa-stage-spec.md: the qa stage runs after
// the code-reviewer, with cap 3 on the minutes 2, 7, ..., 57. A host config
// that an older release wrote holds no qa entry, and it still loads. A
// project config that does not list qa does not enable it.
func TestC2QAStageIsOptionalAndOffUnlessListed(t *testing.T) {
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
	pc, err := ParseProjectConfig([]byte("stages:\n  implementer: {}\n  code-reviewer: {}\nmerge:\n  mechanism: direct\n"))
	if err != nil {
		t.Fatal(err)
	}
	if pc.StageEnabled("qa") {
		t.Fatal("a project config that does not list qa enables it")
	}
}
