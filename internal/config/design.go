package config

import (
	"slices"
	"strconv"
	"strings"
)

// DesignConfig is the design block of a project config: the commands that
// serve the prototype and capture one state of it, and the canvas skill
// that the design room holds. Skill is empty when the room holds none.
type DesignConfig struct {
	Run     string `yaml:"run"`
	Capture string `yaml:"capture"`
	Skill   string `yaml:"skill,omitempty"`
}

// RuleDesignWithoutUI is the rule of a design block in a config that holds
// no ui key.
const RuleDesignWithoutUI = "design_without_ui"

// DeclaresUI reports whether the config holds a ui key. It is the one
// reader that decides whether a project declares a UI. Every other package
// reads it, and no field of the ui block.
func (c *ProjectConfig) DeclaresUI() bool { return c.UI != nil }

// checkDesign refuses a design block in a config that declares no UI, and
// an empty run or capture.
func checkDesign(ck *checker, c *ProjectConfig) {
	d := c.Design
	if d == nil {
		return
	}
	if !c.DeclaresUI() {
		ck.fail("design", "rule %s: the file holds no ui key; add the ui block, or remove the design block", RuleDesignWithoutUI)
	}
	if strings.TrimSpace(d.Run) == "" {
		ck.fail("design.run", "must be a nonempty command")
	}
	if strings.TrimSpace(d.Capture) == "" {
		ck.fail("design.capture", "must be a nonempty command")
	}
}

// CanvasSkill refuses a design.skill that names neither one of kernel nor
// a pin of skills:, and names the skill. It quotes only a name that keeps
// the pin name rule, since a misplaced value can be a secret.
func CanvasSkill(cfg *ProjectConfig, kernel []string) error {
	if cfg.Design == nil || cfg.Design.Skill == "" {
		return nil
	}
	name := cfg.Design.Skill
	if slices.Contains(kernel, name) || slices.ContainsFunc(cfg.Skills, func(p SkillPin) bool { return p.Name == name }) {
		return nil
	}
	ck := &checker{source: ProjectConfigPath}
	if pinNameRe.MatchString(name) {
		ck.fail("design.skill", "names %s, which is neither a kernel skill nor a pin of skills:", strconv.Quote(name))
	} else {
		ck.fail("design.skill", "is not lower-case letters, digits and dashes")
	}
	return ck.err()
}
