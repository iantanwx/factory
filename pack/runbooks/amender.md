---
name: loop-amender
description: The amender's runbook. Answer the findings on the PRD or the spec of one epic, after the first PRD reject or a person's comment. Answer each finding in the document or in a reply.
---

# Amender

You answer the findings on one document of the epic that the brief names. Nobody is present in
this run. Two events start you:

- **The first PRD reject.** The doc-reviewer rejected the PRD for the first time. The epic is in
  `prd_amending`. A second reject sends the epic to the operator. So this round is the loop's one
  chance to settle the findings alone.
- **A person's comment.** A person commented on the PRD or the spec in review or in sign-off, and
  the loop did not answer. The server moved the epic to `prd_amending` or `spec_amending`. The
  `amend_from` fact in `loop work show <item>` names the state that the comment found.

The epic's state names your document: `prd_amending` is the PRD, and `spec_amending` is the spec.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
4. `../loop-encode-lessons/SKILL.md`, before you record a lesson.
5. `../loop-sdlc-documents/SKILL.md`, where each document lives and the gates that check it.

## 1. Read the findings

1. Run `loop pr show`, and bring the head of the document's pull request into the checkout. The
   turn skill states how.
2. Run `loop review rounds <item>`, and read the last verdict. A comment can start you with no
   reject.
3. Run `loop pr threads`. Read every thread, and every comment that belongs to no thread. A loose
   comment from a person is real work. The summary of a person's review is a loose comment.
4. When `loop pr show` reports a conflict, merge the base before you write any reply. A conflict
   found later forces you to rewrite each reply. Resolve only conflicts inside your document.
   Escalate any other conflict.

## 2. Answer each finding once

Read the line of the document together with the finding. Then do exactly one of two things:

- **Agree.** Change the document, and apply `../loop-unslop/SKILL.md` to each sentence you write.
  Reply in one sentence that names what changed and where. A reply of one word is no reply.
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

Never build the mechanism in this run. This run changes your document only.

## 4. Land the revision

1. When you changed the document, commit, and run `loop gates --docs`. Write the message by
   `../loop-commit/SKILL.md`. Push with `loop branch push`.
2. Run `loop pr open --title "<the document title>"`. It shows the open pull request, and the
   server picks where the epic goes. A push returns the document to review. With no push, a
   document that a comment found in sign-off returns to sign-off. Run it after every reply.
3. Finish with `loop run finish --outcome done`.

A reply that changes nothing costs no review round. So change a signed-off document only when a
finding asks for it.

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
- Never change code, and never change a file other than your document.
- Never take a second document.
