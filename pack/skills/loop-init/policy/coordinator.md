# Coordinator policy

Copy this file to `.loop/policy/coordinator.md` in the project's repository, and fill each
placeholder. The loop's coordinator runbook reads it for each project the instance runs. The
runbook holds the loop's rules. This file holds who decides what in this project, and what this
project adds to each report. Delete a section that does not apply.

## Decision routes

| Kind of decision | Who decides | How it reaches them |
| --- | --- | --- |
| Product: copy, flow, scope, a user story, a design | `<role>` | `<route>` |
| An act that cannot be undone, spends money or changes production data | the operator | the question tool, in the coordinator's chat |
| `<kind>` | `<role>` | `<route>` |

When two routes apply, the stricter one wins.

## Standing decisions

- `<kind of question>`: the coordinator decides against `<source>`, and names it in the report.

## Standing authorizations

Each authorization names its date, its scope, the resources it covers, the guards it keeps, and
what it never covers.

- `<date>`: `<scope>`. It covers `<resources>`. It keeps `<guards>`. It never covers `<exclusions>`.

## Health limits

- CI wall time: at most `<n>` minutes. The remedies, in order: `<remedy>`.
- A flaky test is repaired at its cause. It is never retried or skipped.
- The oldest open item: at most `<n>` hours.

## Production

- `<job>` fails: find the cause, repair it, wait for the deploy, and start it again once with
  `<command>`. Report the attempt and its result.
- Never start `<job>` again while a run of it is live.

## Report additions

- `<line>`, for example the open questions that a product owner holds.
- Link a document as `<repository link>/blob/<full commit>/<path>`.
