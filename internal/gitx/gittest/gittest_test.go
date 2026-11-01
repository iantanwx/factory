package gittest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := Command(args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A commit, a local push and a fetch through Command start no auto
// maintenance and no auto gc, in the repository that runs git or in the
// one that receives the push. The config of each repository asks for the
// commit-graph task at every auto maintenance, in the foreground.
func TestCommandStartsNoAutoMaintenance(t *testing.T) {
	tmp := t.TempDir()
	origin, work := filepath.Join(tmp, "origin.git"), filepath.Join(tmp, "work")
	run(t, tmp, "init", "-q", "--bare", "-b", "main", origin)
	run(t, tmp, "init", "-q", "-b", "main", work)
	run(t, work, "remote", "add", "origin", origin)
	for _, repo := range []string{origin, work} {
		for _, kv := range [][2]string{
			{"maintenance.autoDetach", "false"},
			{"maintenance.commit-graph.enabled", "true"},
			{"maintenance.commit-graph.auto", "-1"},
		} {
			run(t, repo, "config", kv[0], kv[1])
		}
	}
	trace := filepath.Join(tmp, "trace.json")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	run(t, work, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "c")
	run(t, work, "push", "-q", "origin", "main")
	run(t, work, "fetch", "-q", "origin")
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, `"event":"child_start"`) && (strings.Contains(line, `"maintenance"`) || strings.Contains(line, `"gc"`)) {
			t.Fatalf("git started auto maintenance: %s", line)
		}
	}
}

// ownHelpers are the test git helpers that turn auto maintenance off on
// their own, each with its own guard test. They keep their fixes.
var ownHelpers = map[string]string{
	"internal/gates/helpers_test.go git": "TestGitHelperStartsNoAutoMaintenance",
	"internal/server/sched_test.go gitT": "TestGitTStartsNoDetachedMaintenance",
	"tools/ci/release_test.go gitIn":     "TestGitInStartsNoAutoMaintenance",
}

// Every git that test code starts runs through Command or CommandContext,
// and an environment that a test sets on such a command passes through
// Env. Test code is a _test.go file, a file of a package whose name ends
// in "test", and the acceptance harness.
func TestEveryTestGitRunsThroughGittest(t *testing.T) {
	root := moduleRoot(t)
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch {
			case rel == "gen", rel == filepath.Join("internal", "gitx", "gittest"), d.Name() == "testdata",
				rel != "." && strings.HasPrefix(d.Name(), "."):
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		isTest := strings.HasSuffix(path, "_test.go") || strings.HasSuffix(f.Name.Name, "test") ||
			strings.HasPrefix(rel, "acceptance"+string(filepath.Separator))
		if !isTest {
			return nil
		}
		for _, p := range directGit(fset, f) {
			if ownHelpers[filepath.ToSlash(rel)+" "+p.fn] == "" {
				found = append(found, rel+": "+p.what)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Fatalf("test git that can start auto maintenance:\n%s", strings.Join(found, "\n"))
	}
}

// finding is one git in a function of a file that runs with no gittest
// environment.
type finding struct{ fn, what string }

// directGit names each call in f that runs git with no gittest
// environment.
func directGit(fset *token.FileSet, f *ast.File) []finding {
	var out []finding
	for _, decl := range f.Decls {
		fn := ""
		if d, ok := decl.(*ast.FuncDecl); ok {
			fn = d.Name.Name
		}
		report := func(n ast.Node, what string) {
			out = append(out, finding{fn, "line " + strconv.Itoa(fset.Position(n.Pos()).Line) + ": " + what})
		}
		cmds := map[string]bool{} // the variables that hold a gittest command
		ast.Inspect(decl, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CallExpr:
				arg := map[string]int{"exec.Command": 0, "exec.CommandContext": 1}
				if i, ok := arg[selector(n.Fun)]; ok && len(n.Args) > i {
					if lit, ok := n.Args[i].(*ast.BasicLit); ok && lit.Value == `"git"` {
						report(n, "runs git with "+selector(n.Fun))
					}
				}
			case *ast.AssignStmt:
				if len(n.Lhs) != len(n.Rhs) {
					return true
				}
				for i, lhs := range n.Lhs {
					call, _ := n.Rhs[i].(*ast.CallExpr)
					switch lhs := lhs.(type) {
					case *ast.Ident:
						if call != nil && (selector(call.Fun) == "gittest.Command" || selector(call.Fun) == "gittest.CommandContext") {
							cmds[lhs.Name] = true
						}
					case *ast.SelectorExpr:
						id, ok := lhs.X.(*ast.Ident)
						if ok && cmds[id.Name] && lhs.Sel.Name == "Env" && (call == nil || selector(call.Fun) != "gittest.Env") {
							report(n, "sets "+id.Name+".Env without gittest.Env")
						}
					}
				}
			}
			return true
		})
	}
	return out
}

// selector returns "pkg.Name" for a selector expression, or "".
func selector(e ast.Expr) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name + "." + sel.Sel.Name
}

// moduleRoot is the directory of go.mod above the working directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}
