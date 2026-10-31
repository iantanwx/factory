package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
		"missing stage":        {"  triage: {cap: 1, schedule: \"14,44 * * * *\"}\n", "throughput.triage"},
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
	write(strings.Replace(validHost, "  triage: {cap: 1, schedule: \"14,44 * * * *\"}\n", "", 1))
	cfg, err = l.Load(ctx)
	if err != nil || cfg.Throughput["implementer"].Cap != 2 {
		t.Fatalf("an invalid file did not keep the last good config: %+v, %v", cfg, err)
	}
	if len(alerts.raised) != 1 || !strings.Contains(alerts.raised[0], "throughput.triage") {
		t.Fatalf("alerts = %v, want one that names throughput.triage", alerts.raised)
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

const agentJobsHost = "agent_jobs:\n  nightly-qa: {cap: 1, schedule: \"55 2 * * *\", runbook: docs/runbooks/nightly-qa.md}\n"

// An agent job is a stage kind host config defines: an agent run that
// claims no item and starts on its own cron. Its cap is its own limit.
func TestAgentJobIsAnItemlessStageWithItsOwnLimit(t *testing.T) {
	cfg, err := ParseHost([]byte(validHost + agentJobsHost))
	if err != nil {
		t.Fatal(err)
	}
	j, ok := cfg.AgentJob("nightly-qa")
	if !ok || j.Cap != 1 || j.Schedule != "55 2 * * *" || j.Runbook != "docs/runbooks/nightly-qa.md" {
		t.Fatalf("AgentJob(nightly-qa) = %+v, %v", j, ok)
	}
	if _, ok := cfg.AgentJob("implementer"); ok {
		t.Fatal("a run stage reads as an agent job")
	}
	if got := cfg.AgentJobNames(); !slices.Equal(got, []string{"nightly-qa"}) {
		t.Fatalf("AgentJobNames = %v", got)
	}
	ls := cfg.Limits("nightly-qa")
	if len(ls) != 1 || ls[0].Name != "nightly-qa" || ls[0].Cap != 1 || !slices.Equal(ls[0].Stages, []string{"nightly-qa"}) {
		t.Fatalf("Limits(nightly-qa) = %+v", ls)
	}
}

// An agent job that shadows a stage, a native job or a pool, or that has
// no cap, no schedule or no clean runbook path in the repository, fails
// to load and names the field.
func TestAgentJobRefusals(t *testing.T) {
	cases := map[string]struct{ entry, names string }{
		"shadows a stage":      {"  triage: {cap: 1, schedule: \"0 3 * * *\", runbook: a.md}\n", "agent_jobs.triage"},
		"shadows a native job": {"  merge-handoff: {cap: 1, schedule: \"0 3 * * *\", runbook: a.md}\n", "agent_jobs.merge-handoff"},
		"shadows a pool":       {"  reviewers: {cap: 1, schedule: \"0 3 * * *\", runbook: a.md}\n", "agent_jobs.reviewers"},
		"bad name":             {"  Nightly: {cap: 1, schedule: \"0 3 * * *\", runbook: a.md}\n", "agent_jobs.Nightly"},
		"zero cap":             {"  nightly: {cap: 0, schedule: \"0 3 * * *\", runbook: a.md}\n", "agent_jobs.nightly.cap"},
		"no schedule":          {"  nightly: {cap: 1, runbook: a.md}\n", "agent_jobs.nightly.schedule"},
		"no runbook":           {"  nightly: {cap: 1, schedule: \"0 3 * * *\"}\n", "agent_jobs.nightly.runbook"},
		"absolute runbook":     {"  nightly: {cap: 1, schedule: \"0 3 * * *\", runbook: /etc/a.md}\n", "agent_jobs.nightly.runbook"},
		"runbook outside":      {"  nightly: {cap: 1, schedule: \"0 3 * * *\", runbook: ../a.md}\n", "agent_jobs.nightly.runbook"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseHost([]byte(validHost + "agent_jobs:\n" + c.entry))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("err = %v, want a refusal that names %s", err, c.names)
			}
		})
	}
}

