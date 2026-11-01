package config

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// UIKind is the kind of a project's user interface.
type UIKind string

// The two kinds of user interface.
const (
	UIWeb    UIKind = "web"
	UINative UIKind = "native"
)

// UIConfig is the ui block of a project config: the project has a user
// interface, and its files sit under Paths. DesignSkill names a project
// skill that a run holds in place of loop-impeccable.
type UIConfig struct {
	Kind        UIKind `yaml:"kind"`
	Paths       []Glob `yaml:"paths"` // at least one
	DesignSkill string `yaml:"design_skill,omitempty"`
}

// UnmarshalYAML refuses an unknown field of the block, a key that is not a
// scalar, a second key of one name and each glob that breaks the glob rule,
// and names the field. A node's own Decode does not refuse an unknown field,
// so each key is read here, and this loop keeps the strict decoder's
// duplicate key rule.
func (u *UIConfig) UnmarshalYAML(n *yaml.Node) error {
	n = resolve(n)
	if n.Kind != yaml.MappingNode {
		return errors.New("ui: must be a mapping of kind and paths")
	}
	var out UIConfig
	seen := map[string]bool{}
	for j := 0; j+1 < len(n.Content); j += 2 {
		kn := resolve(n.Content[j])
		if kn.Kind != yaml.ScalarNode {
			return fmt.Errorf("ui: line %d: each key must be a scalar", kn.Line)
		}
		if seen[kn.Value] {
			return fmt.Errorf("ui.%s: line %d: key already defined", kn.Value, kn.Line)
		}
		seen[kn.Value] = true
		switch k, v := kn.Value, n.Content[j+1]; k {
		case "kind":
			if err := v.Decode(&out.Kind); err != nil {
				return errors.New("ui.kind: must be web or native")
			}
		case "paths":
			var raw []string
			if err := v.Decode(&raw); err != nil {
				return errors.New("ui.paths: must be a list of globs")
			}
			for i, s := range raw {
				g, err := ParseGlob(s)
				if err != nil {
					return fmt.Errorf("ui.paths[%d]: %v", i, err)
				}
				out.Paths = append(out.Paths, g)
			}
		case "design_skill":
			if err := v.Decode(&out.DesignSkill); err != nil {
				return errors.New("ui.design_skill: must be a skill name")
			}
		default:
			return fmt.Errorf("ui: unknown field %s", strconv.Quote(k))
		}
	}
	*u = out
	return nil
}

// checkUI refuses a kind other than web or native and an empty paths list.
func checkUI(ck *checker, u *UIConfig) {
	if u == nil {
		return
	}
	if u.Kind != UIWeb && u.Kind != UINative {
		ck.fail("ui.kind", "must be web or native")
	}
	if len(u.Paths) == 0 {
		ck.fail("ui.paths", "must list at least one glob")
	}
}

// DesignSkill refuses a ui.design_skill that names no skill of the
// project: a name in the loop- namespace, a name that breaks the pin name
// rule, and a name that is neither a pin of skills: nor an entry of a
// skill root at ref.
func DesignSkill(ctx context.Context, cfg *ProjectConfig, tree TreeReader, ref string) error {
	if cfg.UI == nil || cfg.UI.DesignSkill == "" {
		return nil
	}
	name := cfg.UI.DesignSkill
	ck := &checker{source: ProjectConfigPath}
	switch {
	case strings.HasPrefix(name, reservedSkillPrefix):
		ck.fail("ui.design_skill", "is in the %s* namespace, which the pack adds", reservedSkillPrefix)
	case !pinNameRe.MatchString(name):
		ck.fail("ui.design_skill", "is not lower-case letters, digits and dashes")
	case slices.ContainsFunc(cfg.Skills, func(p SkillPin) bool { return p.Name == name }):
	case tree == nil:
		return errors.New("config: no tree reader lists the skill directories")
	default:
		for _, root := range skillRoots {
			entries, err := tree(ctx, ref, root)
			if err != nil {
				return fmt.Errorf("config: list %s at %s: %w", root, ref, err)
			}
			if slices.ContainsFunc(entries, func(e TreeEntry) bool { return e.Name == name }) {
				return nil
			}
		}
		ck.fail("ui.design_skill", "names no pin of skills: and no entry of %s at %s", strings.Join(skillRoots, " or "), ref)
	}
	return ck.err()
}
