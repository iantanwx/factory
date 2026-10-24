---
name: loop-doc-reviewer
description: The doc-reviewer's runbook. Review the PRD or the spec of one epic, and submit one verdict at the head you read. The server starts this run and names the epic in the brief.
---

# Doc-reviewer

You review one document: the PRD of an epic in `prd_review`, or its spec in `spec_review`. The
brief names the epic and its status. Nobody is present in this run. The operator reads the document
only after your pass, at the sign-off. So a fault you miss reaches every ticket.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-review/SKILL.md`, the rules every review shares.
3. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
4. `../loop-sdlc-split/SKILL.md`, before you name a seam.
5. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
6. `../loop-sdlc-documents/SKILL.md`, the template of each document and the gates that check it.

## 1. Before you review

1. Run `loop pr show`, and record the head.
2. Bring that head into the checkout. The turn skill states how.
3. Run `loop review rounds <item>`. When a round already holds a verdict at this head, do not review
   again. Finish the run.
4. Read the document at the head, the sources it cites, and `loop pr threads`.

When a fact you need is missing, never guess it. Run `loop work note <item> --body "<note>"`, name
the fact, and finish `blocked`. The server holds the item and raises an alert.

## 2. Review a PRD

Check in this order. The first check finds the worst fault.

1. A requirement whose source does not say what the PRD claims. A requirement traces to a user
   story label, or to the operator's own words, quoted. The `citations` gate proves that each
   citation resolves. Only you read whether the source says what the requirement says.
2. An epic that is too large. Answer the two questions in `../loop-sdlc-split/SKILL.md` yourself.
   Read the requirements, not the verdict of the scope check. When the epic fails one question,
   name the seam in the comment. The author does not split it.
3. An acceptance criterion that cannot fail.
4. A criterion that the spec-author would have to split, merge or reword.
5. A boundary that conflicts with another PRD of the project.
6. A requirement that names an implementation.
7. A rule of the project that the document breaks.

What happens after your verdict:

- A pass moves the epic to `prd_signoff`. The operator signs it off by merge.
- A first reject moves the epic to `prd_amending`. The amender revises it once from your comment.
- A second reject escalates the epic to the operator.

So write a reject that the amender can act on from the comment alone.

## 3. Review a spec

Check in this order:

1. A PRD requirement or criterion that no contract covers. This is the worst fault, because no
   stage after you opens the PRD.
2. A contract that cannot fail.
3. Work that no slice can cover, or an order of dependencies that cannot finish.
4. A decision that names no alternative it rejected.
5. A domain concept that the contracts use and the domain model does not define, or a domain
   model that breaks a rule of `../loop-domain-modeling/SKILL.md`.
6. An excluded part with no owner.
7. An open question that the author could have settled from the PRD or the code.
8. More than two components, or a second component with no line that says why it is not its own
   epic.
9. A section of the spec template that the spec leaves out, or puts out of order. A section with
   nothing to say holds None.
10. A rule of the project that the document breaks.

Never reject a spec for an empty Open questions section. When the source is a set of decision
records that a person wrote, trace those decisions, and do not argue them again.

A pass moves the epic to `spec_signoff`, and the operator signs it off by merge. A reject returns
it to `spec_needed`, and the spec-author writes the next round.

## 4. Submit the verdict

1. Write the whole review as one comment, by `../loop-sdlc-review/SKILL.md`.
2. Run `loop pr show` again. When the head moved, submit nothing. Start again at section 1 with
   the new head.
3. Run `loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"`. The
   server posts the comment on the pull request and records it with the verdict.
4. Finish with `loop run finish --outcome done`.

## 5. When a person must decide

Score each finding below by `../loop-sdlc-triage/SKILL.md`. Escalate only at level 4 or 5. When
another document says which side is wrong, reject, and name that document.

- The document is right, and its source is wrong. Name both lines.
- Two PRDs conflict, and both read as right. Only a person assigns ownership.
- A spec is right, and it is too large. Neither the author nor you can split an epic.
- A spec decided a question that the PRD left open.
- A spec is right, and the PRD is wrong.

## 6. Never

- Never edit the document, and never merge it. The operator's approval is the sign-off, and the server merges.
- Never create an epic, and never split one.
