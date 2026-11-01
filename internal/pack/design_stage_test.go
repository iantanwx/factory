package pack_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/shape"
)

// TestC11PRDWritersSetTheUILine proves C11 of spec factorio-a4yq. The PRD
// template holds a UI line under its story, where the prd gate reads it.
// The brainstorm skill and the brainstorm room set the line of each story
// with the operator, in a project whose config holds a ui key. The
// unattended brainstorm writes each line from the request. Each file says
// so in one paragraph, so words scattered over the file do not pass.
func TestC11PRDWritersSetTheUILine(t *testing.T) {
	stories := shape.Stories(read(t, "skills/loop-brainstorm/prd.md"))
	if len(stories) != 1 || stories[0].UI == shape.UIMissing {
		t.Errorf("the PRD template's stories = %+v, want one story with a **UI:** line under it", stories)
	}
	for file, words := range map[string][]string{
		"skills/loop-brainstorm/SKILL.md":   {"`**UI:** yes`", "`**UI:** no`", "`ui` key", "operator"},
		"runbooks/brainstorm-room.md":       {"`**UI:**`", "`ui` key", "operator"},
		"runbooks/unattended-brainstorm.md": {"`**UI:**`", "`ui` key", "`request` fact"},
	} {
		if !slices.ContainsFunc(strings.Split(read(t, file), "\n\n"), func(p string) bool {
			return !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(p, w) })
		}) {
			t.Errorf("%s holds no paragraph that names each of %q", file, words)
		}
	}
}

// TestC23DocReviewerChecksEachVisibleCriterionAgainstAState proves C23 of
// spec factorio-a4yq. The doc-reviewer runbook holds a section that starts
// "Review a design record". It reads each criterion of each UI story, and
// rejects a criterion that a user can see, or a refusal, with no state
// that shows it. It submits with loop review submit, and never merges.
// Its description names the design record, because the skill list shows
// only that line.
func TestC23DocReviewerChecksEachVisibleCriterionAgainstAState(t *testing.T) {
	_, rest, _ := strings.Cut(read(t, docReviewerRB), "\ndescription:")
	if desc, _, _ := strings.Cut(rest, "\n"); !strings.Contains(desc, "design record") {
		t.Errorf("the description of %s does not name the design record: %s", docReviewerRB, desc)
	}
	s := flat(pack.Section(read(t, docReviewerRB), 2, func(title string) bool {
		_, rest, _ := strings.Cut(title, ". ")
		return strings.HasPrefix(rest, "Review a design record")
	}))
	if s == "" {
		t.Fatalf("%s holds no section that starts %q", docReviewerRB, "Review a design record")
	}
	holdsAll(t, "the design review of the doc-reviewer runbook", s, []string{
		"Read each acceptance criterion of each `UI: yes` story of the PRD.",
		"Reject when a criterion that a user can see has no state, and name the criterion in the comment.",
		"Reject when a criterion that says the product refuses something has no state that shows the refusal.",
		`loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"`,
		"Never merge the design record.",
	})
}

// TestC16CoordinatorSkipsTheDesignStageWithTheOperatorsWords proves C16 of
// spec factorio-a4yq on the pack side. The coordinator runbook runs the
// skip only on the operator's word, with those words, on an epic in
// design. It says that an open design pull request holds the skip.
func TestC16CoordinatorSkipsTheDesignStageWithTheOperatorsWords(t *testing.T) {
	s := flat(coordinatorSection(t, "8"))
	if !strings.Contains(s, `loop epic skip-design <epic> --request "<the operator's words>"`) {
		t.Errorf("section 8 of %s runs no skip with the operator's words", coordinatorRunbook)
	}
	for _, w := range []string{"`design`", "`spec_needed`", "`design_skipped`", "`design_pr_open`"} {
		if !strings.Contains(s, w) {
			t.Errorf("section 8 of %s does not name %s", coordinatorRunbook, w)
		}
	}
}

const designRoomRunbook = "runbooks/design-room.md"

// canvasNames lists each canvas that a project's design skill drives. No
// loop runbook, gate or state names one. Add one name for each new canvas.
var canvasNames = []string{"paper.design", "Figma", "Penpot", "Framer"}

// paragraphWith reports whether text holds one paragraph that names each
// of words, so words scattered over the file do not pass.
func paragraphWith(text string, words []string) bool {
	return slices.ContainsFunc(strings.Split(text, "\n\n"), func(p string) bool {
		p = flat(p)
		return !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(p, w) })
	})
}

// TestC25CoordinatorOpensTheDesignRoomOnTheOperatorsYes proves C25 of spec
// factorio-a4yq on the pack side. The coordinator runbook brings the
// design entry to the operator, and opens the design room on the
// operator's yes. It names the refused answer and the refused open.
func TestC25CoordinatorOpensTheDesignRoomOnTheOperatorsYes(t *testing.T) {
	s := coordinatorSection(t, "8")
	for _, words := range [][]string{
		{"`design/<project>/<epic>`", "operator", "`design_entry`"},
		{"`not_in_design`", "`design`"},
	} {
		if !paragraphWith(s, words) {
			t.Errorf("section 8 of %s holds no paragraph that names each of %q", coordinatorRunbook, words)
		}
	}
	ps := strings.Split(s, "\n\n")
	if !slices.ContainsFunc(ps[:len(ps)-1], func(p string) bool {
		i := slices.Index(ps, p)
		return strings.Contains(p, "operator says yes") && strings.Contains(ps[i+1], "loop room open --kind design --item <epic>")
	}) {
		t.Errorf("section 8 of %s opens no design room on the operator's yes", coordinatorRunbook)
	}
	if !strings.Contains(coordinatorSection(t, "7"), "brainstorm|decision|review|architect|design") {
		t.Errorf("section 7 of %s does not list the design room among the kinds of room_open", coordinatorRunbook)
	}
}

