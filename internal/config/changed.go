package config

import (
	"reflect"

	"gopkg.in/yaml.v3"
)

// settingSections are the top-level keys of a project config that hold its
// settings. Only the coordinator changes them, with `loop settings set`.
var settingSections = []string{"stages", "pools", "merge"}

// SettingsChanged reports whether two config files differ in stages, pools
// or merge. A file that does not parse as YAML is an error.
func SettingsChanged(old, new []byte) (bool, error) {
	a, err := settingsOf(old)
	if err != nil {
		return false, err
	}
	b, err := settingsOf(new)
	if err != nil {
		return false, err
	}
	return !reflect.DeepEqual(a, b), nil
}

// settingsOf decodes the setting sections of one file. It compares values,
// not bytes, so a comment or a layout change is no change. The error never
// holds a value from the file.
func settingsOf(raw []byte) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, &InvalidError{Source: ProjectConfigPath, Reason: backticked.ReplaceAllString(err.Error(), "<value>")}
	}
	out := map[string]any{}
	for _, k := range settingSections {
		out[k] = doc[k]
	}
	return out, nil
}
