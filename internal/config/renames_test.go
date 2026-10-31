package config

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// withStageRenames puts pairs in the rename table for the test.
func withStageRenames(t *testing.T, pairs map[string]string) {
	t.Helper()
	saved := stageRenames
	stageRenames = pairs
	t.Cleanup(func() { stageRenames = saved })
}

// openAlerts reads the open alerts of kind under dedupe key, with their
// bodies and counts.
func openAlerts(t *testing.T, ctx context.Context, l *Loader, key string) (bodies []string, counts []int) {
	t.Helper()
	st := l.Alerts.(storeAlerter).st
	err := st.Read(ctx, func(tx *sql.Tx) error {
		r, err := tx.Query(`SELECT body, count FROM alerts WHERE dedupe_key = ? AND state = 'open' AND kind = ?`, key, AlertOldStageNames)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var b string
			var n int
			if err := r.Scan(&b, &n); err != nil {
				return err
			}
			bodies, counts = append(bodies, b), append(counts, n)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return bodies, counts
}

func storeLoader(t *testing.T, ctx context.Context) *Loader {
	t.Helper()
	st := openStore(t)
	if err := StoreProjectRow(st)(ctx, Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}); err != nil {
		t.Fatal(err)
	}
	return &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st)}
}

// C1, done means of the ticket: with the pair old-stage to new-stage,
// stages.old-stage.cap: 2 loads as cap 2 under new-stage, and the project
// holds one open old_stage_names alert that names both.
func TestC1OldStageKeyLoadsUnderItsNewKey(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": "new-stage"})
	ctx := context.Background()
	l := storeLoader(t, ctx)
	origin := fakeOrigin{ProjectConfigPath: "merge:\n  mechanism: direct\nstages:\n  old-stage:\n    cap: 2\n"}

	got, err := l.Load(ctx, "alpha", Refs{Read: origin.read}, "main")
	if err != nil || got.Stale {
		t.Fatalf("load = %+v, %v", got, err)
	}
	if c := got.Config.Stages["new-stage"].Cap; c == nil || *c != 2 {
		t.Fatalf("stages.new-stage.cap = %v, want 2", c)
	}
	if _, ok := got.Config.Stages["old-stage"]; ok {
		t.Fatal("the config still holds stages.old-stage")
	}
	want := []Rename{{Source: ".loop/config.yaml", Old: "old-stage", New: "new-stage"}}
	if !slices.Equal(got.Renames, want) {
		t.Fatalf("Renames = %+v, want %+v", got.Renames, want)
	}
	bodies, _ := openAlerts(t, ctx, l, "old_stage_names:alpha")
	if len(bodies) != 1 || !strings.Contains(bodies[0], "old-stage") || !strings.Contains(bodies[0], "new-stage") ||
		!strings.Contains(bodies[0], ".loop/config.yaml") {
		t.Fatalf("open old_stage_names alerts = %q, want one that names old-stage, new-stage and .loop/config.yaml", bodies)
	}
}

