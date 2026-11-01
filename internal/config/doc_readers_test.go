package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const docReadersBase = "merge:\n  mechanism: direct\nstages:\n"

// settingRow returns the row of key in the settings of pc, and false when
// the settings list no such key.
func settingRow(t *testing.T, pc *ProjectConfig, key string) (Setting, bool) {
	t.Helper()
	k := Known{RunStages: RunStages(), ProjectJobs: ProjectJobs()}
	for _, st := range Effective(DefaultHost("/var/lib/loop"), pc).Settings(k, nil) {
		if st.Key.String() == key {
			return st, true
		}
	}
	return Setting{}, false
}

// C5: doc_readers on any stage other than the doc-reviewer fails the parse
// with an error that names the field. No other stage takes it as a setting
// key, and the settings list it on the doc-reviewer alone, with its value
// and source.
func TestC5DocReadersLivesOnTheDocReviewerOnly(t *testing.T) {
	for _, stage := range []string{"implementer", "code-reviewer", "reviser", "planner"} {
		t.Run(stage, func(t *testing.T) {
			_, err := ParseProjectConfig([]byte(docReadersBase + "  " + stage + ":\n    doc_readers: false\n"))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "stages."+stage+".doc_readers") {
				t.Fatalf("err = %v, want ErrInvalid naming stages.%s.doc_readers", err, stage)
			}
			if _, err := ParseSettingKey("stages."+stage+".doc_readers", codeKnown()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("setting key stages.%s.doc_readers: err = %v, want ErrInvalid", stage, err)
			}
		})
	}
	if _, err := ParseSettingKey("stages.doc-reviewer.doc_readers", codeKnown()); err != nil {
		t.Fatalf("setting key stages.doc-reviewer.doc_readers: %v", err)
	}
	pc, err := ParseProjectConfig([]byte(docReadersBase + "  doc-reviewer:\n    doc_readers: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := settingRow(t, pc, "stages.doc-reviewer.doc_readers"); !ok || st.Value != "false" || st.Source != SourceProject {
		t.Fatalf("doc_readers set false: row %+v, listed %v", st, ok)
	}
	if _, ok := settingRow(t, pc, "stages.implementer.doc_readers"); ok {
		t.Fatal("the settings list stages.implementer.doc_readers")
	}
	pc, err = ParseProjectConfig([]byte(docReadersBase + "  doc-reviewer: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if st, ok := settingRow(t, pc, "stages.doc-reviewer.doc_readers"); !ok || st.Value != "true" || st.Source != SourceDefault {
		t.Fatalf("doc_readers unset: row %+v, listed %v", st, ok)
	}
}

// C1: with no doc_readers key and no doc-reviewer harness, or harness
// claude, the readers are all three, and the settings row reads true by
// default with no reason.
func TestC1ReadersAreOnByDefault(t *testing.T) {
	for _, doc := range []string{"  doc-reviewer: {}\n", "  doc-reviewer:\n    harness: claude\n", "  implementer: {}\n"} {
		pc, err := ParseProjectConfig([]byte(docReadersBase + doc))
		if err != nil {
			t.Fatal(err)
		}
		if got := pc.DocReaders(); !slices.Equal(got.Readers, []string{"unslop", "asd-ste100", "content-lens"}) || got.Off != "" {
			t.Fatalf("%q: readers = %+v, want all three", doc, got)
		}
		if st, _ := settingRow(t, pc, "stages.doc-reviewer.doc_readers"); st.Value != "true" || st.Source != SourceDefault || st.Reason != "" {
			t.Fatalf("%q: settings row = %+v, want true by default with no reason", doc, st)
		}
	}
}

// C23: doc_readers: true beside a doc-reviewer harness or escalated
// harness other than claude fails the parse with an error that names both
// fields. Beside claude it parses, and the readers are in force.
func TestC23DocReadersTrueBesideAnotherHarnessFailsTheParse(t *testing.T) {
	for field, doc := range map[string]string{
		"stages.doc-reviewer.harness":           "  doc-reviewer:\n    doc_readers: true\n    harness: codex\n",
		"stages.doc-reviewer.escalated.harness": "  doc-reviewer:\n    doc_readers: true\n    escalated:\n      harness: codex\n",
	} {
		t.Run(field, func(t *testing.T) {
			_, err := ParseProjectConfig([]byte(docReadersBase + doc))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "stages.doc-reviewer.doc_readers") || !strings.Contains(err.Error(), field) {
				t.Fatalf("err = %v, want ErrInvalid naming stages.doc-reviewer.doc_readers and %s", err, field)
			}
		})
	}
	pc, err := ParseProjectConfig([]byte(docReadersBase + "  doc-reviewer:\n    doc_readers: true\n    harness: claude\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := pc.DocReaders(); !slices.Equal(got.Readers, []string{"unslop", "asd-ste100", "content-lens"}) || got.Off != "" {
		t.Fatalf("doc_readers true beside claude: %+v", got)
	}
}

// C4 and C23: the readers are off when doc_readers is false, and when it
// is unset beside a doc-reviewer harness other than claude. The reason
// names the field that turned them off, and the settings row names it too.
func TestC23ReadersOffNameTheirReason(t *testing.T) {
	cases := []struct{ name, doc, off string }{
		{"doc_readers false", "  doc-reviewer:\n    doc_readers: false\n", "stages.doc-reviewer.doc_readers is false"},
		{"unset beside codex", "  doc-reviewer:\n    harness: codex\n", "stages.doc-reviewer.harness is codex"},
		{"unset beside an escalated codex", "  doc-reviewer:\n    escalated:\n      harness: codex\n", "stages.doc-reviewer.escalated.harness is codex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pc, err := ParseProjectConfig([]byte(docReadersBase + c.doc))
			if err != nil {
				t.Fatal(err)
			}
			if got := pc.DocReaders(); len(got.Readers) != 0 || got.Off != c.off {
				t.Fatalf("readers = %+v, want none, off by %q", got, c.off)
			}
			if st, _ := settingRow(t, pc, "stages.doc-reviewer.doc_readers"); st.Value != "false" || st.Reason != c.off {
				t.Fatalf("settings row = %+v, want false with reason %q", st, c.off)
			}
		})
	}
}

// C5: a push that changes only doc_readers changes a setting, so a stage
// run's push that holds it gets SETTINGS_CHANGE. The push scan decides with
// SettingsChanged.
func TestC5PushThatChangesDocReadersChangesASetting(t *testing.T) {
	unset := []byte(docReadersBase + "  doc-reviewer:\n    harness: claude\n")
	for _, value := range []string{"true", "false"} {
		set := []byte(docReadersBase + "  doc-reviewer:\n    harness: claude\n    doc_readers: " + value + "\n")
		for _, pair := range [][2][]byte{{unset, set}, {set, unset}} {
			changed, err := SettingsChanged(pair[0], pair[1])
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatalf("SettingsChanged(%q, %q) = false, want true", pair[0], pair[1])
			}
		}
	}
	changed, err := SettingsChanged(
		[]byte(docReadersBase+"  doc-reviewer:\n    doc_readers: true\n"),
		[]byte(docReadersBase+"  doc-reviewer:\n    doc_readers: false\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("SettingsChanged from doc_readers true to false = false, want true")
	}
}
