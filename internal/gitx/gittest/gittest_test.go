package gittest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// AutoMaintenance names each auto maintenance that a traced git started,
// and the git that started it. A git with Config in force starts none.
func TestAutoMaintenanceNamesTheGitThatStartedIt(t *testing.T) {
	tmp := t.TempDir()
	work, trace := filepath.Join(tmp, "work"), filepath.Join(tmp, "trace")
	run(t, tmp, "init", "-q", "-b", "main", work)
	run(t, work, "config", "maintenance.autoDetach", "false")
	if err := os.Mkdir(trace, 0o700); err != nil {
		t.Fatal(err)
	}
	commit := []string{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "c"}
	cmd := exec.Command("git", commit...)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TRACE2_EVENT="+trace)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	t.Setenv("GIT_TRACE2_EVENT", trace)
	run(t, work, commit...)
	starts, err := AutoMaintenance(trace)
	if err != nil {
		t.Fatal(err)
	}
	want := "git " + strings.Join(commit, " ") + " started git maintenance run --auto"
	if len(starts) != 1 || !strings.HasPrefix(starts[0], want) {
		t.Fatalf("starts = %q, want one that starts with %q", starts, want)
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
// in "test", and the acceptance harness. A package whose tests import
// gitx, gittest or githubtest has a TestMain that calls Main, unless it
// holds one of ownHelpers.
func TestEveryTestGitRunsThroughGittest(t *testing.T) {
	root := moduleRoot(t)
	var found []string
	runsGit, callsMain := map[string]bool{}, map[string]bool{}
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
		if pkg := filepath.ToSlash(filepath.Dir(rel)); strings.HasSuffix(path, "_test.go") {
			runsGit[pkg] = runsGit[pkg] || importsGit(f)
			callsMain[pkg] = callsMain[pkg] || testMainCallsMain(f)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range ownHelpers {
		delete(runsGit, filepath.ToSlash(filepath.Dir(strings.Fields(key)[0])))
	}
	for _, pkg := range slices.Sorted(maps.Keys(runsGit)) {
		if runsGit[pkg] && !callsMain[pkg] {
			found = append(found, pkg+": its tests run git with no TestMain that calls gittest.Main")
		}
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

// importsGit reports whether f imports a package that runs git.
func importsGit(f *ast.File) bool {
	for _, imp := range f.Imports {
		switch strings.Trim(imp.Path.Value, `"`) {
		case "github.com/iantanwx/loop/internal/gitx", "github.com/iantanwx/loop/internal/gitx/gittest",
			"github.com/iantanwx/loop/internal/github/githubtest":
			return true
		}
	}
	return false
}

// testMainCallsMain reports whether f holds a TestMain that calls
// gittest.Main.
func testMainCallsMain(f *ast.File) bool {
	calls := false
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "TestMain" {
			ast.Inspect(d, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok && selector(c.Fun) == "gittest.Main" {
					calls = true
				}
				return true
			})
		}
	}
	return calls
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
