package userspace

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
)

// neonTree is a skill tree whose SKILL.md names neon, with one
// executable file.
func neonTree() fstest.MapFS {
	return fstest.MapFS{
		"SKILL.md":          {Data: []byte("---\nname: neon\ndescription: Neon.\n---\n\nUse Neon.\n"), Mode: 0o644},
		"scripts/check.sh":  {Data: []byte("#!/bin/sh\necho ok\n"), Mode: 0o755},
		"references/api.md": {Data: []byte("# API\n"), Mode: 0o644},
	}
}

// pinOf is the pin named name whose sha256 is tree's hash.
func pinOf(t *testing.T, name string, tree fs.FS) config.SkillPin {
	t.Helper()
	sum, err := pack.TreeHash(tree)
	if err != nil {
		t.Fatal(err)
	}
	return config.SkillPin{Name: name, Repo: "neondatabase/agent-skills",
		Commit: "ccca3334a7542d3133134cc6926f2fafc6edeb1f", Path: "skills/" + name, SHA256: sum}
}

// pinDir is where the store keeps pin of project.
func pinDir(st Store, project string, pin config.SkillPin) string {
	return filepath.Join(st.Dir, project, pin.Name, pin.SHA256)
}

// wantTree fails t unless got holds exactly the files and modes of want.
func wantTree(t *testing.T, got fs.FS, want fstest.MapFS) {
	t.Helper()
	seen := map[string]bool{}
	err := fs.WalkDir(got, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		seen[name] = true
		w, ok := want[name]
		if !ok {
			t.Errorf("the tree holds %s, which the pin lacks", name)
			return nil
		}
		data, err := fs.ReadFile(got, name)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if string(data) != string(w.Data) || info.Mode().Perm() != w.Mode {
			t.Errorf("%s = %q %v, want %q %v", name, data, info.Mode().Perm(), w.Data, w.Mode)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("the tree lacks %s", name)
		}
	}
}

// C10: the store holds only a pin's own tree. A Put of the pin's tree
// lands at <dir>/<project>/<name>/<sha256>/ with every byte and mode, and
// Held returns that tree. A second Store over the same Dir reads it too.
func TestC10StoreHoldsThePutTree(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	tree := neonTree()
	pin := pinOf(t, "neon", tree)
	if _, ok := st.Held("p1", pin); ok {
		t.Fatal("Held = true before any Put")
	}
	if err := st.Put("p1", pin, tree); err != nil {
		t.Fatal(err)
	}
	wantTree(t, os.DirFS(pinDir(st, "p1", pin)), tree)
	h, ok := Store{Dir: st.Dir}.Held("p1", pin)
	if !ok || h.Pin != pin {
		t.Fatalf("Held = %+v, %v; want the pin", h.Pin, ok)
	}
	wantTree(t, h.Tree, tree)
	if _, ok := st.Held("p2", pin); ok {
		t.Fatal("Held(p2) = true: the pin is p1's")
	}
	if got := st.HeldSet("p1", []config.SkillPin{pinOf(t, "neon-postgres", tree), pin}); len(got) != 1 || got[0].Pin != pin {
		t.Fatalf("HeldSet = %+v, want neon only", got)
	}
}

// C10: Held refuses a stored tree that differs from the pin in one byte
// or one exec bit, or whose SKILL.md does not name the pin.
func TestC10StoreRefusesAChangedTree(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"one byte": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "references", "api.md"), []byte("# APi\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"one exec bit": func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, "scripts", "check.sh"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"missing": func(t *testing.T, dir string) {
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			st := Store{Dir: t.TempDir()}
			pin := pinOf(t, "neon", neonTree())
			if err := st.Put("p1", pin, neonTree()); err != nil {
				t.Fatal(err)
			}
			change(t, pinDir(st, "p1", pin))
			if _, ok := st.Held("p1", pin); ok {
				t.Fatal("Held = true for a changed tree")
			}
		})
	}
	t.Run("SKILL.md names another skill", func(t *testing.T) {
		st := Store{Dir: t.TempDir()}
		tree := neonTreeNamed("other")
		pin := pinOf(t, "neon", tree)
		// The tree matches the pin's hash, so only its name refuses it.
		writeDir(t, pinDir(st, "p1", pin), tree)
		if _, ok := st.Held("p1", pin); ok {
			t.Fatal("Held = true for a SKILL.md that names other")
		}
	})
}

// writeDir writes each file of tree under dir, at its mode.
func writeDir(t *testing.T, dir string, tree fstest.MapFS) {
	t.Helper()
	for name, f := range tree {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.Data, f.Mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, f.Mode); err != nil {
			t.Fatal(err)
		}
	}
}

// neonTreeNamed is neonTree with a SKILL.md that names name.
func neonTreeNamed(name string) fstest.MapFS {
	tree := neonTree()
	tree["SKILL.md"] = &fstest.MapFile{Data: []byte("---\nname: " + name + "\n---\n"), Mode: 0o644}
	return tree
}

// C10: a Put of a tree that fails the pin's hash or name writes nothing.
func TestC10StorePutRefusesAnotherTree(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	pin := pinOf(t, "neon", neonTree())
	changed := neonTree()
	changed["references/api.md"] = &fstest.MapFile{Data: []byte("# APi\n"), Mode: 0o644}
	if err := st.Put("p1", pin, changed); err == nil {
		t.Fatal("Put of a tree with another hash = nil")
	}
	other := neonTreeNamed("other")
	if err := st.Put("p1", pinOf(t, "neon", other), other); err == nil {
		t.Fatal("Put of a SKILL.md that names other = nil")
	}
	entries, err := os.ReadDir(st.Dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the store holds %v after two refused Puts, want nothing", entries)
	}
}

// C10: two writers put one pin at once, and the path holds the whole
// tree. A Put over a changed tree replaces it.
func TestC10StorePutIsWholeUnderTwoWriters(t *testing.T) {
	st := Store{Dir: t.TempDir()}
	tree := neonTree()
	pin := pinOf(t, "neon", tree)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { errs[i] = Store{Dir: st.Dir}.Put("p1", pin, tree) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	wantTree(t, os.DirFS(pinDir(st, "p1", pin)), tree)
	if _, ok := st.Held("p1", pin); !ok {
		t.Fatal("Held = false after two writers")
	}

	if err := os.WriteFile(filepath.Join(pinDir(st, "p1", pin), "references", "api.md"), []byte("# APi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Put("p1", pin, tree); err != nil {
		t.Fatal(err)
	}
	wantTree(t, os.DirFS(pinDir(st, "p1", pin)), tree)
	names, err := os.ReadDir(filepath.Dir(pinDir(st, "p1", pin)))
	if err != nil || len(names) != 1 {
		t.Fatalf("the pin's directory holds %v, %v; want its one tree", names, err)
	}
}
