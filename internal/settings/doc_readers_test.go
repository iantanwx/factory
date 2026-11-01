package settings

import (
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// C5: settings set refuses doc_readers on a stage other than the
// doc-reviewer, and lands nothing. On the doc-reviewer it lands.
func TestC5SettingsSetRefusesDocReadersOnAnotherStage(t *testing.T) {
	f := newSetFixture(t, baseConfig)
	_, ref := f.set("stages.implementer.doc_readers", strp("false"), "the reason")
	f.refused(ref, cli.CodeInvalidSetting, "stages.implementer.doc_readers")

	f = newSetFixture(t, baseConfig+"  doc-reviewer: {}\n")
	f.accept("stages.doc-reviewer.doc_readers", strp("false"))
	if len(f.proposed) != 1 {
		t.Fatalf("%d proposals", len(f.proposed))
	}
}

// C23: settings set refuses each change that would put doc_readers: true
// beside a doc-reviewer harness other than claude, and lands nothing. The
// hint names the other field of the pair.
func TestC23SettingsSetRefusesReadersBesideAnotherHarness(t *testing.T) {
	for _, c := range []struct{ name, cfg, key, value, other string }{
		{"doc_readers true beside codex", "  doc-reviewer: {harness: codex}\n", "stages.doc-reviewer.doc_readers", "true", "stages.doc-reviewer.harness"},
		{"codex beside doc_readers true", "  doc-reviewer: {doc_readers: true}\n", "stages.doc-reviewer.harness", "codex", "stages.doc-reviewer.doc_readers"},
		{"escalated codex beside doc_readers true", "  doc-reviewer: {doc_readers: true}\n", "stages.doc-reviewer.escalated.harness", "codex", "stages.doc-reviewer.doc_readers"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSetFixture(t, baseConfig+c.cfg)
			_, ref := f.set(c.key, strp(c.value), "the reason")
			f.refused(ref, cli.CodeInvalidSetting, c.other)
		})
	}
}
