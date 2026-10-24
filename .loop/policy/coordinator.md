# Coordinator policy

This project is the loop's own repository. A fault in the loop is a defect in this project.

## Decision routes

| Kind of decision | Who decides | How it reaches them |
| --- | --- | --- |
| Product: a verb, a stage, a runbook rule, the operator's setup steps | the triage rubric first: the loop at level 1 to 3, the operator at level 4 or 5 | `loop work create` at level 1 to 3. An intake room, opened with `--score`, at level 4 or 5 |
| An act that cannot be undone, spends money or changes production data | the operator | the question tool, in the coordinator's chat |

Score each finding against the triage rubric before you pick a route. A route to the operator
applies only when the rubric sends the finding to a person: level 4 or 5, or the Choice outside.
When two routes then apply, the stricter one wins.

## Standing authorizations

- 2026-10-23: file each open loop fault whose cause is in the loop's code as a bug in this
  project. It covers `loop work create --kind bug` in this project, with the operator's words as
  `--request`: `you should register yourself, so we can start to systematically poll faults/bugs and work on features.`
  It keeps one bug for each fault, and the fault id in the bug's
  body. It never covers a change to another project.
- 2026-10-24: the operator, `do everything you can do by yourself. make sure this is part of the loop skills/runbooks. do not ask me unless you absolutely have to.`
  It covers these acts:
  - Merging a PRD or spec pull request the operator approved in the coordinator's chat, at the
    head the operator read.
  - Running a host upgrade the operator decided.
  - Fast-forwarding the host clone's base branch (loop fault F-14).
  - Reading back the result of each.

  It never covers an act that cannot be undone, spends money or changes production data, and it
  never covers a decision only the operator can make.
- 2026-10-23: the operator, on factorio-obc, `the server should not refuse the coordinator of loop itself.`
  and, of a release, `that is definitely somethint you can do.` It covers running the host verbs
  the server opens to the coordinator from its checkout: `loop upgrade` when a published release
  ends an open loop fault, `loop rollback` when that upgrade breaks the loop, `loop doctor`, and
  `loop project list` and `loop project show`. A release needs no act: each push to `main` that
  passes CI becomes the next release. Never list a release or an upgrade under "Blocked on you".

## Report additions

- Each open loop fault, the bug it became, and the release that will end it.
