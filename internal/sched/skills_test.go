package sched

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/pack"
)

// impeccablePack is the scheduler test pack with a loop-impeccable
// directory that the manifest marks conditional on the key ui.
func impeccablePack() brief.Pack {
	return brief.Pack{Version: "v1", FS: fstest.MapFS{
		"loop-implementer/SKILL.md": {Data: []byte("# runbook\n")},
		"loop-planner/SKILL.md":     {Data: []byte("# runbook\n")},
		"loop-reproducer/SKILL.md":  {Data: []byte("# runbook\n")},
		"loop-retro/SKILL.md":       {Data: []byte("# runbook\n")},
		"loop-impeccable/SKILL.md":  {Data: []byte("# impeccable\n")},
	}, Conditional: map[string]string{"loop-impeccable": "ui"}}
}

const uiConfig = "ui:\n  kind: web\n  paths: [web/**]\n"

// holds reports whether p holds the skill, and fails the test on a pack
// that does not list.
func holds(t *testing.T, p brief.Pack, skill string) bool {
	t.Helper()
	skills, err := p.Skills()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(skills, skill)
}

// filesBody is a ticket body whose "## Files" section holds the two lists.
func filesBody(may, mustNot []string) string {
	var b strings.Builder
	b.WriteString("## Change\n\nThe work.\n\n## Files\n\nMay change:\n\n")
	for _, p := range may {
		b.WriteString("- `" + p + "`\n")
	}
	b.WriteString("\nMust not change:\n\n")
	for _, p := range mustNot {
		b.WriteString("- `" + p + "`\n")
	}
	b.WriteString("\n## Done means\n\nIt works.\n")
	return b.String()
}

// C14: at the ticket level a ui glob meets the "May change" list only. A
// literal path matches by the glob, a glob entry by its directory, and a
// "Must not change" entry never counts.
func TestC14TheTicketLevelReadsMayChangeOnly(t *testing.T) {
	cfg := mustParse(t, uiConfig)
	for _, c := range []struct {
		name           string
		may, mustNot   []string
		wantImpeccable bool
	}{
		{"a literal ui path", []string{"web/src/App.tsx"}, nil, true},
		{"a ui glob", []string{"web/**"}, nil, true},
		{"a path outside ui", []string{"internal/x.go"}, nil, false},
		{"ui under must not change", []string{"internal/x.go"}, []string{"web/**"}, false},
		{"no paths", nil, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := Select(impeccablePack(), cfg, ScopeOf(machine.KindTicket, filesBody(c.may, c.mustNot)))
			if got := holds(t, p, "loop-impeccable"); got != c.wantImpeccable {
				t.Fatalf("holds loop-impeccable = %v, want %v", got, c.wantImpeccable)
			}
			if want := map[bool]string{true: "loop-impeccable"}[c.wantImpeccable]; p.Design != want {
				t.Fatalf("design = %q, want %q", p.Design, want)
			}
			if !holds(t, p, "loop-implementer") {
				t.Fatal("the selection dropped a skill that is not conditional")
			}
		})
	}
}

// C16: a second conditional skill needs one manifest line. A fixture pack
// marks loop-probe on the key health, and the same Select gives it to an
// epic run of a project that sets health and to none of a project
// without it.
func TestC16ASecondConditionalSkillNeedsOnlyAManifestLine(t *testing.T) {
	src := fstest.MapFS{
		pack.ManifestFile: {Data: []byte(`{"runbooks": {"implementer": {"skills": ["loop-probe"]}}, "skills": ["loop-probe"],
			"conditional": {"loop-probe": {"key": "health"}}}`)},
		"runbooks/implementer.md":    {Data: []byte("---\nname: loop-implementer\n---\n")},
		"skills/loop-probe/SKILL.md": {Data: []byte("---\nname: loop-probe\n---\n")},
		pack.ThirdPartyFile:          {Data: []byte(`{"skills": []}`)},
	}
	p, _, err := pack.BriefOf(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	epic := ScopeOf(machine.KindEpic, "")
	if !holds(t, Select(p, mustParse(t, "health:\n  ci_budget: 5m\n"), epic), "loop-probe") {
		t.Fatal("an epic run of a project that sets health holds no loop-probe")
	}
	if holds(t, Select(p, mustParse(t, ""), epic), "loop-probe") {
		t.Fatal("an epic run of a project without health holds loop-probe")
	}
}

func mustParse(t *testing.T, cfg string) *config.ProjectConfig {
	t.Helper()
	c, err := config.ParseProjectConfig([]byte(cfg + "merge:\n  mechanism: direct\n"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}
