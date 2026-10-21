package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultManifestPath is where the secret manifest sits when project config
// names no other path.
const DefaultManifestPath = ".loop/secrets.yaml"

// Manifest is a project's secret manifest (R34, R35). It holds secret names
// and source references only, never values.
type Manifest struct {
	Secrets []ManifestEntry `yaml:"secrets"`
}

// ManifestEntry is one secret the project's runs receive in .env.
type ManifestEntry struct {
	Name       string `yaml:"name"`
	Source     string `yaml:"source"`               // a 1Password reference: op://vault/item[/section]/field
	Production bool   `yaml:"production,omitempty"` // marks a production secret for later per-stage scoping (R34)
}

// Vault returns the 1Password vault of the entry's source reference.
func (e ManifestEntry) Vault() string {
	rest, ok := strings.CutPrefix(e.Source, "op://")
	if !ok {
		return ""
	}
	vault, _, _ := strings.Cut(rest, "/")
	return vault
}

// Names returns the entry names in manifest order.
func (m *Manifest) Names() []string {
	names := make([]string, len(m.Secrets))
	for i, e := range m.Secrets {
		names[i] = e.Name
	}
	return names
}

// ParseManifest parses and validates a secret manifest. It refuses an entry
// with a value field by name, and it never quotes the value.
func ParseManifest(data []byte) (*Manifest, error) {
	const source = "secret manifest"
	if err := refuseValueFields(source, data); err != nil {
		return nil, err
	}
	var m Manifest
	if err := decodeStrict(source, data, &m); err != nil {
		return nil, err
	}
	ck := &checker{source: source}
	seen := map[string]bool{}
	for i, e := range m.Secrets {
		f := fmt.Sprintf("secrets[%d]", i)
		switch {
		case !ValidSecretName(e.Name):
			ck.fail(f+".name", "must be an environment variable name: a letter or _, then letters, digits and _")
		case seen[e.Name]:
			ck.fail(f+".name", "names a secret twice")
		}
		seen[e.Name] = true
		if reason := checkOpRef(e.Source); reason != "" {
			ck.fail(f+".source", "%s", reason)
		}
	}
	if err := ck.err(); err != nil {
		return nil, err
	}
	return &m, nil
}

// refuseValueFields names an entry that holds a value field. The generic
// unknown-field refusal would also stop it, but this message tells the
// author why: the manifest holds names and source references only.
func refuseValueFields(source string, data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
		return nil // decodeStrict reports a syntax error
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "secrets" || root.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for j, entry := range root.Content[i+1].Content {
			if entry.Kind != yaml.MappingNode {
				continue
			}
			for k := 0; k+1 < len(entry.Content); k += 2 {
				if strings.EqualFold(entry.Content[k].Value, "value") {
					return &InvalidError{
						Source: source,
						Field:  fmt.Sprintf("secrets[%d].value", j),
						Reason: "a manifest holds secret names and source references only, never a value; store the value with loop secret set or loop secret import",
					}
				}
			}
		}
	}
	return nil
}

// checkOpRef returns why ref is not a usable 1Password secret reference, or
// "" when it is. A reference names a vault, an item, an optional section and
// a field. A query such as ?attribute=otp is refused, so one entry is one
// stored value.
func checkOpRef(ref string) string {
	rest, ok := strings.CutPrefix(ref, "op://")
	if !ok {
		return "must be a 1Password reference that starts with op://"
	}
	if hasControl(rest) || strings.ContainsAny(rest, "?#") {
		return "must not hold a control character, a query or a fragment"
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 3 || len(parts) > 4 {
		return "must be op://vault/item/field or op://vault/item/section/field"
	}
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return "must not hold an empty segment"
		}
	}
	return ""
}
