// Package userspace keeps each project's userspace skill pins on the host,
// and fetches each pin that a project's config names and the host lacks
// (ADR 0013 decision 2).
package userspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/pack"
)

// Store keeps each verified pin tree at <Dir>/<project>/<name>/<sha256>/.
// The hash in the path makes a changed pin a new directory, so an old tree
// never serves under a new pin. Nothing sweeps old directories: the
// operator can delete any of them, and the next config read fetches each
// pin the store then lacks.
type Store struct {
	// Dir is <state_dir>/userspace-skills. It sits outside versions/<v>/,
	// so an upgrade keeps it.
	Dir string
}

// Held is a pin and its tree, read from the store. Store.Held is its one
// constructor, so the tree matches the pin's hash and its SKILL.md names
// the pin.
type Held struct {
	Pin  config.SkillPin
	Tree fs.FS
}

// path is where the store keeps pin of project. A project or pin that
// cannot name one directory has no path.
func (s Store) path(project string, pin config.SkillPin) (string, bool) {
	if !fs.ValidPath(project) || strings.Contains(project, "/") || project == "." || pin.Check() != nil {
		return "", false
	}
	return filepath.Join(s.Dir, project, pin.Name, pin.SHA256), true
}

// Held reads pin of project. It passes only when the stored tree hashes to
// the pin's sha256 and its SKILL.md holds the line name: <name>, so a tree
// that changed on disk reads as not held.
func (s Store) Held(project string, pin config.SkillPin) (Held, bool) {
	dir, ok := s.path(project, pin)
	if !ok {
		return Held{}, false
	}
	tree := os.DirFS(dir)
	if verify(pin, tree) != nil {
		return Held{}, false
	}
	return Held{Pin: pin, Tree: tree}, true
}

// HeldSet returns the held pins of pins, in their order. It never fetches.
func (s Store) HeldSet(project string, pins []config.SkillPin) []Held {
	var out []Held
	for _, pin := range pins {
		if h, ok := s.Held(project, pin); ok {
			out = append(out, h)
		}
	}
	return out
}

// Put stores tree as pin of project. It refuses a tree that fails the
// pin's hash or name, and writes nothing then. It writes the tree beside
// its path and renames it into place, so a reader sees the whole tree or
// none, also when the server and loop register put one pin at once.
func (s Store) Put(project string, pin config.SkillPin, tree fs.FS) (err error) {
	dir, ok := s.path(project, pin)
	if !ok {
		return fmt.Errorf("userspace: project %q or pin %q names no store directory", project, pin.Name)
	}
	if err := verify(pin, tree); err != nil {
		return err
	}
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := writeTree(tree, tmp); err != nil {
		return fmt.Errorf("userspace: write the skill %s: %w", pin.Name, err)
	}
	if os.Rename(tmp, dir) == nil {
		return nil
	}
	// The path holds a tree: another writer's, or one that changed on disk.
	if _, ok := s.Held(project, pin); ok {
		return nil
	}
	old, err := os.MkdirTemp(parent, ".old-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(old) }()
	if err := os.Rename(dir, filepath.Join(old, "tree")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		// A writer that renamed its own tree between the two renames wins.
		if _, ok := s.Held(project, pin); ok {
			return nil
		}
		return err
	}
	return nil
}

// verify refuses a tree whose hash is not the pin's sha256, or whose
// SKILL.md does not name the pin.
func verify(pin config.SkillPin, tree fs.FS) error {
	got, err := pack.TreeHash(tree)
	if err != nil {
		return fmt.Errorf("userspace: skill %s: %w", pin.Name, err)
	}
	if got != pin.SHA256 {
		return fmt.Errorf("userspace: the skill %s at %s has tree hash %s, and the pin holds %s", pin.Name, pin.Commit, got, pin.SHA256)
	}
	skill, err := fs.ReadFile(tree, "SKILL.md")
	if err != nil || !namesSkill(skill, pin.Name) {
		return fmt.Errorf("userspace: the front matter of the skill %s's SKILL.md holds no line name: %s", pin.Name, pin.Name)
	}
	return nil
}

// namesSkill reports whether a SKILL.md opens with a front matter that
// holds the line "name: <name>", as release.SkillFetcher checks it.
func namesSkill(skill []byte, name string) bool {
	for i, line := range strings.Split(string(skill), "\n") {
		line = strings.TrimSuffix(line, "\r")
		switch {
		case i == 0 && line != "---":
			return false
		case i > 0 && line == "---":
			return false
		case line == "name: "+name:
			return true
		}
	}
	return false
}

// writeTree writes each regular file of tree under dst: 0755 when its mode
// holds an executable bit, 0644 otherwise. Each directory is 0700.
func writeTree(tree fs.FS, dst string) error {
	return fs.WalkDir(tree, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path := filepath.Join(dst, filepath.FromSlash(name))
		if d.IsDir() {
			return os.MkdirAll(path, 0o700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := fs.ReadFile(tree, name)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(path, data, mode); err != nil {
			return err
		}
		// The umask trims a new file's mode.
		return os.Chmod(path, mode)
	})
}
