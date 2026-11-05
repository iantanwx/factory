package overwatch

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/frontend"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/gitx/gittest"
)

// memTree is a tracked tree held in memory: each path and its bytes.
type memTree map[string]string

func (m memTree) Paths() []string { return slices.Sorted(maps.Keys(m)) }

func (m memTree) Read(p string) ([]byte, error) {
	b, ok := m[p]
	if !ok {
		return nil, errors.New("not tracked")
	}
	return []byte(b), nil
}

// uiWorld is fakeWorld with the project configs and the tracked trees a
// test sets. A project in forbid fails the test when its tree is read.
type uiWorld struct {
	*fakeWorld
	t       *testing.T
	configs map[string]*config.ProjectSnapshot
	trees   map[string]frontend.Tree
	treeErr map[string]error
	forbid  map[string]bool
}

func newUIWorld(t *testing.T, projects ...string) *uiWorld {
	t.Helper()
	fw := newFakeWorld(t)
	fw.projects = nil
	for _, id := range projects {
		fw.projects = append(fw.projects, config.Project{ID: id, Repo: "acme/" + id})
	}
	return &uiWorld{fakeWorld: fw, t: t, configs: map[string]*config.ProjectSnapshot{}, trees: map[string]frontend.Tree{},
		treeErr: map[string]error{}, forbid: map[string]bool{}}
}

func (w *uiWorld) ProjectConfig(_ context.Context, project string) (*config.ProjectSnapshot, error) {
	s, ok := w.configs[project]
	if !ok {
		return nil, fmt.Errorf("no config of %s: %w", project, errFake)
	}
	return s, nil
}

func (w *uiWorld) Tracked(_ context.Context, project string) (frontend.Tree, error) {
	if w.forbid[project] {
		w.t.Errorf("the ui part read the tree of %s, which declares ui", project)
	}
	if err := w.treeErr[project]; err != nil {
		return nil, err
	}
	return w.trees[project], nil
}

// noUI gives project a config with no ui key and tree as its tracked files.
func (w *uiWorld) noUI(project string, tree memTree) {
	w.configs[project] = &config.ProjectSnapshot{Config: &config.ProjectConfig{}}
	w.trees[project] = tree
}

// C26: a project whose default branch tracks web/App.tsx and whose last
// good config holds no ui key is one finding, overwatch:ui:<project>, and
// its line names the signal's path. A project with no signal is quiet.
func TestC26TheSweepReportsAFrontendWithNoDeclaration(t *testing.T) {
	w := newUIWorld(t, "alpha", "beta")
	w.noUI("alpha", memTree{"go.mod": "module x\n", "web/App.tsx": "export {}\n"})
	w.noUI("beta", memTree{"go.mod": "module y\n", "main.go": "package main\n"})
	pr := sweepOne(t, w, partNamed(t, "ui"))
	expect(t, pr, Work, "web/App.tsx", "overwatch:ui:alpha")
	if got := unfiled(pr, "overwatch:ui:alpha"); got != 1 {
		t.Fatalf("%d unfiled lines quote overwatch:ui:alpha, want 1: %v", got, pr.Lines)
	}
	if slices.ContainsFunc(pr.Lines, func(l string) bool { return strings.Contains(l, "beta") }) {
		t.Fatalf("a line names beta, which shows no frontend: %v", pr.Lines)
	}
}

// C26: a failed tree read is one UNREAD note for that project, and the
// part has code 2. The other project's finding stands.
func TestC26AFailedTreeReadCostsItsProjectOnly(t *testing.T) {
	w := newUIWorld(t, "alpha", "beta")
	w.noUI("alpha", nil)
	w.treeErr["alpha"] = errors.New("ls-tree: origin/main is no commit")
	w.noUI("beta", memTree{"apps/web/package.json": `{"dependencies": {"react": "18.0.0"}}`})
	pr := sweepOne(t, w, partNamed(t, "ui"))
	expect(t, pr, Unread, "apps/web/package.json", "overwatch:ui:beta")
	var unread []string
	for _, l := range pr.Lines {
		if strings.HasPrefix(l, "UNREAD") {
			unread = append(unread, l)
		}
	}
	if len(unread) != 1 || !strings.Contains(unread[0], "alpha") || !strings.Contains(unread[0], "origin/main is no commit") {
		t.Fatalf("UNREAD lines = %q, want one that names alpha and the failure", unread)
	}
}

