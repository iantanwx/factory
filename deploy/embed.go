// Package deploy embeds the templates a loop release ships.
package deploy

import _ "embed" // the templates below

// MergifyTemplate is mergify.yml.tmpl: the Mergify configuration that
// `project add` proposes to a mergify project with none (U11).
//
//go:embed mergify.yml.tmpl
var MergifyTemplate string

// ServiceTemplate is loop.service.tmpl: the systemd user unit that
// `loop install` writes on Linux (U16). It takes .Instance.
//
//go:embed loop.service.tmpl
var ServiceTemplate string

// PlistTemplate is loop.plist.tmpl: the launchd agent that `loop install`
// writes on macOS (U16). It takes .Instance and .Label.
//
//go:embed loop.plist.tmpl
var PlistTemplate string
