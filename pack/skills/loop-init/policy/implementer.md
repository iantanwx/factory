# Implementer policy

Copy this file to `.loop/policy/implementer.md` in the project's repository, and fill each
placeholder. The loop's implementer runbook reads it at the start of each run. The runbook holds
the loop's rules. This file holds the project's rules. Delete a section that does not apply to the
project.

## Commands

- Fast gates: `<command>`. Run them after each commit.
- Tests of the changed directories: `<command>`.
- One test file: `<command> <path>`.
- The full suite: `<command>`. Run it only when `<condition>`. CI runs it on every pull request.

## Standards

- The standards file: `<path>`. The changed paths select its scopes.
- The commit message rules: `<path or skill>`. The subject holds at most `<n>` characters.

## Architecture

- The layers: `<layer>` owns `<what>`, and it never `<what>`.
- The domain types live in `<package>`. Translate at each edge, even when the shapes match today.
- The build order of a feature: `<domain, then service, then storage, then interface>`. Go from red
  to green at each step.

## Tests

- The assertion library: `<library>`.
- Patterns that make a test vacuous in this project: `<pattern>`.
- Tests that touch storage start from a known state: `<how>`.

## Design sources

Delete this section when the project draws no screens.

- The copy source sets the words: `<path>`.
- The user stories set the behaviour: `<path>`.
- The design references set the look: `<path>`.
- The comparison command: `<command> <route> <reference>`. Put each mismatch in the pull request
  body.

## Priorities

- `<rule>`, for example: production bugs first.