// C1: overwatch is a native job with an entry, cap 1, on the minutes 3, 13,
// 23, 33, 43 and 53 (ADR 0009, A1).
func TestC1OverwatchEntryExists(t *testing.T) {
	if !slices.Contains(ThroughputKeys(), "overwatch") {
		t.Fatalf("ThroughputKeys lacks overwatch: %v", ThroughputKeys())
	}
	if !slices.Contains(NativeJobs(), "overwatch") {
		t.Fatalf("NativeJobs lacks overwatch: %v", NativeJobs())
	}
	want := Throughput{Cap: 1, Schedule: "3,13,23,33,43,53 * * * *"}
	if got := DefaultThroughput()["overwatch"]; got != want {
		t.Fatalf("DefaultThroughput()[overwatch] = %+v, want %+v", got, want)
	}
}

// C2: a host config that v0.0.22 wrote holds no overwatch entry. It loads,
// ThroughputFor reads the default without writing it, and a save writes no
// overwatch key.
func TestC2OldHostConfigLoadsAndStaysAsItWas(t *testing.T) {
	cfg, err := LoadHost(filepath.Join("testdata", "host-v0.0.22.yaml"))
	if err != nil {
		t.Fatalf("the v0.0.22 host config does not load: %v", err)
	}
	want := Throughput{Cap: 1, Schedule: "3,13,23,33,43,53 * * * *"}
	if got := cfg.ThroughputFor("overwatch"); got != want {
		t.Fatalf("ThroughputFor(overwatch) = %+v, want %+v", got, want)
	}
	if _, ok := cfg.Throughput["overwatch"]; ok {
		t.Fatal("ThroughputFor wrote the overwatch entry into the config")
	}
	if ls := cfg.Limits("overwatch"); len(ls) != 1 || ls[0].Cap != 1 {
		t.Fatalf("Limits(overwatch) = %+v, want the default cap 1", ls)
	}
	if _, ok := cfg.Throughput["overwatch"]; ok {
		t.Fatal("Limits wrote the overwatch entry into the config")
	}
	if got := cfg.ThroughputFor("implementer"); got != (Throughput{Cap: 8, Schedule: "3,18,33,48 * * * *"}) {
		t.Fatalf("ThroughputFor(implementer) = %+v, want the file's entry", got)
	}
	path := filepath.Join(t.TempDir(), HostConfigFile)
	if err := SaveHost(path, cfg); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "overwatch") {
		t.Fatalf("SaveHost wrote an overwatch key:\n%s", data)
	}
	if _, err := LoadHost(path); err != nil {
		t.Fatalf("the saved config does not load: %v", err)
	}
}

// C2: a present overwatch entry is checked as every other entry is, and
// every entry v0.0.22 requires stays required.
func TestC2OverwatchEntryRefusals(t *testing.T) {
	cases := map[string]struct{ doc, names string }{
		"zero cap": {
			strings.Replace(validHost, "throughput:\n", "throughput:\n  overwatch: {cap: 0, schedule: \"3,13,23,33,43,53 * * * *\"}\n", 1),
			"throughput.overwatch.cap",
		},
		"empty schedule": {
			strings.Replace(validHost, "throughput:\n", "throughput:\n  overwatch: {cap: 1, schedule: \"\"}\n", 1),
			"throughput.overwatch.schedule",
		},
		"no merge-handoff": {
			strings.Replace(validHost, "  merge-handoff: {cap: 1, schedule: \"*/5 * * * *\"}\n", "", 1),
			"throughput.merge-handoff",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.doc == validHost {
				t.Fatal("the edit changed nothing")
			}
			_, err := ParseHost([]byte(c.doc))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.names) {
				t.Fatalf("err = %v, want a refusal that names %s", err, c.names)
			}
		})
	}
	good := strings.Replace(validHost, "throughput:\n", "throughput:\n  overwatch: {cap: 2, schedule: \"*/10 * * * *\"}\n", 1)
	cfg, err := ParseHost([]byte(good))
	if err != nil {
		t.Fatalf("a valid overwatch entry does not load: %v", err)
	}
	if got := cfg.ThroughputFor("overwatch"); got != (Throughput{Cap: 2, Schedule: "*/10 * * * *"}) {
		t.Fatalf("ThroughputFor(overwatch) = %+v, want the file's entry", got)
	}
}