// C1: the value under an old key is the value in force for the new stage,
// and the settings name the new key only.
func TestC1OldStageKeyValueIsInForceUnderTheNewStage(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	pc, err := ParseProjectConfig([]byte("merge:\n  mechanism: direct\nstages:\n  old-stage:\n    cap: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.CheckStages(codeKnown()); err != nil {
		t.Fatalf("CheckStages: %v", err)
	}
	ls := Effective(DefaultHost("/var/lib/loop"), pc).Limits("alpha", StageImplementer)
	if len(ls) == 0 || ls[0].Cap != 2 || ls[0].Field != "stages.implementer.cap" {
		t.Fatalf("Limits(implementer) = %+v, want cap 2 from stages.implementer.cap", ls)
	}
	for _, s := range Effective(DefaultHost("/var/lib/loop"), pc).Settings(codeKnown(), nil) {
		if strings.Contains(s.Key.String(), "old-stage") {
			t.Fatalf("settings name the old key: %s", s.Key)
		}
		if s.Key.String() == "stages.implementer.cap" && (s.Value != "2" || s.Source != SourceProject) {
			t.Fatalf("stages.implementer.cap = %q from %s, want 2 from project", s.Value, s.Source)
		}
	}
}

// C1: every pair of the table loads its old key under its new key.
func TestC1EveryPairOfTheTableLoadsUnderItsNewKey(t *testing.T) {
	for _, p := range StageRenames() {
		pc, err := ParseProjectConfig([]byte("merge:\n  mechanism: direct\nstages:\n  " + p[0] + ":\n    cap: 2\n"))
		if err != nil {
			t.Fatalf("%s: %v", p[0], err)
		}
		if c := pc.Stages[p[1]].Cap; c == nil || *c != 2 {
			t.Fatalf("stages.%s.cap = %v, want 2 from stages.%s", p[1], c, p[0])
		}
		if err := pc.CheckStages(codeKnown()); err != nil {
			t.Fatalf("%s: CheckStages: %v", p[0], err)
		}
	}
}

// C2: a read with an old name raises one alert, a second read counts on it,
// and a read with no old name clears it.
func TestC2OldNameRaisesOneAlertAndACleanReadClearsIt(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	ctx := context.Background()
	l := storeLoader(t, ctx)
	origin := fakeOrigin{ProjectConfigPath: "merge:\n  mechanism: direct\nstages:\n  old-stage: {}\n"}
	for range 2 {
		if _, err := l.Load(ctx, "alpha", Refs{Read: origin.read}, "main"); err != nil {
			t.Fatal(err)
		}
	}
	if bodies, counts := openAlerts(t, ctx, l, "old_stage_names:alpha"); len(bodies) != 1 || counts[0] != 2 {
		t.Fatalf("after two reads: %d open alerts with counts %v, want one", len(bodies), counts)
	}
	origin[ProjectConfigPath] = "merge:\n  mechanism: direct\nstages:\n  implementer: {}\n"
	got, err := l.Load(ctx, "alpha", Refs{Read: origin.read}, "main")
	if err != nil || len(got.Renames) != 0 {
		t.Fatalf("clean load = %+v, %v", got, err)
	}
	if bodies, _ := openAlerts(t, ctx, l, "old_stage_names:alpha"); len(bodies) != 0 {
		t.Fatalf("a clean read left %d open alerts", len(bodies))
	}
}

// C2: a host config with an old throughput key loads it under the new key,
// and raises the host's alert once. A clean read clears it.
func TestC2HostOldThroughputKeyRaisesTheHostAlert(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), HostConfigFile)
	write := func(doc string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(strings.Replace(validHost, "  implementer: {cap: 4,", "  old-stage: {cap: 4,", 1))
	alerts := &alertLog{}
	l := &HostLoader{Path: path, Alerts: alerts}
	for range 2 {
		cfg, err := l.Load(ctx)
		if err != nil || cfg.Throughput[StageImplementer].Cap != 4 {
			t.Fatalf("Load = %+v, %v", cfg, err)
		}
		want := []Rename{{Source: path, Old: "old-stage", New: StageImplementer}}
		if !slices.Equal(cfg.Renames, want) {
			t.Fatalf("Renames = %+v, want %+v", cfg.Renames, want)
		}
	}
	if len(alerts.raised) != 1 || !strings.HasPrefix(alerts.raised[0], "old_stage_names:host: ") ||
		!strings.Contains(alerts.raised[0], "old-stage") || !strings.Contains(alerts.raised[0], StageImplementer) {
		t.Fatalf("raised = %q, want one old_stage_names:host alert that names old-stage and implementer", alerts.raised)
	}
	write(validHost)
	if _, err := l.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(alerts.cleared, "old_stage_names:host") {
		t.Fatalf("cleared = %q, want old_stage_names:host", alerts.cleared)
	}
}

// C3: a project config that holds one stage under its old key and its new
// key does not load, the error names both keys, and the last good config
// stays in force.
func TestC3ProjectStageUnderTwoKeysDoesNotLoad(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	both := "merge:\n  mechanism: direct\nstages:\n  old-stage: {cap: 2}\n  implementer: {cap: 3}\n"
	_, err := ParseProjectConfig([]byte(both))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "stages.old-stage") ||
		!strings.Contains(err.Error(), "stages.implementer") {
		t.Fatalf("err = %v, want ErrInvalid that names stages.old-stage and stages.implementer", err)
	}
	l, _ := checkedLoader(t)
	got, err := loadAfterGood(t, l, both)
	if err != nil || !got.Stale || got.Config.Stages[StageImplementer].Cap != nil {
		t.Fatalf("load = %+v, %v, want the last good config", got, err)
	}
}

