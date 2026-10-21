---
name: loop-sdlc-split
description: How to tell when one epic must become several, and who splits it. Read this when a PRD or a spec reaches more than one subsystem, or covers work that cannot land as one unit.
---

# loop-sdlc-split

An epic that reaches more than one subsystem cannot be specified or tested as one unit. This skill
states where the seam falls, and who may cut along it.

## 1. The seam is the source, not the size

Ask which source each part of the work comes from.

- Product behaviour has a source that a person approved: a user story or the operator's own words.
  It belongs to a PRD.
- A decision with no product source is architecture. It belongs to a decision record, not to a PRD.

One epic must not hold both. A review checks each PRD requirement against its source. An invented
requirement in an architecture epic has no source, so no check reads it.

## 2. Two questions decide the unit

1. Can each part land on its own?
2. Can each promise be proven without building the rest?

Two parts that must land together are one epic, and the ticket count does not change that. An epic
that fails either question is two epics. A size number only prompts the questions. It decides
nothing.

## 3. Who splits

Only the operator agrees to a split, in an intake room. The room writes each new PRD and submits
each one. A split creates epics, and a wrong epic costs a person's time to undo.

- **The doc-reviewer names the seam.** At the PRD stage it rejects the PRD and names the seam in
  its comment. It never creates an epic.
- **The amender never splits.** When the seam needs a second epic, it escalates with a score.
- **The spec-author never splits.** It names the seam under Open questions, and proposes the
  split.
- **The decomposer never splits an epic.** It cuts tickets inside one epic.

## 4. Stop when a split is too late

- **The epic already holds tickets.** A split then moves work under a new parent. Escalate, and
  name each ticket.
- **The epic is past its PRD sign-off.** A split at that point is two documents to rewrite, and a
  spec that already cites the old scope. Escalate with the seam and its evidence.

## 5. Never

- Never split an epic that the operator has not agreed to split.
- Never create an epic from a run. Only an intake room creates one, with `loop prd submit`.