// C13: a host config that an older release wrote holds no prd-author
// entry. It loads, ThroughputFor reads the default without writing it,
// and the stage runs among the run stages.
func TestC13OldHostConfigLoadsWithNoPRDAuthorEntry(t *testing.T) {
	cfg, err := LoadHost(filepath.Join("testdata", "host-v0.0.22.yaml"))
	if err != nil {
		t.Fatalf("the v0.0.22 host config does not load: %v", err)
	}
	want := Throughput{Cap: 2, Schedule: "5,35 * * * *"}
	if got := cfg.ThroughputFor("prd-author"); got != want {
		t.Fatalf("ThroughputFor(prd-author) = %+v, want %+v", got, want)
	}
	if _, ok := cfg.Throughput["prd-author"]; ok {
		t.Fatal("ThroughputFor wrote the prd-author entry into the config")
	}
	if ls := cfg.Limits("prd-author"); len(ls) != 1 || ls[0].Cap != 2 {
		t.Fatalf("Limits(prd-author) = %+v, want the default cap 2", ls)
	}
	if !slices.Contains(RunStages(), "prd-author") {
		t.Fatalf("RunStages lacks prd-author: %v", RunStages())
	}
}

// C13: a present prd-author entry is read as the file holds it.
func TestC13PRDAuthorEntryReadsItsValues(t *testing.T) {
	good := strings.Replace(validHost, "throughput:\n", "throughput:\n  prd-author: {cap: 3, schedule: \"*/15 * * * *\"}\n", 1)
	cfg, err := ParseHost([]byte(good))
	if err != nil {
		t.Fatalf("a valid prd-author entry does not load: %v", err)
	}
	if got := cfg.ThroughputFor("prd-author"); got != (Throughput{Cap: 3, Schedule: "*/15 * * * *"}) {
		t.Fatalf("ThroughputFor(prd-author) = %+v, want the file's entry", got)
	}
}

// C15: a host config that v0.0.58 wrote holds no orca-acceptance entry. It
// loads, the job reads cap 1 on 30 17 * * * without writing the entry, and a
// save writes no orca-acceptance key.
func TestC15OrcaAcceptanceEntryDefaults(t *testing.T) {
	if !slices.Contains(NativeJobs(), "orca-acceptance") {
		t.Fatalf("NativeJobs lacks orca-acceptance: %v", NativeJobs())
	}
	cfg, err := LoadHost(filepath.Join("testdata", "host-v0.0.58.yaml"))
	if err != nil {
		t.Fatalf("the v0.0.58 host config does not load: %v", err)
	}
	want := Throughput{Cap: 1, Schedule: "30 17 * * *"}
	if got := cfg.ThroughputFor("orca-acceptance"); got != want {
		t.Fatalf("ThroughputFor(orca-acceptance) = %+v, want %+v", got, want)
	}
	if ls := cfg.Limits("orca-acceptance"); len(ls) != 1 || ls[0].Cap != 1 {
		t.Fatalf("Limits(orca-acceptance) = %+v, want the default cap 1", ls)
	}
	if _, ok := cfg.Throughput["orca-acceptance"]; ok {
		t.Fatal("a read wrote the orca-acceptance entry into the config")
	}
	path := filepath.Join(t.TempDir(), HostConfigFile)
	if err := SaveHost(path, cfg); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "orca-acceptance") {
		t.Fatalf("SaveHost wrote an orca-acceptance key:\n%s", data)
	}
	if _, err := LoadHost(path); err != nil {
		t.Fatalf("the saved config does not load: %v", err)
	}
}

