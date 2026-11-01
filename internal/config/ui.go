package config

import (
	"errors"
	"fmt"
	"strconv"

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
// interface, and its files sit under Paths. No reader reads it yet.
type UIConfig struct {
	Kind  UIKind `yaml:"kind"`
	Paths []Glob `yaml:"paths"` // at least one
}

// UnmarshalYAML refuses an unknown field of the block and each glob that
// breaks the glob rule, and names the field. A node's own Decode does not
// refuse an unknown field, so each key is read here.
func (u *UIConfig) UnmarshalYAML(n *yaml.Node) error {
	n = resolve(n)
	if n.Kind != yaml.MappingNode {
		return errors.New("ui: must be a mapping of kind and paths")
	}
	var out UIConfig
	for j := 0; j+1 < len(n.Content); j += 2 {
		switch k, v := n.Content[j].Value, n.Content[j+1]; k {
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
