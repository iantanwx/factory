package config

import (
	"bytes"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetSetting returns raw with key set to value, or removed when value is
// nil. It edits the one node and keeps every comment. It never removes a
// stage's key under stages: removing the last field leaves "<stage>: {}".
// It does not validate. The command refuses a field other than enabled on
// an unlisted run stage or agent job before it calls SetSetting.
func SetSetting(raw []byte, key SettingKey, value *string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, &InvalidError{Source: ProjectConfigPath, Reason: backticked.ReplaceAllString(err.Error(), "<value>")}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	path := slices.Concat([]string{key.section}, strings.Split(key.field, "."))
	if key.name != "" {
		path = slices.Insert(path, 1, key.name)
	}
	if value == nil {
		if !unsetNode(doc.Content[0], path) {
			return raw, nil
		}
	} else if err := setNode(doc.Content[0], path, *value); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
}

// setNode writes v at path under the mapping n. It adds each missing key,
// and turns a key with no value into a mapping.
func setNode(n *yaml.Node, path []string, v string) error {
	for i, k := range path {
		if n.Tag == "!!null" {
			n.Kind, n.Tag, n.Value = yaml.MappingNode, "", ""
		}
		if n.Kind != yaml.MappingNode {
			return &InvalidError{Source: ProjectConfigPath, Field: strings.Join(path[:i], "."), Reason: "is not a mapping"}
		}
		j := keyIndex(n, k)
		if j < 0 {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null"})
			j = len(n.Content) - 2
		}
		n = n.Content[j+1]
	}
	// The value node keeps its comments. A plain scalar lets the encoder
	// quote a value that YAML would read as something else.
	n.Kind, n.Tag, n.Value, n.Content = yaml.ScalarNode, "", v, nil
	if n.Style != yaml.FlowStyle {
		n.Style = 0
	}
	if path[len(path)-1] == "required_checks" {
		n.Kind, n.Value = yaml.SequenceNode, ""
		for c := range strings.SplitSeq(v, ",") {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: c})
		}
	}
	return nil
}

// unsetNode removes the last key of path under the mapping n, and reports
// whether the file held it. The comments of the removed key move to the
// next key, or below the mapping's own key when no key follows.
func unsetNode(n *yaml.Node, path []string) bool {
	var owner *yaml.Node
	for _, k := range path[:len(path)-1] {
		j := keyIndex(n, k)
		if j < 0 {
			return false
		}
		owner, n = n.Content[j], n.Content[j+1]
	}
	j := keyIndex(n, path[len(path)-1])
	if j < 0 {
		return false
	}
	k, v := n.Content[j], n.Content[j+1]
	n.Content = slices.Delete(n.Content, j, j+2)
	moved := joinComments(k.HeadComment, k.LineComment, v.LineComment, k.FootComment, v.FootComment)
	if j < len(n.Content) {
		next := n.Content[j]
		next.HeadComment = joinComments(moved, next.HeadComment)
	} else {
		owner.FootComment = joinComments(owner.FootComment, moved)
	}
	return true
}

// keyIndex returns the index of key k in the mapping n, or -1.
func keyIndex(n *yaml.Node, k string) int {
	if n.Kind != yaml.MappingNode {
		return -1
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == k {
			return i
		}
	}
	return -1
}

func joinComments(cs ...string) string {
	return strings.Join(slices.DeleteFunc(cs, func(c string) bool { return c == "" }), "\n")
}