// C15: config load checks a present orca-acceptance entry as every other
// entry, and reads a valid one from the file.
func TestC15OrcaAcceptanceEntryRefusals(t *testing.T) {
	bad := strings.Replace(validHost, "throughput:\n", "throughput:\n  orca-acceptance: {cap: 0, schedule: \"30 17 * * *\"}\n", 1)
	if _, err := ParseHost([]byte(bad)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "throughput.orca-acceptance.cap") {
		t.Fatalf("err = %v, want a refusal that names throughput.orca-acceptance.cap", err)
	}
	good := strings.Replace(validHost, "throughput:\n", "throughput:\n  orca-acceptance: {cap: 2, schedule: \"0 3 * * *\"}\n", 1)
	cfg, err := ParseHost([]byte(good))
	if err != nil {
		t.Fatalf("a valid orca-acceptance entry does not load: %v", err)
	}
	if got := cfg.ThroughputFor("orca-acceptance"); got != (Throughput{Cap: 2, Schedule: "0 3 * * *"}) {
		t.Fatalf("ThroughputFor(orca-acceptance) = %+v, want the file's entry", got)
	}
}

// C23: with no overdue key, the implementer's overdue wait is 90 minutes. A
// key on the implementer entry sets it.
func TestC23OverdueDefaultsTo90Minutes(t *testing.T) {
	cfg, err := ParseHost([]byte(validHost))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Overdue(); got != 90*time.Minute {
		t.Fatalf("Overdue() = %v with no overdue key, want 90m", got)
	}
	doc := strings.Replace(validHost, "  implementer: {cap: 4, ", "  implementer: {overdue: 45m, cap: 4, ", 1)
	if cfg, err = ParseHost([]byte(doc)); err != nil {
		t.Fatalf("an implementer overdue of 45m does not load: %v", err)
	}
	if got := cfg.Overdue(); got != 45*time.Minute {
		t.Fatalf("Overdue() = %v, want the file's 45m", got)
	}
}

// C23: host config validation refuses a negative overdue, and an overdue on
// any entry but the implementer's.
func TestC23OverdueRefusals(t *testing.T) {
	cases := map[string]struct{ from, to, names string }{
		"negative": {"  implementer: {cap: 4, ", "  implementer: {overdue: -1m, cap: 4, ", "throughput.implementer.overdue"},
		"another stage": {"  code-reviewer: {cap: 8, ", "  code-reviewer: {overdue: 90m, cap: 8, ",
			"throughput.code-reviewer.overdue"},
		"a pool":       {"  reviewers: {cap: 10}", "  reviewers: {cap: 10, overdue: 90m}", "throughput.reviewers.overdue"},
		"a native job": {"  merge-handoff: {cap: 1, ", "  merge-handoff: {overdue: 90m, cap: 1, ", "throughput.merge-handoff.overdue"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			doc := strings.Replace(validHost, c.from, c.to, 1)
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

// A host config that v0.0.58 wrote holds no retro entry. It loads, and the
// retro stage reads cap 1 on the triage schedule without writing the entry.
func TestOldHostConfigLoadsWithNoRetroEntry(t *testing.T) {
	if !slices.Contains(RunStages(), "retro") {
		t.Fatalf("RunStages lacks retro: %v", RunStages())
	}
	cfg, err := LoadHost(filepath.Join("testdata", "host-v0.0.58.yaml"))
	if err != nil {
		t.Fatalf("the v0.0.58 host config does not load: %v", err)
	}
	if got, want := cfg.ThroughputFor("retro"), (Throughput{Cap: 1, Schedule: "14,44 * * * *"}); got != want {
		t.Fatalf("ThroughputFor(retro) = %+v, want %+v", got, want)
	}
	if _, ok := cfg.Throughput["retro"]; ok {
		t.Fatal("ThroughputFor wrote the retro entry into the config")
	}
}
