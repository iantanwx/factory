package config

import (
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillPin is one pinned skill: a kernel pin of the release pack, or a
// userspace pin of a project config (ADR 0007 decision 7, ADR 0013). The
// host fetches its files from GitHub, and TreeHash checks them against
// SHA256.
type SkillPin struct {
	// Name is the upstream skill name, such as impeccable.
	Name string `yaml:"name" json:"name"`
	// Repo is owner/name on GitHub.
	Repo string `yaml:"repo" json:"repo"`
	// Commit is the pinned commit.
	Commit string `yaml:"commit" json:"commit"`
	// Path is the skill's directory in the repository. "" is the root.
	Path string `yaml:"path" json:"path"`
	// SHA256 is the TreeHash of that directory at Commit.
	SHA256 string `yaml:"sha256" json:"sha256"`
}

// reservedSkillPrefix is brief.SkillPrefix, the namespace the pack adds
// (KTD19). internal/brief imports this package, so it cannot be named here.
const reservedSkillPrefix = "loop-"

var (
	pinNameRe   = regexp.MustCompile(`^[a-z0-9-]+$`)
	pinCommitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pinSHA256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Check reports the first field of p that breaks a pin's invariant. The
// error names the field and never the refused value.
func (p SkillPin) Check() error {
	switch {
	case !pinNameRe.MatchString(p.Name):
		return errors.New("name is not lower-case letters, digits and dashes")
	case strings.HasPrefix(p.Name, reservedSkillPrefix):
		return fmt.Errorf("name is in the %s* namespace, which the pack adds", reservedSkillPrefix)
	case !repoRe.MatchString(p.Repo) || strings.Contains(p.Repo, "..") || strings.HasSuffix(p.Repo, "/."):
		return errors.New("repo is not owner/name on GitHub")
	case !pinCommitRe.MatchString(p.Commit):
		return errors.New("commit is not 40 lower-case hex characters")
	case !pinSHA256Re.MatchString(p.SHA256):
		return errors.New("sha256 is not 64 lower-case hex characters")
	case p.Path != "" && (p.Path == "." || !fs.ValidPath(p.Path)):
		return errors.New("path is not a clean relative path inside the repository")
	}
	return nil
}

// PinLabel names the pin at index i of a list: by its name, or by its
// index when the name is empty.
func PinLabel(i int, name string) string {
	if name == "" {
		return strconv.Itoa(i)
	}
	return strconv.Quote(name)
}

// SkillPins is the skills: list of a project config.
type SkillPins []SkillPin

var pinFields = []string{"name", "repo", "commit", "path", "sha256"}

// UnmarshalYAML refuses an unknown field of a pin and names the pin. The
// strict decoder's own refusal names only a line.
func (ps *SkillPins) UnmarshalYAML(n *yaml.Node) error {
	var out []SkillPin
	if err := n.Decode(&out); err != nil {
		return err
	}
	for i, item := range resolve(n).Content {
		item = resolve(item)
		for j := 0; j+1 < len(item.Content); j += 2 {
			if k := item.Content[j].Value; !slices.Contains(pinFields, k) {
				return fmt.Errorf("skills[%d]: pin %s: unknown field %s", i, PinLabel(i, out[i].Name), strconv.Quote(k))
			}
		}
	}
	*ps = out
	return nil
}

// resolve follows a YAML alias to the node it names.
func resolve(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	return n
}

// checkSkills refuses each pin that fails Check, and a second pin with one
// name.
func checkSkills(ck *checker, pins SkillPins) {
	seen := map[string]bool{}
	for i, p := range pins {
		err := p.Check()
		if err == nil && seen[p.Name] {
			err = errors.New("name is the name of another pin")
		}
		if err != nil {
			ck.fail(fmt.Sprintf("skills[%d]", i), "pin %s: %v", PinLabel(i, p.Name), err)
		}
		seen[p.Name] = true
	}
}