// C3: a host config that holds one stage under its old key and its new key
// does not load, and the error names both keys.
func TestC3HostStageUnderTwoKeysDoesNotLoad(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	doc := strings.Replace(validHost, "  reviewers: {cap: 10}\n", "  reviewers: {cap: 10}\n  old-stage: {cap: 2, schedule: \"*/5 * * * *\"}\n", 1)
	_, err := ParseHost([]byte(doc))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "throughput.old-stage") ||
		!strings.Contains(err.Error(), "throughput.implementer") {
		t.Fatalf("err = %v, want ErrInvalid that names throughput.old-stage and throughput.implementer", err)
	}
}

// C4: a name that is no stage, no job and no old stage name still refuses
// the config, next to an old name that loads.
func TestC4UnknownStageNameStillRefusesTheConfig(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	pc, err := ParseProjectConfig([]byte("merge:\n  mechanism: direct\nstages:\n  old-stage: {cap: 2}\n  no-such-stage: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = pc.CheckStages(codeKnown())
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "stages.no-such-stage") {
		t.Fatalf("err = %v, want ErrInvalid that names stages.no-such-stage", err)
	}
	if strings.Contains(err.Error(), "old-stage") || strings.Contains(err.Error(), "stages.implementer") {
		t.Fatalf("err = %v, names the renamed stage too", err)
	}
}

// C5: a secret whose stages list holds an old name goes to the new stage,
// and a list that holds both names gives the secret once.
func TestC5ManifestOldStageNameReachesTheNewStage(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	m, err := ParseManifest([]byte(`
secrets:
  - name: OLD_ONLY
    stages: [old-stage]
  - name: BOTH
    stages: [old-stage, implementer]
  - name: OTHER
    stages: [decomposer]
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ForStage(StageImplementer).Names(); !slices.Equal(got, []string{"OLD_ONLY", "BOTH"}) {
		t.Fatalf("ForStage(implementer) = %v, want [OLD_ONLY BOTH]", got)
	}
	for _, e := range m.Secrets[:2] {
		if !slices.Equal(e.Stages, []string{StageImplementer}) {
			t.Fatalf("%s stages = %v, want [implementer]", e.Name, e.Stages)
		}
	}
	if len(m.Renames) != 2 || m.Renames[0].Old != "old-stage" || m.Renames[0].New != StageImplementer {
		t.Fatalf("Renames = %+v, want old-stage to implementer twice", m.Renames)
	}
}

// C5 with C2: the old name in the manifest raises the project's alert, and
// the alert names the manifest's path.
func TestC5ManifestOldStageNameRaisesTheAlert(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	ctx := context.Background()
	l := storeLoader(t, ctx)
	origin := fakeOrigin{
		ProjectConfigPath:   "merge:\n  mechanism: direct\n",
		DefaultManifestPath: "secrets:\n  - name: JEV\n    stages: [old-stage]\n",
	}
	got, err := l.Load(ctx, "alpha", Refs{Read: origin.read}, "main")
	if err != nil {
		t.Fatal(err)
	}
	if names := got.Manifest.ForStage(StageImplementer).Names(); !slices.Equal(names, []string{"JEV"}) {
		t.Fatalf("ForStage(implementer) = %v", names)
	}
	bodies, _ := openAlerts(t, ctx, l, "old_stage_names:alpha")
	if len(bodies) != 1 || !strings.Contains(bodies[0], DefaultManifestPath) || !strings.Contains(bodies[0], "old-stage") {
		t.Fatalf("open alerts = %q, want one that names %s and old-stage", bodies, DefaultManifestPath)
	}
}

// The table keeps its invariants: no rename chains, each new name is a
// stage of this loop, and no old name is one.
func TestStageRenamesKeepTheirInvariants(t *testing.T) {
	// The room package imports this one, so the room stages are spelled
	// here: room.Stage of each room.Kinds.
	stages := append(RunStages(), "intake-room", "decision-room", "review-room", "architect-room")
	current := slices.Concat(ThroughputKeys(), ProjectJobs(), InstanceJobs(), stages)
	for _, p := range StageRenames() {
		if _, chained := NewStageName(p[1]); chained {
			t.Errorf("%s renames to %s, which is an old name too", p[0], p[1])
		}
		if !slices.Contains(stages, p[1]) {
			t.Errorf("%s renames to %s, which is no stage of this loop", p[0], p[1])
		}
		if slices.Contains(current, p[0]) {
			t.Errorf("old name %s is still a stage, job or pool of this loop", p[0])
		}
	}
}

// Each old key of the table is a retired word.
func TestRetiredWordsHoldEachOldKey(t *testing.T) {
	withStageRenames(t, map[string]string{"old-stage": StageImplementer})
	if !slices.Contains(RetiredWords(), "old-stage") {
		t.Fatalf("RetiredWords() = %v, want old-stage", RetiredWords())
	}
	if n, ok := NewStageName("old-stage"); !ok || n != StageImplementer {
		t.Fatalf("NewStageName(old-stage) = %q, %v", n, ok)
	}
	if _, ok := NewStageName(StageImplementer); ok {
		t.Fatal("a new name has a new name")
	}
}

// C1, work entry 3: stages.amender.cap: 2 loads as the reviser's cap of 2,
// and the read names amender and reviser.
func TestC1AmenderCapLoadsAsTheRevisersCap(t *testing.T) {
	pc, err := ParseProjectConfig([]byte("merge:\n  mechanism: direct\nstages:\n  amender:\n    cap: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.CheckStages(codeKnown()); err != nil {
		t.Fatalf("CheckStages: %v", err)
	}
	if c := pc.Stages["reviser"].Cap; c == nil || *c != 2 {
		t.Fatalf("stages.reviser.cap = %v, want 2", c)
	}
	want := []Rename{{Source: ProjectConfigPath, Old: "amender", New: "reviser"}}
	if !slices.Equal(pc.Renames, want) {
		t.Fatalf("Renames = %+v, want %+v", pc.Renames, want)
	}
}

// C12, factorio-2mwq: the table holds the pair fixer to triage, so the
// golden transition tables map fixer through it.
func TestC12TableRenamesFixerToTriage(t *testing.T) {
	if n, ok := NewStageName("fixer"); !ok || n != "triage" {
		t.Fatalf("NewStageName(fixer) = %q, %v, want triage", n, ok)
	}
}

// C5, factorio-2mwq: the manifest this repository held before the rename,
// stages: [fixer, retro], gives Jev's key to the triage stage.
func TestC5ManifestFixerReachesTheTriageStage(t *testing.T) {
	m, err := ParseManifest([]byte("secrets:\n  - name: TYPESAFE_API_KEY\n    stages: [fixer, retro]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ForStage("triage").Names(); !slices.Equal(got, []string{"TYPESAFE_API_KEY"}) {
		t.Fatalf("ForStage(triage) = %v, want [TYPESAFE_API_KEY]", got)
	}
	want := []Rename{{Source: DefaultManifestPath, Old: "fixer", New: "triage"}}
	if !slices.Equal(m.Renames, want) {
		t.Fatalf("Renames = %+v, want %+v", m.Renames, want)
	}
}

// C9, C12: the prd-author stage is now unattended-brainstorm, and its old
// key and its old role words are retired.
func TestPRDAuthorIsNowUnattendedBrainstorm(t *testing.T) {
	if n, ok := NewStageName("prd-author"); !ok || n != "unattended-brainstorm" {
		t.Fatalf("NewStageName(prd-author) = %q, %v, want unattended-brainstorm", n, ok)
	}
	for _, w := range []string{"prd-author", "PRD author"} {
		if !slices.Contains(RetiredWords(), w) {
			t.Errorf("RetiredWords() = %v, want %q", RetiredWords(), w)
		}
	}
}
