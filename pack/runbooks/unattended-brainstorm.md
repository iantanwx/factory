---
name: loop-unattended-brainstorm
description: The unattended brainstorm's runbook. Write the PRD of one epic that the coordinator created from the operator's request, and submit it for review. The server starts this run and names the epic in the brief.
---

# Unattended brainstorm

The coordinator created the epic that the brief names from the operator's own words, and the epic
waits for its PRD. You write that PRD, and you submit it for review. Nobody is present in this
run. The doc-reviewer reads the PRD next, and a person reads it first at PRD sign-off. A story that
you invent ships with nobody reading it.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-sdlc-split/SKILL.md`, before you decide the epic is one unit.
4. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
5. `../loop-sdlc-documents/SKILL.md`, where each document lives and the gates that check it.
6. `../loop-brainstorm/SKILL.md`, how a PRD goes from the operator's words to user stories.

Do these steps in this order.

## 1. Read the source

Read the brief. Then read three parts of the epic in `item`: the `request` fact, the body, and the
`amendment` fact. The `request` fact holds the operator's own words. The `amendment` fact holds a
person's answer to an earlier escalation of this epic, when one exists.

## 2. Find the round

Run `loop pr show`. When the epic already has a PRD pull request, bring its head into the
checkout, as the turn skill states, and continue that PRD. Never open a second PRD.

## 3. Write the PRD

Write the PRD at `docs/prd/<slug>.md`. Start from the template, `../loop-brainstorm/prd.md`.

- Each story quotes the operator's words from the `request` fact or the `amendment` fact. These
  two facts are the only source a story quotes.
- Each requirement cites a story, the epic body or a file of the repository.
- A requirement with no source goes under Open questions. Never guess a source.

## 4. When no story can quote a source

When the `request` fact and the `amendment` fact yield no story that you can quote, do not write
the PRD. Run `loop escalation raise --score 4` with a body that names what the request leaves
open. Then finish `blocked`.

## 5. When the work reaches more than one subsystem

Name the seam under Open questions, by the split skill. Never split. Only the operator agrees to a
split, in a brainstorm room.

## 6. Submit the PRD

1. Run `loop gates --docs`. Repair each finding.
2. Commit the PRD. Write the message to a file outside the checkout, and pass it with
   `git commit -F <file>`.
3. Run `loop prd submit --path docs/prd/<slug>.md --title "<title>" --body "<body>"`. The server
   pushes the commit, opens the pull request and moves the epic to `prd_review`.
4. Finish with `loop run finish --outcome done`.

## 7. Never

- Never write code. A PRD run changes one document.
- Never quote a source other than the `request` fact or the `amendment` fact in a story.
- Never create an epic, and never split one.
- Never merge the PRD. The operator's approval is the sign-off, and the server merges.
