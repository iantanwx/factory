package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Every stage and native job has an entry, and the shipped defaults carry
// R13's caps (KTD30).
func TestDefaultThroughputCoversEveryKey(t *testing.T) {
	d := DefaultThroughput()
	for _, k := range ThroughputKeys() {
		if _, ok := d[k]; !ok {
			t.Fatalf("DefaultThroughput has no entry for %s", k)
		}
	}
	if len(d) != len(ThroughputKeys()) {
		t.Fatalf("DefaultThroughput holds %d entries, want %d", len(d), len(ThroughputKeys()))
	}
	for k, want := range map[string]int{"implementer": 8, "reviewers": 10, "code-reviewer": 8, "intake": 8} {
		if d[k].Cap != want {
			t.Fatalf("default cap of %s = %d, want %d (R13)", k, d[k].Cap, want)
		}
	}
	cfg := &HostConfig{StateDir: "/var/lib/loop", Throughput: d}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
}

func TestThroughputKeysHoldStagesJobsAndPools(t *testing.T) {
	keys := ThroughputKeys()
	for _, k := range []string{"implementer", "code-reviewer", "doc-reviewer", "intake", "merge-handoff", "reviewers"} {
		if !slices.Contains(keys, k) {
			t.Fatalf("ThroughputKeys lacks %s: %v", k, keys)
		}
	}
	if slices.Contains(RunStages(), "reviewers") || slices.Contains(RunStages(), "merge-handoff") {
		t.Fatalf("RunStages holds a pool or a native job: %v", RunStages())
	}
}

// A host config with no entry for a stage or a native job fails to load
// and names it. So does a cap below 1, an unknown key, a stage with no
// schedule and a pool with one.
func TestThroughputRefusals(t *testing.T) {
	cases := map[string]struct{ edit, names string }{
		"missing stage":        {"  fixer: {cap: 1, schedule: \"14,44 * * * *\"}\n", "throughput.fixer"},
		"missing native job":   {"  decision-timers: {cap: 1, schedule: \"* * * * *\"}\n", "throughput.decision-timers"},
		"missing pool":         {"  reviewers: {cap: 10}\n", "throughput.reviewers"},
		"zero cap":             {"  implementer: {cap: 4, ", "throughput.implementer.cap"},
		"no schedule":          {"  amender: {cap: 2, schedule: \"16,46 * * * *\"}\n", "throughput.amender.schedule"},
		"pool with a schedule": {"  reviewers: {cap: 10}\n", "throughput.reviewers.schedule"},
		"unknown stage":        {"", "throughput.merger"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := validHost
			switch name {
			case "zero cap":
				doc = strings.Replace(doc, c.edit, "  implementer: {cap: 0, ", 1)
			case "no schedule":
				doc = strings.Replace(doc, c.edit, "  amender: {cap: 2}\n", 1)
			case "pool with a schedule":
				doc = strings.Replace(doc, c.edit, "  reviewers: {cap: 10, schedule: \"* * * * *\"}\n", 1)
			case "unknown stage":
				doc = strings.Replace(doc, "throughput:\n", "throughput:\n  merger: {cap: 1, schedule: \"* * * * *\"}\n", 1)
			default:
				doc = strings.Replace(doc, c.edit, "", 1)
			}
			if doc == validHost {
				t.Fatal("the edit changed nothing")
			}
			_, err := ParseHost([]byte(doc))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("err = %v, want a refusal that names %s", err, c.names)
			}
		})
	}
}

func TestLimitsNameTheStageAndItsPool(t *testing.T) {
	cfg, err := ParseHost([]byte(validHost))
	if err != nil {
		t.Fatal(err)
	}
	ls := cfg.Limits("code-reviewer")
	if len(ls) != 2 || ls[0].Name != "code-reviewer" || ls[0].Cap != 8 || ls[1].Name != "reviewers" || ls[1].Cap != 10 {
		t.Fatalf("Limits(code-reviewer) = %+v", ls)
	}
	if !slices.Equal(ls[1].Stages, []string{"code-reviewer", "doc-reviewer"}) {
		t.Fatalf("the reviewers pool counts %v", ls[1].Stages)
	}
	if ls := cfg.Limits("implementer"); len(ls) != 1 || ls[0].Cap != 4 || !slices.Equal(ls[0].Stages, []string{"implementer"}) {
		t.Fatalf("Limits(implementer) = %+v", ls)
	}
	if ls := cfg.Limits("nonsense"); len(ls) != 0 {
		t.Fatalf("Limits(nonsense) = %+v", ls)
	}
}

type alertLog struct{ raised, cleared []string }

func (a *alertLog) Raise(_ context.Context, al Alert) error {
	a.raised = append(a.raised, al.DedupeKey+": "+al.Body)
	return nil
}

func (a *alertLog) Clear(_ context.Context, key string) error {
	a.cleared = append(a.cleared, key)
	return nil
}

// A changed entry takes effect on the next load with no restart. An
// invalid file keeps the last good config in force and raises an alert.
func TestHostLoaderReloadsAndKeepsTheLastGoodConfig(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), HostConfigFile)
	write := func(doc string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	alerts := &alertLog{}
	l := &HostLoader{Path: path, Alerts: alerts}
	if _, err := l.Load(ctx); err == nil {
		t.Fatal("a loader with no file and no good config loaded")
	}
	write(validHost)
	cfg, err := l.Load(ctx)
	if err != nil || cfg.Throughput["implementer"].Cap != 4 {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
	write(strings.Replace(validHost, "implementer: {cap: 4,", "implementer: {cap: 2,", 1))
	if cfg, err = l.Load(ctx); err != nil || cfg.Throughput["implementer"].Cap != 2 {
		t.Fatalf("a reload did not take the new cap: %+v, %v", cfg, err)
	}
	write(strings.Replace(validHost, "  fixer: {cap: 1, schedule: \"14,44 * * * *\"}\n", "", 1))
	cfg, err = l.Load(ctx)
	if err != nil || cfg.Throughput["implementer"].Cap != 2 {
		t.Fatalf("an invalid file did not keep the last good config: %+v, %v", cfg, err)
	}
	if len(alerts.raised) != 1 || !strings.Contains(alerts.raised[0], "throughput.fixer") {
		t.Fatalf("alerts = %v, want one that names throughput.fixer", alerts.raised)
	}
	write(validHost)
	if cfg, err = l.Load(ctx); err != nil || cfg.Throughput["implementer"].Cap != 4 {
		t.Fatalf("Load = %+v, %v", cfg, err)
	}
	if len(alerts.cleared) == 0 || alerts.cleared[len(alerts.cleared)-1] != AlertHostConfigInvalid {
		t.Fatalf("a valid load did not clear the alert: %v", alerts.cleared)
	}
}

// With no good config yet, an invalid file is the caller's error.
func TestHostLoaderWithNoGoodConfigFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), HostConfigFile)
	if err := os.WriteFile(path, []byte(strings.Replace(validHost, "  reviewers: {cap: 10}\n", "", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&HostLoader{Path: path}).Load(context.Background())
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "throughput.reviewers") {
		t.Fatalf("err = %v", err)
	}
}
