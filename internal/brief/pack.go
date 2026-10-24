package brief

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
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

// files lists every regular file of the pack, sorted.
func (p Pack) files() ([]string, error) {
	if p.FS == nil {
		return nil, nil
	}
	var out []string
	err := fs.WalkDir(p.FS, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			out = append(out, name)
		} else if !d.IsDir() {
			return fmt.Errorf("brief: pack entry %q is not a regular file", name)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// Hash is the content hash of the pack: every path and its bytes, and
// each agent under agents/. The run record and the brief hold it (KTD19).
func (p Pack) Hash() (string, error) {
	h := sha256.New()
	files, err := p.files()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		data, err := fs.ReadFile(p.FS, f)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "%s\x00%d\x00", f, len(data)) // a hash never fails a write
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
		_, _ = fmt.Fprintf(h, "agents/%s.md\x00%d\x00", a, len(data))
		h.Write(data)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Write injects the pack into the checkout for the harness. Each skill
// directory is replaced whole, so a file a later pack dropped is gone.
// Each agent file is written over, beside a project's own agents.
func (p Pack) Write(checkout string, hs agenthost.Harness) error {
	root := SkillRoot(hs)
	skills, err := p.Skills()
	if err != nil {
		return err
	}
	for _, s := range skills {
		dir := filepath.Join(checkout, filepath.FromSlash(root), s)
		if fi, err := os.Lstat(dir); err == nil && fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("brief: refuse to write the pack into %s: it is a symlink", dir)
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("brief: clear %s: %w", dir, err)
		}
	}
	files, err := p.files()
	if err != nil {
		return err
	}
	for _, f := range files {
		data, err := fs.ReadFile(p.FS, f)
		if err != nil {
			return err
		}
		dst := filepath.Join(checkout, filepath.FromSlash(path.Join(root, f)))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := config.WriteFileAtomic(dst, data, 0o644); err != nil {
			return fmt.Errorf("brief: write the pack: %w", err)
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
