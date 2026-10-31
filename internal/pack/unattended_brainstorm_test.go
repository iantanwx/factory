package pack_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
)

const brainstormRunbook = "runbooks/unattended-brainstorm.md"

// C10: the manifest names the unattended-brainstorm runbook, unattended,
// with the six skills of spec factorio-4n0 Work entry 4, and holds no
// prd-author key. The runbook lays out as loop-unattended-brainstorm,
// names itself so, and sends the writer to the brainstorm skill and its
// PRD template.
func TestC10UnattendedBrainstormRunbookHasItsNewName(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if _, old := m.Runbooks["prd-author"]; old {
		t.Error("the manifest still names a prd-author runbook")
	}
	rb, ok := m.Runbooks["unattended-brainstorm"]
	if !ok {
		t.Fatal("the manifest names no unattended-brainstorm runbook")
	}
	if rb.Attended {
		t.Error("the unattended brainstorm runs with nobody present, and its runbook says it is attended")
	}
	want := []string{"loop-sdlc-turn", "loop-sdlc-glossary", "loop-sdlc-triage", "loop-sdlc-split", "loop-sdlc-documents", "loop-brainstorm"}
	if !slices.Equal(slices.Sorted(slices.Values(rb.Skills)), slices.Sorted(slices.Values(want))) {
		t.Errorf("unattended-brainstorm skills = %v, want %v", rb.Skills, want)
	}
	if got := pack.RunbookSource("unattended-brainstorm"); got != brainstormRunbook {
		t.Errorf("RunbookSource(unattended-brainstorm) = %q, want %q", got, brainstormRunbook)
	}
	text := read(t, brainstormRunbook)
	if !strings.Contains(text, "\nname: loop-unattended-brainstorm\n") {
		t.Error("the unattended-brainstorm runbook does not name itself loop-unattended-brainstorm")
	}
	for _, ptr := range []string{"`../loop-brainstorm/SKILL.md`", "`../loop-brainstorm/prd.md`"} {
		if !strings.Contains(text, ptr) {
			t.Errorf("the unattended-brainstorm runbook does not name %s", ptr)
		}
	}
}