// C27: a project whose config declares ui yields no finding and no note,
// whatever its tree holds, and its tree is not read.
func TestC27TheSweepSaysNothingAboutADeclaredUI(t *testing.T) {
	g, err := config.ParseGlob("web/**")
	if err != nil {
		t.Fatal(err)
	}
	w := newUIWorld(t, "alpha")
	w.configs["alpha"] = &config.ProjectSnapshot{Config: &config.ProjectConfig{UI: &config.UIConfig{Kind: config.UIWeb, Paths: []config.Glob{g}}}}
	w.trees["alpha"] = memTree{"web/App.tsx": "export {}\n", "ios/App/AppDelegate.swift": "import UIKit\n"}
	w.forbid["alpha"] = true
	pr := sweepOne(t, w, partNamed(t, "ui"))
	if pr.Code != Quiet || len(pr.Lines) != 0 {
		t.Fatalf("code %d, lines %q; want code 0 and no line", pr.Code, pr.Lines)
	}
}

// git runs git in dir with a fixed author, and fails the test on error.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gittest.Command(append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return string(out)
}

// gitWorld is fakeWorld over one project with a real clone: its config is
// the clone's .loop/config.yaml at origin/main, and its tree is the
// clone's tree at origin/main, as the server reads them.
type gitWorld struct {
	*fakeWorld
	clone string
}

func (w *gitWorld) ProjectConfig(ctx context.Context, _ string) (*config.ProjectSnapshot, error) {
	raw, err := gitx.Run(ctx, w.clone, "show", "origin/main:.loop/config.yaml")
	if err != nil {
		return nil, err
	}
	cfg, err := config.ParseProjectConfig(raw)
	if err != nil {
		return nil, err
	}
	return &config.ProjectSnapshot{Config: cfg, ConfigRaw: raw, Branch: "main"}, nil
}

func (w *gitWorld) Tracked(ctx context.Context, _ string) (frontend.Tree, error) {
	return frontend.GitTree(ctx, w.clone, "origin/main")
}

// C28: after a sweep that reports overwatch:ui:<project>, the bytes of
// .loop/config.yaml at the default branch and in the clone are the same,
// and so are the refs and the status of both repositories.
func TestC28TheSweepNeverWritesTheBlock(t *testing.T) {
	root := t.TempDir()
	origin, clone := filepath.Join(root, "origin.git"), filepath.Join(root, "alpha")
	git(t, root, "init", "--bare", "-b", "main", origin)
	git(t, root, "clone", origin, clone)
	for path, body := range map[string]string{".loop/config.yaml": "merge:\n  mechanism: mergify\n", "web/App.tsx": "export {}\n"} {
		if err := os.MkdirAll(filepath.Join(clone, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(clone, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, clone, "add", ".")
	git(t, clone, "commit", "-m", "init")
	git(t, clone, "push", "origin", "HEAD:main")
	git(t, clone, "fetch", "origin")

	state := func() string {
		file, err := os.ReadFile(filepath.Join(clone, ".loop/config.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		parts := []string{string(file), git(t, origin, "show", "main:.loop/config.yaml"), git(t, clone, "show", "origin/main:.loop/config.yaml"),
			git(t, origin, "for-each-ref"), git(t, clone, "for-each-ref"), git(t, clone, "status", "--porcelain", "--ignored")}
		return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "\x00"))))
	}
	before := state()

	fw := newFakeWorld(t)
	fw.projects = []config.Project{{ID: "alpha", Repo: "acme/alpha", Path: clone}}
	pr := sweepOne(t, &gitWorld{fakeWorld: fw, clone: clone}, partNamed(t, "ui"))
	expect(t, pr, Work, "overwatch:ui:alpha", "web/App.tsx")
	if after := state(); after != before {
		t.Fatal("the sweep changed the config, a ref or the status of the origin or the clone")
	}
}

// C28: World holds reads only. Each method a part may call is one of these
// reads, so a method that writes a file or a ref fails here until a person
// reviews it.
func TestC28WorldHoldsNoMethodThatWrites(t *testing.T) {
	want := []string{"Body", "CheckoutDirs", "Coordinators", "Dependencies", "Doctor", "Drains", "GitHub", "Host", "Inbox", "Intervals",
		"Inventory", "Items", "Jobs", "LastRings", "Now", "Pauses", "ProjectConfig", "Projects", "Store", "Tracked", "WorkQuery"}
	typ := reflect.TypeFor[World]()
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("World holds %q, want the reads %q", got, want)
	}
	tree := reflect.TypeFor[frontend.Tree]()
	for i := range tree.NumMethod() {
		if m := tree.Method(i).Name; m != "Paths" && m != "Read" {
			t.Fatalf("frontend.Tree holds %s, which is no read", m)
		}
	}
	query := reflect.TypeFor[WorkQuery]()
	for i := range query.NumMethod() {
		if m := query.Method(i).Name; m != "Admissible" && m != "FilesWait" && m != "Peek" && m != "Room" {
			t.Fatalf("WorkQuery holds %s, which is no read", m)
		}
	}
}
