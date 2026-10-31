package config

import (
	"slices"
	"testing"
)

// A base branch can name a stage that a later release adds before the host
// runs that release (F-63). The manifest still loads under the installed
// release: the entry reaches each stage this release knows, and the name it
// does not know reaches no run.
func TestCBug1ManifestLoadsWhenAnEntryNamesAStageOfALaterRelease(t *testing.T) {
	doc := "secrets:\n  - name: JEV_API_KEY\n    stages: [fixer, stage-of-a-later-release]\n"
	m, err := ParseManifest([]byte(doc))
	if err != nil {
		t.Fatalf("ParseManifest: %v, want the manifest to load under this release", err)
	}
	if got := m.ForStage("fixer").Names(); !slices.Equal(got, []string{"JEV_API_KEY"}) {
		t.Fatalf("ForStage(fixer) = %v, want [JEV_API_KEY]", got)
	}
	if got := m.ForStage("implementer").Names(); len(got) != 0 {
		t.Fatalf("ForStage(implementer) = %v, want none", got)
	}
}

// An entry that names only stages this release does not know reaches no run
// of this release, not every run as an entry with no stages does.
func TestCBug1EntryOfOnlyUnknownStagesReachesNoRun(t *testing.T) {
	m, err := ParseManifest([]byte("secrets:\n  - name: WEB_KEY\n    stages: [deploy]\n"))
	if err != nil {
		t.Fatalf("ParseManifest: %v, want the manifest to load under this release", err)
	}
	for _, stage := range append(RunStages(), "review-room") {
		if got := m.ForStage(stage).Names(); len(got) != 0 {
			t.Fatalf("ForStage(%s) = %v, want none", stage, got)
		}
	}
}
