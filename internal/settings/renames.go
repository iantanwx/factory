package settings

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
)

// stageRenames is the rename table that a settings change reads. A test
// puts its own pair in place.
var stageRenames = config.StageRenames

// renameStageKey refuses a key that names a stage by its old name. A key
// on a new name moves the stage's old entry in raw to the new key, so the
// proposed file never holds the stage under both keys.
func renameStageKey(raw []byte, key string) ([]byte, *cli.Refusal) {
	section, rest, _ := strings.Cut(key, ".")
	stage, field, _ := strings.Cut(rest, ".")
	if section != "stages" {
		return raw, nil
	}
	for _, r := range stageRenames() {
		switch stage {
		case r[0]:
			return nil, &cli.Refusal{Code: cli.CodeUsage, Rule: "stage_renamed",
				Hint: fmt.Sprintf("stages.%s.%s: %s is now %s; set stages.%s.%s", r[0], field, r[0], r[1], r[1], field)}
		case r[1]:
			moved, err := moveStage(raw, r[0], r[1])
			if err != nil {
				return nil, invalid(key, err.Error())
			}
			raw = moved
		}
	}
	return raw, nil
}

// moveStage renames the key from under stages to to, with its fields and
// comments. It leaves raw as it is when stages holds no key from, or holds
// both keys, which the config refuses to load.
func moveStage(raw []byte, from, to string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || doc.Kind == 0 {
		return raw, nil
	}
	root := doc.Content[0]
	i := keyIndex(root, "stages")
	if i < 0 {
		return raw, nil
	}
	stages := root.Content[i+1]
	j := keyIndex(stages, from)
	if j < 0 || keyIndex(stages, to) >= 0 {
		return raw, nil
	}
	stages.Content[j].Value = to
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	return b.Bytes(), enc.Close()
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
