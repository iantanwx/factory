package qa

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// skill is the SKILL.md of one verify skill, with paths in its front
// matter when any are given.
func skill(name string, paths ...string) *fstest.MapFile {
	text := "---\nname: verify-" + name + "\ndescription: drive " + name + "\n"
	if len(paths) > 0 {
		text += "paths:\n"
		for _, p := range paths {
			text += "  - " + p + "\n"
		}
	}
	return &fstest.MapFile{Data: []byte(text + "---\n\n# Launch\n")}
}

// webAndAPI is the fixture tree of C3: verify-web over apps/web/** and
// verify-api over apps/api/**, next to a loop skill that is no verify skill.
func webAndAPI() fstest.MapFS {
	return fstest.MapFS{
		".claude/skills/verify-web/SKILL.md":     skill("web", "apps/web/**"),
		".claude/skills/verify-api/SKILL.md":     skill("api", "apps/api/**"),
		".claude/skills/loop-sdlc-turn/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: loop-sdlc-turn\n---\n")},
	}
}

func drive(t *testing.T, tree fstest.MapFS, mayChange ...string) []string {
	t.Helper()
	apps, err := Apps(tree)
	if err != nil {
		t.Fatal(err)
	}
	return MakePlan(apps, mayChange, "").Drive
}

// C3: the plan drives each app whose paths meet a "May change" entry, or
// every app when none meets one.
func TestC3PlanDrivesEachAppThatReachesTheTicket(t *testing.T) {
	ops := webAndAPI()
	ops[".claude/skills/verify-ops/SKILL.md"] = skill("ops")
	cases := []struct {
		name      string
		tree      fstest.MapFS
		mayChange []string
		want      []string
	}{
		{"a path under apps/web", webAndAPI(), []string{"apps/web/src/a.ts"}, []string{"web"}},
		{"a glob under apps/api", webAndAPI(), []string{"apps/api/**"}, []string{"api"}},
		{"both apps", webAndAPI(), []string{"apps/web/a.ts", "apps/api/b.go"}, []string{"api", "web"}},
		{"no app reaches docs/x.md", webAndAPI(), []string{"docs/x.md"}, []string{"api", "web"}},
		{"no May change entry", webAndAPI(), nil, []string{"api", "web"}},
		{"a skill with no paths reaches docs/x.md", ops, []string{"docs/x.md"}, []string{"ops"}},
		{"a skill with no paths joins apps/web", ops, []string{"apps/web/src/a.ts"}, []string{"ops", "web"}},
		{"no verify skill", fstest.MapFS{"README.md": {Data: []byte("x\n")}}, []string{"apps/web/a.ts"}, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := drive(t, c.tree, c.mayChange...); !slices.Equal(got, c.want) {
				t.Fatalf("Drive = %q, want %q", got, c.want)
			}
		})
	}
}

// C3: Apps reads the name from the directory, the skill's path and its globs.
func TestC3AppsReadsEachVerifySkill(t *testing.T) {
	apps, err := Apps(webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range apps {
		var paths []string
		for _, g := range a.Paths {
			paths = append(paths, g.String())
		}
		got = append(got, a.Name+" "+a.Skill+" "+strings.Join(paths, ","))
	}
	want := []string{
		"api .claude/skills/verify-api/SKILL.md apps/api/**",
		"web .claude/skills/verify-web/SKILL.md apps/web/**",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Apps = %q, want %q", got, want)
	}
}

// C3: a bad name, two skills with one name, or a bad glob make Apps fail
// with an error that names the path.
func TestC3AppsRefusesABadVerifySkill(t *testing.T) {
	cases := []struct {
		name string
		tree fstest.MapFS
		path string
	}{
		{"an upper-case name", fstest.MapFS{".claude/skills/verify-Web/SKILL.md": skill("Web")},
			".claude/skills/verify-Web/SKILL.md"},
		{"two skills with one name", fstest.MapFS{
			".claude/skills/verify-web/SKILL.md":  skill("web"),
			".claude/skills/verify-web2/SKILL.md": skill("web"),
		}, ".claude/skills/verify-web2/SKILL.md"},
		{"a glob that starts with /", fstest.MapFS{".claude/skills/verify-web/SKILL.md": skill("web", "/apps/web/**")},
			".claude/skills/verify-web/SKILL.md"},
		{"paths that are no list", fstest.MapFS{".claude/skills/verify-web/SKILL.md": {Data: []byte("---\nname: verify-web\npaths: 3\n---\n")}},
			".claude/skills/verify-web/SKILL.md"},
		{"no front matter", fstest.MapFS{".claude/skills/verify-web/SKILL.md": {Data: []byte("# Launch\n")}},
			".claude/skills/verify-web/SKILL.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Apps(c.tree)
			if err == nil || !strings.Contains(err.Error(), c.path) {
				t.Fatalf("Apps error = %v, want one that names %s", err, c.path)
			}
		})
	}
}

// C3: visual QA runs exactly when the run holds a design skill.
func TestC3PlanRunsVisualQAExactlyWithADesignSkill(t *testing.T) {
	apps, err := Apps(webAndAPI())
	if err != nil {
		t.Fatal(err)
	}
	for design, want := range map[string]bool{"": false, "loop-impeccable": true} {
		p := MakePlan(apps, []string{"apps/web/a.ts"}, design)
		if p.Visual != want || p.DesignSkill != design {
			t.Fatalf("design %q: Visual %v, DesignSkill %q, want %v and %q", design, p.Visual, p.DesignSkill, want, design)
		}
	}
}

// C4: the plan carries the one value of the bounds, and a change to a
// plan's copy leaves that value as it is.
func TestC4PlanCarriesTheBounds(t *testing.T) {
	p := MakePlan(nil, nil, "")
	p.Bounds.Widths[0].W = 1
	want := Bounds{Steps: 25, Variations: 3, VariationSteps: 10, Minutes: 20, Screens: 8,
		Widths: []Viewport{{1280, 800}, {390, 844}}}
	got := MakePlan(nil, nil, "").Bounds
	if got.Steps != want.Steps || got.Variations != want.Variations || got.VariationSteps != want.VariationSteps ||
		got.Minutes != want.Minutes || got.Screens != want.Screens || !slices.Equal(got.Widths, want.Widths) {
		t.Fatalf("Bounds = %+v, want %+v", got, want)
	}
}
