// Package prose holds the vendored STE and unslop linters, so the release
// embeds them and `loop gates` runs them in a project.
package prose

import _ "embed"

// STELint is ste-lint.py.
//
//go:embed ste-lint.py
var STELint []byte

// UnslopLint is unslop-lint.py.
//
//go:embed unslop-lint.py
var UnslopLint []byte