// TestC26DesignRoomBuildsFirstAndPortsEachCanvasEditBack proves C26 of spec
// factorio-a4yq. The manifest names the attended design-room runbook. The
// runbook builds the prototype in a worktree on loop/<epic>-prototype, and
// runs it with the brief's run command. It pushes the prototype before any
// step that names the canvas skill. With a skill, the room imports each
// screen, reads each of the operator's edits back, ports it, commits and
// pushes. With none, it skips every canvas step. Then it pins the commit,
// captures the pictures, and opens the design pull request, in that order.
func TestC26DesignRoomBuildsFirstAndPortsEachCanvasEditBack(t *testing.T) {
	m, err := pack.ReadManifest(pack.Source())
	if err != nil {
		t.Fatal(err)
	}
	if rb, ok := m.Runbooks["design-room"]; !ok || !rb.Attended {
		t.Fatalf("the manifest's design-room runbook = %+v, %v; want an attended runbook", rb, ok)
	}
	rb := read(t, designRoomRunbook)
	if !strings.Contains(rb, "\nname: loop-design-room\n") {
		t.Error("the design-room runbook does not name itself loop-design-room")
	}
	s := flat(rb)
	order := []string{
		"git worktree add",
		"`loop/<epic>-prototype`",
		"the `run` command of the brief's `design` object",
		"loop branch push --prototype",
		"the skill that the brief names",
		"Import each screen",
		"Read each edit back",
		"Port each edit into the prototype",
		"`**Prototype:**`",
		"loop design capture --prototype",
		"loop gates --docs",
		"Push the design branch with `loop branch push`",
		"loop pr open",
	}
	at := -1
	for _, w := range order {
		i := strings.Index(s[at+1:], w)
		if i < 0 {
			t.Fatalf("%s names no %q after the step before it; the order is %q", designRoomRunbook, w, order)
		}
		at += 1 + i
	}
	if first, push := strings.Index(s, "the skill that the brief names"), strings.Index(s, "loop branch push --prototype"); first < push {
		t.Errorf("%s names the canvas skill before the first prototype push", designRoomRunbook)
	}
	if !paragraphWith(rb, []string{"names no skill", "skip"}) {
		t.Errorf("%s holds no paragraph that skips the canvas steps when the brief names no skill", designRoomRunbook)
	}
}

// TestC27DesignRoomReadsTheCanvasSkillByTheNameInItsBrief proves C27 of
// spec factorio-a4yq on the pack side. The runbook names the canvas skill
// only as the skill that the brief names, and reads only loop skills by
// path.
func TestC27DesignRoomReadsTheCanvasSkillByTheNameInItsBrief(t *testing.T) {
	rb := read(t, designRoomRunbook)
	if !paragraphWith(rb, []string{"the skill that the brief names", "`design.skill`", "`.loop/brief.json`"}) {
		t.Errorf("%s holds no paragraph that reads the canvas skill by the name in design.skill of .loop/brief.json", designRoomRunbook)
	}
	for _, m := range regexp.MustCompile(`\.\./([^/\s]+)/SKILL\.md`).FindAllStringSubmatch(rb, -1) {
		if !strings.HasPrefix(m[1], "loop-") {
			t.Errorf("%s reads the skill %s by its name", designRoomRunbook, m[1])
		}
	}
}

// TestC28NoLoopRunbookGateOrStateNamesACanvas proves C28 of spec
// factorio-a4yq. A case-insensitive search of every runbook, each loop-*
// skill, the gates and the transition table finds no canvas name. The
// search reads the design-room runbook, which names the canvas skill.
func TestC28NoLoopRunbookGateOrStateNamesACanvas(t *testing.T) {
	texts := map[string]string{}
	src := pack.Source()
	if err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasPrefix(p, "runbooks/") || strings.HasPrefix(p, "skills/loop-") {
			raw, err := fs.ReadFile(src, p)
			texts["pack/"+p] = string(raw)
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"../gates", "../machine"} {
		if err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			raw, err := os.ReadFile(p)
			texts[p] = string(raw)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := texts["pack/"+designRoomRunbook]; !ok {
		t.Fatalf("the search reads no %s", designRoomRunbook)
	}
	for where, text := range texts {
		for _, name := range canvasNames {
			if strings.Contains(strings.ToLower(text), strings.ToLower(name)) {
				t.Errorf("%s names the canvas %s", where, name)
			}
		}
	}
}

// TestDocumentsSkillOrdersTheDesignRecordBetweenTheADRAndTheSpec proves the
// pack half of work entry 9 of spec factorio-a4yq. The documents skill
// places the design record and its doc review between the ADRs and the
// spec, in one paragraph.
func TestDocumentsSkillOrdersTheDesignRecordBetweenTheADRAndTheSpec(t *testing.T) {
	order := []string{"PRD", "ADR", "design record", "doc review", "`UI: yes`", "spec"}
	for _, p := range strings.Split(read(t, "skills/loop-sdlc-documents/SKILL.md"), "\n\n") {
		p, at := flat(p), -1
		for _, w := range order {
			i := strings.Index(p[at+1:], w)
			if i < 0 {
				at = -2
				break
			}
			at += 1 + i
		}
		if at >= 0 {
			return
		}
	}
	t.Errorf("skills/loop-sdlc-documents/SKILL.md holds no paragraph that names %q in that order", order)
}
