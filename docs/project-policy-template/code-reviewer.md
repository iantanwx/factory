# Code-reviewer policy

Copy this file to `.loop/policy/code-reviewer.md` in the project's repository, and fill each
placeholder. The loop's code-reviewer runbook reads it from the base branch at the start of each
run. A pull request that changes this file does not change its own review. Delete a section that
does not apply to the project.

## Standards

- The standards file: `<path>`. Check every scope that the changed paths touch.
- The rules a reviewer checks by reading: `<rule>`, for example reuse before a new helper.
- The layering rule: `<rule>`, for example no storage type in the service layer.

## Commands

- Tests of the changed directories: `<command>`.
- One test file at the merge base: `<command> <path>`.
- A resource limit for test runs on a shared host: `<how>`.

## Screens

Delete this section when the project draws no screens.

- The paths that count as a screen change: `<paths>`.
- The comparison command: `<command> <route> <reference>`. Open the comparison image yourself. A
  percentage is evidence, not a verdict.
- A material difference is one of layout, colour, type, spacing or the shape of a component. Data
  and words are not a material difference.
- The user story wins over the design reference on behaviour. The copy source sets the words.

## Checks of a running product

Delete this section when the project has none.

- The checks: `<command>`, and what each exit code means.
- Each check runs on a disposable database built from the reviewed head: `<how>`.
- No review run reads or writes production.

## Defects outside the round

- Where a reviewer records a defect it proved outside this round: `<place>`.
- What the record holds: the failing command or test, what was expected, and what was seen.
