# ADR NNNN: <the decision, in one line>

**Status:** Proposed | Accepted
**Date:** YYYY-MM-DD
**Ticket:** <epic id>
**Binds:** <epic ids, comma-separated> | all

> Copy this file to `docs/adr/NNNN-<slug>.md`. Take the next free number. Never reuse a number,
> and never renumber an existing ADR. Other documents cite these by number.
>
> **`Ticket:` is required.** An ADR is the first stage of an architecture epic. The epic moves
> from `architecture` to `spec_needed` when a merge adds its ADR to the base branch. The server
> joins the file to the epic through the epic id on this line, because the filename carries a
> number and no id. Write `none` for a decision recorded outside the pipeline.
>
> An ADR records a decision that binds later work. Write one only for a decision that passes the
> test of `../loop-sdlc-documents/SKILL.md` § 6, When a decision needs an ADR. That section also
> names the place of each decision that fails it.
>
> **`Binds:` names each epic that the decision binds**, comma-separated, or `all` for the whole
> project. The `## Why an ADR` section answers the four parts of the test, one line each.
>
> **`Status:` reads Proposed or Accepted.** A later ADR with a higher number supersedes this one,
> and nobody edits this one after it lands. The same section states that rule.
>
> **Architecture does not go through a PRD.** Each PRD requirement cites a user story or the
> operator's own words. "Use this framework" has no such source. An architecture epic writes ADRs
> here instead, and the operator decides by merge.
>
> The `adr` gate of `loop gates` checks the header, the number, the four sections, a rejected
> alternative and the criteria. Delete this block and every example before you commit.

## Context

State what forced the decision. Give the facts a reader needs, and no history that they do not
need.

Name what you measured. "The board shows 27 findings" is a fact. "Performance was poor" is not.
When you could not measure something, say so, and say what you assumed instead.

## Decision

State the choice in one sentence. Then say what you rejected, and why.

A choice with no rejected alternative is an assumption, not a decision. Write it under Assumptions
in the spec instead, or find the alternative that you dismissed without noticing.

## Why an ADR

Answer each part of the test with a fact about this decision.

- **More than one epic:** <which epics, or why the whole project>
- **Rejected alternative:** <the alternative the Decision rejects>
- **Costly to undo:** <which change of part 3 it makes>
- **How, not what:** <the requirement it serves, or why it decides how>

## Consequences

Say what this makes easy, and what it makes hard. Name the files and the commands that change.

Name what a later reader must not do. An ADR that only records a preference binds nothing.

## Acceptance criteria

Write a check that somebody can run. Each line must be able to fail.

Each line must also be checkable when this ADR lands. A criterion that waits for a screen, or for a
feature that nobody has specified, is a promise the epic cannot keep. Name the epic that owns it
instead, under the list.

- `grep -rn "<the thing this ADR bans>"` returns nothing outside this ADR.
- `loop gates` passes.

> One ADR required a 30 second countdown on a resend control. Each other criterion in that ADR
> ran as a grep or a type check. The countdown needed a screen that no epic had specified. So the
> criterion now names the epic that ships the screen.

**Write a criterion that scopes itself.** "Every strategy module exports one `Strategy`" holds for
the first strategy and for the fortieth, with no edit. "Every one of the 40 strategies exports one
`Strategy`" fails until the last one lands, so the first epic can never pass it.

---

## For an architecture bootstrap

A new application needs several decisions before any feature ticket can start. Write one ADR per
decision, not one ADR for all of them, because each one changes on its own schedule.

Decide all of these before the first feature epic leaves its PRD:

| Decision | Why it blocks the first feature |
| --- | --- |
| Application framework and its version | Every route and every page depends on it |
| Data store | The implementer builds the data layer against a real store |
| Domain package layout | Services speak domain types only, so the package must exist first |
| Authentication and the email sender | The first user story is often sign-up |
| Test runner and the integration-test boundary | Red-green needs a runner from the first ticket |
| Deployment target and preview environments | The merge reads the checks that these produce |
| Styling and the component catalogue | The coding standards require reuse before invention |

Each ADR names the file that proves it landed. A decision with no file is a preference.

Record the decisions that you postponed on purpose, and say what forces each one later. A
postponed decision that nobody recorded reads to the next agent as a decision that nobody made.