// C15: ADR 0001 D4 ports no intake author. Outside tests, pack, internal
// and cmd hold no name of tickertown's intake author or its coverage verb.
// The walk must read the unattended brainstorm's runbook, so a walk that reads
// nothing cannot pass.
func TestNoIntakeAuthorIsPorted(t *testing.T) {
	root := filepath.Join("..", "..")
	banned := []string{"intake-author", "sdlc-intake", "intake coverage"}
	readRunbook := false
	for _, dir := range []string{"pack", "internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if filepath.ToSlash(path) == "../../pack/"+brainstormRunbook {
				readRunbook = true
			}
			for _, b := range banned {
				if strings.Contains(string(raw), b) {
					t.Errorf("%s names %q", path, b)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !readRunbook {
		t.Errorf("the walk did not read pack/%s", brainstormRunbook)
	}
}

// C18: the unattended brainstorm's runbook holds ADR 0001's source rule. The request
// and amendment facts are the only sources a story quotes, a requirement
// with no source goes under Open questions, a request with no story goes
// to a level 4 escalation, and the author never splits.
func TestUnattendedBrainstormRunbookHoldsTheSourceRule(t *testing.T) {
	text := strings.Join(strings.Fields(read(t, brainstormRunbook)), " ")
	for _, want := range []string{
		"These two facts are the only source a story quotes",
		"the `request` fact",
		"the `amendment` fact",
		"A requirement with no source goes under Open questions",
		"`loop escalation raise --score 4`",
		"Never split",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the unattended-brainstorm runbook does not hold %q", want)
		}
	}
}

// repoFile reads a file of the repository outside the pack, such as the
// project policy, the plan or an ADR.
func repoFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", path))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// joined returns the text from the first line that starts with start up to
// the next line that starts with end, with each run of white space as one
// space, so a wrapped line still matches.
func joined(t *testing.T, name, text, start, end string) string {
	t.Helper()
	i := strings.Index(text, "\n"+start)
	if i < 0 {
		t.Fatalf("%s holds no line that starts with %q", name, start)
	}
	rest := text[i+1:]
	if j := strings.Index(rest[len(start):], "\n"+end); j >= 0 {
		rest = rest[:j+len(start)]
	}
	return strings.Join(strings.Fields(rest), " ")
}

// C16: the coordinator routes level 1 to 3 product work to the unattended
// brainstorm,
// and parks a request that the brainstorm cap refuses on a pending decision,
// so the request waits in the record and never in the chat.
func TestCoordinatorRoutesProductWorkToTheUnattendedBrainstorm(t *testing.T) {
	coordinator := read(t, "runbooks/coordinator.md")
	route := section(t, coordinator, "8. New work")
	for _, want := range []string{
		"loop epic create --request",
		"--blocked-by",
		"`prd_author_disabled`",
		"`unattended-brainstorm: {}`",
		"`CAP_FULL`",
		"loop decision file",
		"--blocked-by <decision>",
		"which epics hold the cap",
		"Level 4 or 5, or the Choice outside",
		"brainstorm room",
	} {
		if !strings.Contains(route, want) {
			t.Errorf("coordinator section 8 does not name %q", want)
		}
	}
	wake := section(t, coordinator, "6. Decisions")
	for _, want := range []string{"brainstorm cap", "loop decision moot <decision>", "raises the cap"} {
		if !strings.Contains(wake, want) {
			t.Errorf("coordinator section 6 does not name %q", want)
		}
	}
	policy := repoFile(t, ".loop/policy/coordinator.md")
	row := joined(t, ".loop/policy/coordinator.md", policy, "| Product", "|")
	for _, want := range []string{"`loop epic create --request` at level 1 to 3", "level 4 or 5"} {
		if !strings.Contains(row, want) {
			t.Errorf("the policy's product route row does not name %q: %s", want, row)
		}
	}
}

// C19: the shared instructions name the unattended brainstorm, its states
// and its check, and the plan and ADR 0001 hold the second entry path.
func TestSharedInstructionsNameTheUnattendedBrainstorm(t *testing.T) {
	review := section(t, read(t, "runbooks/doc-reviewer.md"), "2. Review a PRD")
	for _, want := range []string{"the `request` fact", "the `amendment` fact", "each story"} {
		if !strings.Contains(review, want) {
			t.Errorf("doc-reviewer section 2 does not name %q", want)
		}
	}

	glossary := read(t, "skills/loop-sdlc-glossary/SKILL.md")
	if !strings.Contains(section(t, glossary, "Stages"), "| `unattended-brainstorm` |") {
		t.Error("the glossary's stage table names no `unattended-brainstorm`")
	}
	epic := joined(t, "glossary", glossary, "- **Epic**:", "- **")
	for _, want := range []string{"`backlog`", "`prd_needed`", "`prd_drafting`"} {
		if !strings.Contains(epic, want) {
			t.Errorf("the glossary's epic states do not name %s: %s", want, epic)
		}
	}
	if prd := joined(t, "glossary", glossary, "- **PRD**:", "- **"); !strings.Contains(prd, "an unattended brainstorm or a brainstorm room") {
		t.Errorf("the glossary's PRD entry does not name its two writers: %s", prd)
	}

	split := read(t, splitSkill)
	if !strings.Contains(section(t, split, "3. Who splits"), "The unattended brainstorm never splits") {
		t.Error("split skill section 3 does not say that the unattended brainstorm never splits")
	}
	never := section(t, split, "10. Never")
	if !strings.Contains(never, "the coordinator, with `loop epic create`") {
		t.Error("split skill section 10 does not name `loop epic create` as the coordinator's create")
	}
	if strings.Contains(never, "`loop epic create --architecture`") {
		t.Error("split skill section 10 still names `loop epic create --architecture` as the coordinator's only create")
	}

	if stages := joined(t, "loop-init", read(t, "skills/loop-init/SKILL.md"), "stages:", "signoff:"); !strings.Contains(stages, "unattended-brainstorm: {}") {
		t.Error("the loop-init config template does not list `unattended-brainstorm: {}` under stages")
	}

	plan := repoFile(t, "docs/plans/2026-10-21-1328-feat-standalone-sdlc-loop-plan.md")
	if r44 := joined(t, "the plan", plan, "- R44.", "- R45."); !strings.Contains(r44, "prd-author") {
		t.Errorf("the plan's R44 does not name prd-author: %s", r44)
	}
	if n := strings.Count(repoFile(t, "docs/adr/0001-unattended-prd-author.md"), "make proto"); n != 0 {
		t.Errorf("ADR 0001 names make proto %d times, want 0", n)
	}
}
