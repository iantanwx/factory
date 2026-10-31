package brief

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
)

// SkillPrefix is the reserved namespace of loop skills (KTD19).
const SkillPrefix = "loop-"

// Pack is the skill pack the release ships (R36). Its FS holds one
// directory for each skill at its root, each named loop-*. Agents holds
// one subagent definition for each file at its root, each named
// loop-*.md. internal/pack embeds the real pack. A nil FS is an empty
// pack, and a nil Agents holds no agent.
type Pack struct {
	Version string
	FS      fs.FS
	Agents  fs.FS
	// User holds the userspace skills of the run's project, each written
	// under its own name (ADR 0013 decision 1). Hash leaves them out.
	User []UserSkill
}

// UserSkill is one userspace skill pin and its verified tree, as the store
// holds it.
type UserSkill struct {
	Pin  config.SkillPin
	Tree fs.FS
}

// WithUser returns a copy of p that holds skills.
func (p Pack) WithUser(skills []UserSkill) Pack {
	p.User = slices.Clone(skills)
	return p
}

// UserSkills lists the names of p's user skills, sorted. A name in the
// loop- namespace, a kernel directory's name, or a name held twice is
// refused.
func (p Pack) UserSkills() ([]string, error) {
	kernel, err := p.Skills()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range p.User {
		n := u.Pin.Name
		switch {
		case strings.HasPrefix(n, SkillPrefix) || slices.Contains(kernel, n):
			return nil, fmt.Errorf("brief: user skill %q takes a kernel skill name", n)
		case slices.Contains(out, n):
			return nil, fmt.Errorf("brief: user skill %q is held twice", n)
		}
		if err := u.Pin.Check(); err != nil {
			return nil, fmt.Errorf("brief: user skill %q: %w", n, err)
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// SkillRoot is where the harness reads skills: Claude Code reads
// .claude/skills and Codex reads .agents/skills.
func SkillRoot(h agenthost.Harness) string {
	switch h {
	case agenthost.Claude:
		return ".claude/skills"
	case agenthost.Codex:
		return ".agents/skills"
	}
	return ""
}

// AgentRoot is where the harness reads subagent definitions: Claude Code
// reads .claude/agents. A harness with no agent directory returns "", and
// it receives no agent.
func AgentRoot(h agenthost.Harness) string {
	if h == agenthost.Claude {
		return ".claude/agents"
	}
	return ""
}

// Runbook is the path of the stage's runbook in the run's checkout. The
// pack names a stage's skill loop-<stage>.
func Runbook(h agenthost.Harness, stage string) string {
	return SkillRoot(h) + "/" + SkillPrefix + stage + "/SKILL.md"
}

// Skills lists the pack's skill directories, sorted. A top-level entry
// outside the loop- namespace, or a file, is refused.
func (p Pack) Skills() ([]string, error) {
	if p.FS == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(p.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("brief: read the pack: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), SkillPrefix) {
			return nil, fmt.Errorf("brief: pack entry %q is not a %s* skill directory", e.Name(), SkillPrefix)
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// AgentNames lists the pack's agents, sorted, each without .md. An entry
// outside the loop- namespace, or one that is no Markdown file, is
// refused.
func (p Pack) AgentNames() ([]string, error) {
	if p.Agents == nil {
		return nil, nil
	}
	entries, err := fs.ReadDir(p.Agents, ".")
	if err != nil {
		return nil, fmt.Errorf("brief: read the pack's agents: %w", err)
	}
	var out []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !e.Type().IsRegular() || !ok || !strings.HasPrefix(name, SkillPrefix) {
			return nil, fmt.Errorf("brief: pack agent %q is not a %s*.md file", e.Name(), SkillPrefix)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// AgentFiles is the path of each agent in a checkout for the harness. A
// caller keeps them out of git as it keeps the skill directories out.
func (p Pack) AgentFiles(h agenthost.Harness) ([]string, error) {
	root := AgentRoot(h)
	if root == "" {
		return nil, nil
	}
	names, err := p.AgentNames()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, root+"/"+n+".md")
	}
	return out, nil
}

// packFile is one regular file of the pack, and the mode a checkout
// receives it at: 0755 when its source mode holds an executable bit, and
// 0644 otherwise.
type packFile struct {
	name string
	mode fs.FileMode
}

// files lists every regular file of the pack, sorted.
func (p Pack) files() ([]packFile, error) {
	if p.FS == nil {
		return nil, nil
	}
	return treeFiles(p.FS)
}

// treeFiles lists every regular file of tree, sorted.
func treeFiles(tree fs.FS) ([]packFile, error) {
	var out []packFile
	err := fs.WalkDir(tree, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			if d.IsDir() {
				return nil
			}
			return fmt.Errorf("brief: pack entry %q is not a regular file", name)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		out = append(out, packFile{name, mode})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, err
}

// Hash is the content hash of the pack: every path, its mode and its
// bytes, and each agent under agents/. The run record and the brief hold
// it (KTD19). It reads the kernel only, so two projects' runs of one
// release hold one hash.
func (p Pack) Hash() (string, error) {
	h := sha256.New()
	files, err := p.files()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		data, err := fs.ReadFile(p.FS, f.name)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "%s\x00%o\x00%d\x00", f.name, f.mode, len(data)) // a hash never fails a write
		h.Write(data)
	}
	agents, err := p.AgentNames()
	if err != nil {
		return "", err
	}
	for _, a := range agents {
		data, err := fs.ReadFile(p.Agents, a+".md")
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "agents/%s.md\x00644\x00%d\x00", a, len(data))
		h.Write(data)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Write injects the pack into the checkout for the harness. Each skill
// directory is replaced whole, so a file a later pack dropped is gone. A
// file whose mode holds an executable bit is written 0755, and any other
// file 0644. Each user skill is written under its own name, every byte as
// the store holds it. Each agent file is written over, beside a project's
// own agents.
func (p Pack) Write(checkout string, hs agenthost.Harness) error {
	root := filepath.Join(checkout, filepath.FromSlash(SkillRoot(hs)))
	skills, err := p.Skills()
	if err != nil {
		return err
	}
	users, err := p.UserSkills()
	if err != nil {
		return err
	}
	for _, s := range append(skills, users...) {
		dir := filepath.Join(root, s)
		if fi, err := os.Lstat(dir); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("brief: refuse to write the pack into %s: it is a symlink", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("brief: clear %s: %w", dir, err)
		}
	}
	if p.FS != nil {
		if err := writeTree(p.FS, root); err != nil {
			return err
		}
	}
	for _, u := range p.User {
		if err := writeTree(u.Tree, filepath.Join(root, u.Pin.Name)); err != nil {
			return err
		}
	}
	agents, err := p.AgentFiles(hs)
	if err != nil {
		return err
	}
	for _, f := range agents {
		data, err := fs.ReadFile(p.Agents, path.Base(f))
		if err != nil {
			return err
		}
		dst := filepath.Join(checkout, filepath.FromSlash(f))
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("brief: refuse to write the pack into %s: it is a symlink", dst)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := config.WriteFileAtomic(dst, data, 0o644); err != nil {
			return fmt.Errorf("brief: write the pack: %w", err)
		}
	}
	return nil
}

// writeTree writes each regular file of tree under dst, at its pack mode.
func writeTree(tree fs.FS, dst string) error {
	files, err := treeFiles(tree)
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := fs.ReadFile(tree, f.name)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, filepath.FromSlash(f.name))
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := config.WriteFileAtomic(out, data, f.mode); err != nil {
			return fmt.Errorf("brief: write the pack: %w", err)
		}
	}
	return nil
}
