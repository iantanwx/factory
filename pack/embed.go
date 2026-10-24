// Package pack holds the source of the loop's skill pack (R36): the
// manifest, one runbook for each stage and room, the shared loop-*
// skills, and the read-only loop-* agents a runbook dispatches. Only this file is Go. internal/pack lays the files out for a
// harness and gates them (U15).
package pack

import "embed"

// Files is the pack source as the release embeds it.
//
//go:embed manifest.json runbooks skills agents
var Files embed.FS
