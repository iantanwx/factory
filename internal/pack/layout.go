// Package pack lays the embedded skill pack out for a harness and gates it
// (R36-R39, KTD19). The source tree holds runbooks/<stage>.md and
// skills/loop-*/. A run's checkout receives one loop-* directory for each:
// a runbook becomes loop-<stage>/SKILL.md, which is the path
// brief.Runbook names, and a skill keeps its directory. The source also
// holds agents/loop-*.md, the read-only subagents a runbook dispatches.
// They land in the harness's agent directory, beside the skill root.
package pack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"
	"testing/fstest"

	"github.com/iantanwx/loop/internal/brief"
)

// ManifestFile is the manifest's path in the pack source.
const ManifestFile = "manifest.json"

// Manifest names every runbook and skill of the pack. The gate checks the
// files against it, so a file the manifest does not name never ships.
type Manifest struct {
	// Runbooks maps a stage or room stage to its entry. The runbook's
	// source is runbooks/<stage>.md.
	Runbooks map[string]Runbook `json:"runbooks"`
	// Skills lists the shared skills, each a directory skills/loop-*.
	Skills []string `json:"skills"`
	// Agents lists the subagents, each a file agents/loop-*.md.
	Agents []string `json:"agents,omitempty"`
}

// Runbook is one runbook's manifest entry.
type Runbook struct {
	// Attended is true when a person is in the session: the coordinator
	// and every room. An unattended runbook holds no step that asks one.
	Attended bool `json:"attended"`
	// Skills lists each skill the runbook reads. The runbook names each
	// one by its path.
	Skills []string `json:"skills"`
	// Agents lists each subagent the runbook dispatches. The runbook
	// names each one.
	Agents []string `json:"agents,omitempty"`
	// Policy is the role whose project policy file the runbook names,
	// .loop/policy/<role>.md in the project repository. Empty names none.
	Policy string `json:"policy,omitempty"`
}

// PolicyPath is where a project keeps the policy of one role (R37, R38).
func PolicyPath(role string) string { return ".loop/policy/" + role + ".md" }

// ReadManifest reads and checks the manifest of a pack source.
func ReadManifest(src fs.FS) (*Manifest, error) {
	raw, err := fs.ReadFile(src, ManifestFile)
	if err != nil {
		return nil, fmt.Errorf("pack: read %s: %w", ManifestFile, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("pack: %s: %w", ManifestFile, err)
	}
	for _, s := range m.Skills {
		if !strings.HasPrefix(s, brief.SkillPrefix) {
			return nil, fmt.Errorf("pack: %s: skill %q is not in the %s* namespace", ManifestFile, s, brief.SkillPrefix)
		}
	}
	for _, a := range m.Agents {
		if !strings.HasPrefix(a, brief.SkillPrefix) || !validName(a) {
			return nil, fmt.Errorf("pack: %s: agent %q is not a lower-case name in the %s* namespace", ManifestFile, a, brief.SkillPrefix)
		}
	}
	for stage, rb := range m.Runbooks {
		if !validName(stage) {
			return nil, fmt.Errorf("pack: %s: runbook %q is not a lower-case stage name", ManifestFile, stage)
		}
		for _, s := range rb.Skills {
			if !slices.Contains(m.Skills, s) {
				return nil, fmt.Errorf("pack: %s: runbook %q reads skill %q, which the manifest does not list", ManifestFile, stage, s)
			}
		}
		for _, a := range rb.Agents {
			if !slices.Contains(m.Agents, a) {
				return nil, fmt.Errorf("pack: %s: runbook %q dispatches agent %q, which the manifest does not list", ManifestFile, stage, a)
			}
		}
	}
	return &m, nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// RunbookSource is a runbook's path in the pack source.
func RunbookSource(stage string) string { return "runbooks/" + stage + ".md" }

// AgentSource is an agent's path in the pack source.
func AgentSource(name string) string { return "agents/" + name + ".md" }

// Agents returns the tree of the pack's agents: one loop-*.md file at
// its root for each agent the manifest lists.
func Agents(src fs.FS) (fs.FS, error) {
	m, err := ReadManifest(src)
	if err != nil {
		return nil, err
	}
	out := fstest.MapFS{}
	for _, a := range m.Agents {
		data, err := fs.ReadFile(src, AgentSource(a))
		if err != nil {
			return nil, fmt.Errorf("pack: agent %q: %w", a, err)
		}
		out[a+".md"] = &fstest.MapFile{Data: data, Mode: 0o644}
	}
	return out, nil
}

// Injected is a runbook's directory in a run's skill root.
func Injected(stage string) string { return brief.SkillPrefix + stage }

// Layout returns the tree a run receives under its harness's skill root:
// one loop-* directory for each runbook and each skill, and a claim on the
// loop-<name> directory of each third-party pin. Two sources that
// land on one directory are refused.
func Layout(src fs.FS) (fs.FS, error) {
	m, err := ReadManifest(src)
	if err != nil {
		return nil, err
	}
	out := fstest.MapFS{}
	owner := map[string]string{}
	claim := func(dir, by string) error {
		if prev, ok := owner[dir]; ok {
			return fmt.Errorf("pack: %s and %s both lay out %s", prev, by, dir)
		}
		owner[dir] = by
		return nil
	}
	stages := slices.Sorted(maps.Keys(m.Runbooks))
	for _, stage := range stages {
		dir := Injected(stage)
		if err := claim(dir, RunbookSource(stage)); err != nil {
			return nil, err
		}
		data, err := fs.ReadFile(src, RunbookSource(stage))
		if err != nil {
			return nil, fmt.Errorf("pack: runbook %q: %w", stage, err)
		}
		out[dir+"/SKILL.md"] = &fstest.MapFile{Data: data, Mode: 0o644}
	}
	for _, skill := range m.Skills {
		if err := claim(skill, "skills/"+skill); err != nil {
			return nil, err
		}
		root := "skills/" + skill
		if _, err := fs.Stat(src, path.Join(root, "SKILL.md")); err != nil {
			return nil, fmt.Errorf("pack: skill %q: %w", skill, err)
		}
		err := fs.WalkDir(src, root, func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(src, name)
			if err != nil {
				return err
			}
			out[skill+strings.TrimPrefix(name, root)] = &fstest.MapFile{Data: data, Mode: 0o644}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("pack: skill %q: %w", skill, err)
		}
	}
	// A pinned skill is fetched on the host, so the source holds none of
	// its files. Its directory is claimed all the same, and a pin that
	// lands on a runbook or a skill is refused here.
	pins, err := ReadThirdParty(src)
	if err != nil {
		return nil, err
	}
	for _, p := range pins {
		if err := claim(brief.SkillPrefix+p.Name, fmt.Sprintf("%s pin %q", ThirdPartyFile, p.Name)); err != nil {
			return nil, err
		}
	}
	return out, nil
}
