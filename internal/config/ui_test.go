package config

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// TestC1AValidUIBlockLoads proves C1: a ui block of kind web or native
// loads, and its kind and paths read back as written.
func TestC1AValidUIBlockLoads(t *testing.T) {
	cases := []struct {
		file  string
		kind  UIKind
		paths []string
	}{
		{"testdata/ui-web.yaml", UIWeb, []string{"web/**"}},
		{"testdata/ui-native.yaml", UINative, []string{"ios/**", "android/app/src/**/*.kt"}},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := ParseProjectConfig(raw)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if cfg.UI == nil || cfg.UI.Kind != c.kind {
			t.Fatalf("%s: ui = %+v, want kind %s", c.file, cfg.UI, c.kind)
		}
		var got []string
		for _, g := range cfg.UI.Paths {
			got = append(got, g.String())
		}
		if !slices.Equal(got, c.paths) {
			t.Fatalf("%s: ui.paths = %q, want %q", c.file, got, c.paths)
		}
	}
	if cfg, err := ParseProjectConfig([]byte(validProjectConfig)); err != nil || cfg.UI != nil {
		t.Fatalf("config with no ui key: ui = %+v, %v; want nil", cfg.UI, err)
	}
}

// uiConfig is validProjectConfig with a ui block of the given body.
func uiConfig(body string) string { return validProjectConfig + "ui:\n" + body }

// TestC2AKindOtherThanWebOrNativeDoesNotLoad proves C2: kind desktop fails
// with ErrInvalid that names ui.kind. The load keeps the last good config,
// marks it stale and raises one project_config_invalid alert that names
// ui.kind.
func TestC2AKindOtherThanWebOrNativeDoesNotLoad(t *testing.T) {
	desktop := uiConfig("  kind: desktop\n  paths: [web/**]\n")
	if _, err := ParseProjectConfig([]byte(desktop)); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "ui.kind") {
		t.Fatalf("parse = %v, want ErrInvalid that names ui.kind", err)
	}
	ctx := context.Background()
	st := openStore(t)
	if err := StoreProjectRow(st)(ctx, Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}); err != nil {
		t.Fatal(err)
	}
	l := &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st)}
	origin := fakeOrigin{ProjectConfigPath: uiConfig("  kind: web\n  paths: [web/**]\n")}
	if got, err := l.Load(ctx, "alpha", Refs{Read: origin.read}, "main"); err != nil || got.Stale {
		t.Fatalf("first load = %+v, %v", got, err)
	}

	origin[ProjectConfigPath] = desktop
	got, err := l.Load(ctx, "alpha", Refs{Read: origin.read}, "main")
	if err != nil || !got.Stale || !errors.Is(got.Problem, ErrInvalid) || got.Config.UI == nil || got.Config.UI.Kind != UIWeb {
		t.Fatalf("load = %+v, %v; want the last good config of kind web, stale", got, err)
	}
	var bodies []string
	err = st.Read(ctx, func(tx *sql.Tx) error {
		r, err := tx.Query(`SELECT body FROM alerts WHERE project = 'alpha' AND kind = ? AND state = 'open'`, AlertProjectConfigInvalid)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		for r.Next() {
			var b string
			if err := r.Scan(&b); err != nil {
				return err
			}
			bodies = append(bodies, b)
		}
		return r.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], "ui.kind") {
		t.Fatalf("open alert bodies = %q, want one that names ui.kind", bodies)
	}
}

