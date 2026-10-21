// Package deploy embeds the templates a loop release ships.
package deploy

import _ "embed" // the templates below

// MergifyTemplate is mergify.yml.tmpl: the Mergify configuration that
// `project add` proposes to a mergify project with none (U11).
//
//go:embed mergify.yml.tmpl
var MergifyTemplate string
