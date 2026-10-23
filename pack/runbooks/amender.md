---
name: loop-amender
description: The amender's runbook. Revise the PRD of one epic once, after the doc-reviewer rejected it for the first time. Answer each finding in the document or in a reply.
---

# Amender

The doc-reviewer rejected the PRD of the epic that the brief names, for the first time. The epic is
in `prd_amending`. You revise the PRD once, from the review findings and from each comment a person
left. Nobody is present in this run. A second reject sends the epic to the operator, so this round
is the loop's one chance to settle the findings alone.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
4. `../loop-encode-lessons/SKILL.md`, before you record a lesson.
5. `../loop-sdlc-documents/SKILL.md`, where each document lives and the gates that check it.

## 1. Read the findings

1. Run `loop pr show`, and bring the head of the PRD's pull request into the checkout. The turn
   skill states how.
2. Run `loop review rounds <item>`, and read the reject.
3. Run `loop pr threads`. Read every thread, and every comment that belongs to no thread. A loose
   comment from a person is real work.
4. When `loop pr show` reports a conflict, merge the base before you write any reply. A conflict
   found later forces you to rewrite each reply. Resolve only conflicts inside the PRD. Escalate any
   other conflict.

## 2. Answer each finding once

Read the line of the document together with the finding. Then do exactly one of two things:

- **Agree.** Change the document. Reply in one sentence that names what changed and where. A reply
  of one word is no reply.
- **Disagree.** Change nothing. Reply with the argument: what the finding assumes, and why the
  document holds.

Reply to every thread you read, with `loop pr reply --body "<reply>" --thread <id>`. Reply to a
loose comment with no `--thread`.

Argue a point once, and never twice. When a person repeats a point after your argument, do what the
comment asks. A second argument overrules a person who already read your case. One exception holds.
When the request contradicts a product source or a story label, escalate, and name both.

Change only what a finding or a comment asks for. Each change in the diff maps to one of them. A
rewrite of a section that nobody named hides the change that a person must read.

## 3. Record each lesson

A comment can teach a rule that binds every later document, not only this one. Decide its scope by
`../loop-encode-lessons/SKILL.md`. For a lesson that binds the repository, file a decision that
forces its mechanism:

```bash
loop decision file --title "Which rung holds: <lesson>" --trigger "<the event that forces it>"
```

Never build the mechanism in this run. This run changes the PRD only.

## 4. Land the revision

1. Commit. Run `loop gates --docs`.
2. Push with `loop branch push`.
3. Run `loop pr open --title "<the PRD title>"`. It shows the open pull request, and it returns the
   epic to `prd_review`. Run it after every reply.
4. Finish with `loop run finish --outcome done`.

## 5. When a person must decide

Score each case by `../loop-sdlc-triage/SKILL.md`, and escalate at level 4 or 5:

- A finding needs a product decision, or a number that no document holds. Reply first, and say
  what the answer needs.
- Two comments contradict each other. Reply on both, and name the conflict. Never choose between
  two people.
- The fix needs a second epic. `../loop-sdlc-split/SKILL.md` says why you never split.
- The pull request closed with no merge. No document holds the reason.

When the pull request merged during your run, stop. The merge is the sign-off. Finish the run.

## 6. Never

- Never resolve a thread. The person who opened it judges your answer.
- Never change code, and never change a file other than the PRD.
- Never take a second document.
