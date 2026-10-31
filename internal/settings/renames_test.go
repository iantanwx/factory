package settings

import (
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// withStageRenames puts pairs in the rename table that a settings change
// reads, for the test.
func withStageRenames(t *testing.T, pairs ...[2]string) {
	t.Helper()
	saved := stageRenames
	stageRenames = func() [][2]string { return pairs }
	t.Cleanup(func() { stageRenames = saved })
}

const oldStageConfig = `merge:
  mechanism: direct
stages:
  code-reviewer: {}
  # the builder
  old-stage:
    cap: 3
    model: claude-sonnet-5-5 # the cheaper model
`

// C6: settings set refuses a key that names an old stage, and the hint
// names the new key.
func TestC6SettingsSetRefusesAnOldStageKey(t *testing.T) {
	withStageRenames(t, [2]string{"old-stage", config.StageImplementer})
	value := "2"
	_, ref := editSetting(config.DefaultHost("/var/lib/loop"), []byte(oldStageConfig), "stages.old-stage.cap", &value, nil)
	if ref == nil || ref.Code != cli.CodeUsage || ref.Rule != "stage_renamed" || !strings.Contains(ref.Hint, "stages.implementer.cap") {
		t.Fatalf("settings set stages.old-stage.cap = %+v, want USAGE stage_renamed naming stages.implementer.cap", ref)
	}
}

// C7: a change on the new key of a stage that the file holds under its
// old key proposes a file that holds the stage under the new key only,
// with the old entry's other fields and the new value. That file loads.
func TestC7SettingsChangeMovesAnOldEntry(t *testing.T) {
	withStageRenames(t, [2]string{"old-stage", config.StageImplementer})
	hc := config.DefaultHost("/var/lib/loop")
	value := "2"
	e, ref := editSetting(hc, []byte(oldStageConfig), "stages.implementer.cap", &value, nil)
	if ref != nil {
		t.Fatalf("settings set stages.implementer.cap = %+v", ref)
	}
	if e.old.Value != "3" {
		t.Fatalf("the old value in force = %q, want the old entry's 3", e.old.Value)
	}
	want := `merge:
  mechanism: direct
stages:
  code-reviewer: {}
  # the builder
  implementer:
    cap: 2
    model: claude-sonnet-5-5 # the cheaper model
`
	if string(e.raw) != want {
		t.Fatalf("the proposed file:\n%s\nwant:\n%s", e.raw, want)
	}
	pc, err := config.ParseProjectConfig(e.raw)
	if err == nil {
		err = pc.CheckStages(config.KnownStages(hc))
	}
	if err != nil {
		t.Fatalf("the proposed file does not load: %v", err)
	}
	if got := pc.Stages[config.StageImplementer]; got.Cap == nil || *got.Cap != 2 || got.Model != "claude-sonnet-5-5" {
		t.Fatalf("the implementer entry = %+v, want cap 2 and the old model", got)
	}
}

// C7: an unset on the new key moves the old entry too, and removes the
// one field.
func TestC7SettingsUnsetMovesAnOldEntry(t *testing.T) {
	withStageRenames(t, [2]string{"old-stage", config.StageImplementer})
	e, ref := editSetting(config.DefaultHost("/var/lib/loop"), []byte(oldStageConfig), "stages.implementer.model", nil, nil)
	if ref != nil {
		t.Fatalf("settings set stages.implementer.model --unset = %+v", ref)
	}
	if s := string(e.raw); strings.Contains(s, "old-stage") || !strings.Contains(s, "  implementer:\n    cap: 3\n") || strings.Contains(s, "model:") {
		t.Fatalf("the proposed file:\n%s", s)
	}
}
