package config

import (
	"context"
	"database/sql"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/store"
)

// webUI is a ui block that loads.
const webUI = "ui:\n  kind: web\n  paths: [web/**]\n"

// designConfig is validProjectConfig with a design block of the given body.
func designConfig(body string) string { return validProjectConfig + "design:\n" + body }

// TestC7DeclaresUIIsTrueOnlyForAConfigWithAUIKey proves C7: DeclaresUI is
// true for a config that holds a ui key, and false for one that holds none.
func TestC7DeclaresUIIsTrueOnlyForAConfigWithAUIKey(t *testing.T) {
	for _, c := range []struct {
		name, config string
		want         bool
	}{
		{"ui key", validProjectConfig + webUI, true},
		{"ui key and design block", designConfig("  run: make serve\n  capture: make shot\n") + webUI, true},
		{"no ui key", validProjectConfig, false},
	} {
		cfg, err := ParseProjectConfig([]byte(c.config))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := cfg.DeclaresUI(); got != c.want {
			t.Fatalf("%s: DeclaresUI = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestC7ADesignBlockWithNoUIKeyDoesNotLoad proves C7: a config with a
// design block and no ui key does not load, the project_config_invalid
// alert names rule design_without_ui, and the last good config stays in
// force.
func TestC7ADesignBlockWithNoUIKeyDoesNotLoad(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	if err := StoreProjectRow(st)(ctx, Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}); err != nil {
		t.Fatal(err)
	}
	l := &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st)}
	origin := fakeOrigin{ProjectConfigPath: validProjectConfig}
	if got, err := l.Load(ctx, "alpha", Refs{Read: origin.read}, "main"); err != nil || got.Stale {
		t.Fatalf("first load = %+v, %v", got, err)
	}

	origin[ProjectConfigPath] = designConfig("  run: make serve\n  capture: make shot\n")
	got, err := l.Load(ctx, "alpha", Refs{Read: origin.read}, "main")
	if err != nil || !got.Stale || !errors.Is(got.Problem, ErrInvalid) {
		t.Fatalf("load = %+v, %v; want the last good config, stale", got, err)
	}
	if got.Config.Design != nil || got.Config.DeclaresUI() {
		t.Fatalf("config in force: design %+v, DeclaresUI %v; want the last good config", got.Config.Design, got.Config.DeclaresUI())
	}
	if body := openConfigAlert(t, st); !strings.Contains(body, "design_without_ui") {
		t.Fatalf("alert body = %q, want one that names design_without_ui", body)
	}
}

// openConfigAlert returns the body of alpha's one open
// project_config_invalid alert.
func openConfigAlert(t *testing.T, st *store.Store) string {
	t.Helper()
	var body string
	err := st.Read(context.Background(), func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT body FROM alerts WHERE project = 'alpha' AND kind = ? AND state = 'open'`, AlertProjectConfigInvalid).Scan(&body)
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// uiBlockNames are the identifiers that read the ui block: its field on
// ProjectConfig, and its type and kinds.
var uiBlockNames = map[string]bool{"UI": true, "UIConfig": true, "UIKind": true, "UIWeb": true, "UINative": true}

// uiReads lists each read of the ui block in the non-test Go files of dir.
func uiReads(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok && uiBlockNames[s.Sel.Name] {
				out = append(out, fset.Position(s.Pos()).String()+": "+s.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestC7OneReaderDecidesWhetherAProjectDeclaresAUI proves C7: config holds
// DeclaresUI, and internal/gates, internal/github and internal/room read no
// field of the ui block. The scan finds a read in a file that holds one.
func TestC7OneReaderDecidesWhetherAProjectDeclaresAUI(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "design.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var reader bool
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "DeclaresUI" && fn.Recv != nil {
			reader = true
		}
	}
	if !reader {
		t.Fatal("internal/config/design.go declares no DeclaresUI method")
	}
	if reads := uiReads(t, "../sched"); len(reads) == 0 {
		t.Fatal("the scan finds no read of the ui block in internal/sched, which reads ui.design_skill")
	}
	for _, dir := range []string{"../gates", "../github", "../room"} {
		if reads := uiReads(t, dir); len(reads) > 0 {
			t.Errorf("%s reads the ui block, and must read DeclaresUI only: %q", dir, reads)
		}
	}
}

// stubCanvasPin is a skills: list with one pin named zz-stub-canvas.
const stubCanvasPin = `skills:
  - name: zz-stub-canvas
    repo: acme/skills
    commit: 0123456789abcdef0123456789abcdef01234567
    path: ""
    sha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
`

// TestC8ADesignBlockWithNoCommandDoesNotLoad proves C8: a design block
// with an empty or absent run or capture does not load, and the refusal
// names the key.
func TestC8ADesignBlockWithNoCommandDoesNotLoad(t *testing.T) {
	for _, c := range []struct {
		name, body, key string
	}{
		{"empty run", "  run: \"\"\n  capture: make shot\n", "design.run"},
		{"blank run", "  run: \"  \"\n  capture: make shot\n", "design.run"},
		{"no run", "  capture: make shot\n", "design.run"},
		{"empty capture", "  run: make serve\n  capture: \"\"\n", "design.capture"},
		{"no capture", "  run: make serve\n", "design.capture"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseProjectConfig([]byte(designConfig(c.body) + webUI))
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.key) {
				t.Fatalf("parse = %v, want ErrInvalid that names %s", err, c.key)
			}
		})
	}
}

// TestC8ACanvasSkillThatNamesNoSkillDoesNotLoad proves C8: a design.skill
// that names neither a kernel skill nor a pin of skills: keeps the last
// good config in force, and the problem names the skill. A pinned
// zz-stub-canvas loads, and so does a kernel skill.
func TestC8ACanvasSkillThatNamesNoSkillDoesNotLoad(t *testing.T) {
	skill := func(name string) string {
		return designConfig("  run: make serve\n  capture: make shot\n  skill: "+name+"\n") + webUI
	}
	for _, c := range []struct {
		name, config, skill string
		loads               bool
	}{
		{"pinned stub", skill("zz-stub-canvas") + stubCanvasPin, "zz-stub-canvas", true},
		{"kernel skill", skill("loop-impeccable"), "loop-impeccable", true},
		{"no pin", skill("zz-stub-canvas"), "zz-stub-canvas", false},
		{"another pin", skill("zz-other-canvas") + stubCanvasPin, "zz-other-canvas", false},
		{"no kernel skill", skill("loop-canvas"), "loop-canvas", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st := openStore(t)
			if err := StoreProjectRow(st)(ctx, Project{ID: "alpha", Repo: "iantanwx/alpha", Path: "/srv/a"}); err != nil {
				t.Fatal(err)
			}
			kernel := func() ([]string, error) { return []string{"loop-impeccable", "loop-implementer"}, nil }
			l := &Loader{LKG: StoreLKG(st), Alerts: StoreAlerter(st), Kernel: kernel}
			origin := fakeOrigin{ProjectConfigPath: validProjectConfig}
			refs := Refs{Read: origin.read, Tree: fakeTree{"README.md"}.list}
			if _, err := l.Load(ctx, "alpha", refs, "main"); err != nil {
				t.Fatal(err)
			}

			origin[ProjectConfigPath] = c.config
			got, err := l.Load(ctx, "alpha", refs, "main")
			if err != nil {
				t.Fatal(err)
			}
			if c.loads {
				if got.Stale || got.Config.Design == nil || got.Config.Design.Skill != c.skill {
					t.Fatalf("load = %+v, problem %v; want design.skill %s in force", got.Config.Design, got.Problem, c.skill)
				}
				return
			}
			if !got.Stale || got.Config.Design != nil {
				t.Fatalf("load stale %v, design %+v; want the last good config", got.Stale, got.Config.Design)
			}
			if body := openConfigAlert(t, st); !strings.Contains(body, "design.skill") || !strings.Contains(body, c.skill) {
				t.Fatalf("alert body = %q, want one that names design.skill and %s", body, c.skill)
			}
		})
	}
}