// TestC3AUIBlockThatBreaksItsShapeDoesNotLoad proves C3: each broken
// block fails with ErrInvalid that names its field, and no error quotes
// the refused glob.
func TestC3AUIBlockThatBreaksItsShapeDoesNotLoad(t *testing.T) {
	cases := []struct {
		name, body, field string
	}{
		{"empty paths", "  kind: web\n  paths: []\n", "ui.paths"},
		{"missing paths", "  kind: web\n", "ui.paths"},
		{"leading slash", "  kind: web\n  paths: [/web/**]\n", "ui.paths"},
		{"dot dot segment", "  kind: web\n  paths: [web/../x]\n", "ui.paths"},
		{"dot segment", "  kind: web\n  paths: [web/./x]\n", "ui.paths"},
		{"empty segment", "  kind: web\n  paths: [web//x]\n", "ui.paths"},
		{"trailing slash", "  kind: web\n  paths: [web/]\n", "ui.paths"},
		{"empty glob", "  kind: web\n  paths: [\"\"]\n", "ui.paths"},
		{"brace", "  kind: web\n  paths: [\"web/{a,b}\"]\n", "ui.paths"},
		{"bad pattern", "  kind: web\n  paths: [\"web/[\"]\n", "ui.paths"},
		{"** joined after", "  kind: web\n  paths: [\"web/**x\"]\n", "ui.paths"},
		{"** joined before", "  kind: web\n  paths: [\"web/a**\"]\n", "ui.paths"},
		{"second glob broken", "  kind: web\n  paths: [web/**, \"web//x\"]\n", "ui.paths[1]"},
		{"missing kind", "  paths: [web/**]\n", "ui.kind"},
		{"unknown key", "  kind: web\n  paths: [web/**]\n  colour: red\n", "colour"},
		{"not a mapping", "  - web/**\n", "ui"},
		{"paths not a list", "  kind: web\n  paths: web/**\n", "ui.paths"},
		{"duplicate kind", "  kind: desktop\n  kind: web\n  paths: [web/**]\n", "ui.kind"},
		{"duplicate paths", "  kind: web\n  paths: [a/**]\n  paths: [b/**]\n", "ui.paths"},
		{"key not a scalar", "  kind: web\n  paths: [web/**]\n  ? [x]\n  : y\n", "scalar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseProjectConfig([]byte(uiConfig(c.body)))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.field) {
				t.Fatalf("parse = %v, want ErrInvalid that names %s", err, c.field)
			}
			for _, glob := range []string{"/web/**", "web/../x", "web//x", "web/{a,b}", "web/[", "web/**x", "web/a**"} {
				if strings.Contains(c.body, glob) && strings.Contains(err.Error(), glob) {
					t.Fatalf("error %q quotes the glob %q", err, glob)
				}
			}
		})
	}
}

// acmePin is a skills: list with one pin named acme-design.
const acmePin = `skills:
  - name: acme-design
    repo: acme/skills
    commit: 0123456789abcdef0123456789abcdef01234567
    path: ""
    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`

// TestC21AnOverrideThatNamesNoProjectSkillDoesNotLoad proves C21: a
// design_skill in the loop- namespace, one that breaks the pin name rule,
// and one that names neither a pin nor a tracked skill root entry keep the
// last good config in force, and the problem names ui.design_skill. The
// override loads when it names a pin, and when a skill root tracks it.
func TestC21AnOverrideThatNamesNoProjectSkillDoesNotLoad(t *testing.T) {
	override := func(name string) string {
		return uiConfig("  kind: web\n  paths: [web/**]\n  design_skill: " + name + "\n")
	}
	for _, c := range []struct {
		name    string
		config  string
		tracked string
		loads   bool
	}{
		{"loop namespace", override("loop-x"), ".claude/skills/loop-x/SKILL.md", false},
		{"breaks the pin name rule", override("Acme"), ".claude/skills/Acme/SKILL.md", false},
		{"no pin and no tracked entry", override("acme-design"), ".claude/skills/acme/SKILL.md", false},
		{"a pin", override("acme-design") + acmePin, "README.md", true},
		{"tracked under .claude/skills", override("acme-design"), ".claude/skills/acme-design/SKILL.md", true},
		{"tracked under .agents/skills", override("acme-design"), ".agents/skills/acme-design/SKILL.md", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			if err := StoreProjectRow(st)(ctx, Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}); err != nil {
				t.Fatal(err)
			}
			l := &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st)}
			origin := fakeOrigin{ProjectConfigPath: uiConfig("  kind: web\n  paths: [web/**]\n")}
			refs := Refs{Read: origin.read, Tree: fakeTree{"README.md", c.tracked}.list}
			if _, err := l.Load(ctx, "alpha", refs, "main"); err != nil {
				t.Fatal(err)
			}

			origin[ProjectConfigPath] = c.config
			got, err := l.Load(ctx, "alpha", refs, "main")
			if err != nil {
				t.Fatal(err)
			}
			if c.loads {
				if got.Stale || got.Config.UI.DesignSkill != "acme-design" {
					t.Fatalf("load = %+v, problem %v; want design_skill acme-design in force", got.Config.UI, got.Problem)
				}
				return
			}
			if !got.Stale || !errors.Is(got.Problem, ErrInvalid) || !strings.Contains(got.Problem.Error(), "ui.design_skill") {
				t.Fatalf("load stale %v, problem %v; want the last good config and a problem that names ui.design_skill", got.Stale, got.Problem)
			}
			if got.Config.UI.DesignSkill != "" {
				t.Fatalf("design_skill %q in force, want the last good config's none", got.Config.UI.DesignSkill)
			}
		})
	}
}
