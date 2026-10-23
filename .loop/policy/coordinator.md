# Coordinator policy

This project is the loop's own repository. A fault in the loop is a defect in this project.

## Decision routes

| Kind of decision | Who decides | How it reaches them |
| --- | --- | --- |
| Product: a verb, a stage, a runbook rule, the operator's setup steps | the operator | an intake room |
| An act that cannot be undone, spends money or changes production data | the operator | the question tool, in the coordinator's chat |

When two routes apply, the stricter one wins.

## Standing authorizations

- 2026-10-23: file each open loop fault whose cause is in the loop's code as a bug in this
  project. It covers `loop work create --kind bug` in this project, with the operator's words as
  `--request`: `you should register yourself, so we can start to systematically poll faults/bugs and work on features.`
  It keeps one bug for each fault, and the fault id in the bug's
  body. It never covers a release, an upgrade of the host, or a change to another project.

## Report additions

- Each open loop fault, the bug it became, and the release that will end it.
