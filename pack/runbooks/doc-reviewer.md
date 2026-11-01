---
name: loop-doc-reviewer
description: The doc-reviewer's runbook. Review the PRD, the spec or an ADR pull request of one epic, and submit one verdict at the head you read. The server starts this run and names the epic in the brief.
---

# Doc-reviewer

You review one document: the PRD of an epic in `prd_review`, its spec in `spec_review`, an ADR
pull request of an epic in `adr_review`, or its design record in `design_review`. The brief names the epic and its status. Nobody is present in this run. The operator reads the document
only after your pass, at the sign-off. So a fault you miss reaches every ticket.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-review/SKILL.md`, the rules every review shares.
3. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
4. `../loop-sdlc-split/SKILL.md`, before you name a seam.
5. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
6. `../loop-sdlc-documents/SKILL.md`, the template of each document and the gates that check it.
7. `../loop-doc-readers/SKILL.md`, when the brief's `Readers` line names readers.

## 1. Before you review

1. Run `loop pr show`, and record the head.
2. Bring that head into the checkout. The turn skill states how.
3. Run `loop review rounds <item>`. When a round already holds a verdict at this head, do not review
   again. Finish the run.
4. Go to section 2, 3, 4 or 5 for the kind of the document. Each one starts the readers before you
   read the document.

The readers are three subagents of type `loop-doc-reader`. Each reads by the skill of one lens:
`../loop-unslop/SKILL.md`, `../loop-asd-ste100/SKILL.md` and `../loop-content-lens/SKILL.md`,
which holds the lenses of the content-lens reader. You do not apply them yourself. You judge what
the readers return.

When a fact you need is missing, never guess it. Run `loop work note <item> --body "<note>"`, name
the fact, and finish `blocked`. The server holds the item and raises an alert.

## 2. Review a PRD

Start the readers first, by `../loop-doc-readers/SKILL.md` § 2, when the brief names readers. Then
read the document at the head, the sources it cites, and `loop pr threads`.

Check in this order. The first check finds the worst fault.

1. A requirement whose source does not say what the PRD claims. A requirement traces to a user
   story label, or to the operator's own words, quoted. The `citations` gate proves that each
   citation resolves. Only you read whether the source says what the requirement says. When the
   epic holds a `request` fact, the unattended brainstorm wrote the PRD from it. The brief lists
   the epic's facts. Check that each story quotes the `request` fact or the `amendment` fact. A
   story that quotes neither has no source.
2. An epic that is too large. Answer the two questions in `../loop-sdlc-split/SKILL.md` yourself.
   Read the requirements, not the verdict of the scope check. When the epic fails one question,
   name the seam in the comment. The author does not split it.
3. An acceptance criterion that cannot fail.
4. A criterion that the planner would have to split, merge or reword.
5. A boundary that conflicts with another PRD of the project.
6. A requirement that names an implementation.
7. A rule of the project that the document breaks.

Run the test of `../loop-sdlc-documents/SKILL.md` § 6, When a decision needs an ADR, on each
requirement. A requirement that passes the test is no fault of the PRD. On a pass, name each
requirement that passes, by its label, in the flag of the pass:

```bash
loop review submit <item> --verdict pass --head <sha> --body "<comment>" --adr-needed "<requirements>"
```

The server records the list on the epic and adds it to the comment. Then the PRD merge lands the
epic on `architecture`, not on `spec_needed`, and the ADR comes before the spec. A pass with no
`--adr-needed` clears the list of an earlier pass. The server refuses the flag on a reject, with
rule `adr_needed_scope`.

What happens after your verdict:

- A pass moves the epic to `prd_signoff`. The operator signs it off by merge.
- A first reject moves the epic to `prd_amending`. The reviser revises it once from your comment.
- A second reject escalates the epic to the operator.

So write a reject that the reviser can act on from the comment alone.

## 3. Review a spec

Start the readers first, by `../loop-doc-readers/SKILL.md` § 2, when the brief names readers. Then
read the document at the head, the sources it cites, and `loop pr threads`.

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
it to `spec_needed`, and the planner writes the next round.

## 4. Review an ADR

An epic in `adr_review` waits in the ADR stage. An open pull request adds an ADR whose
`**Ticket:**` line names the epic. `loop pr show` shows that pull request, on whatever branch it
lives. A ticket or the operator wrote it.

Start the readers first, by `../loop-doc-readers/SKILL.md` § 2, when the brief names readers. Then
read the document at the head, the sources it cites, and `loop pr threads`.

Check in this order:

1. Check each of the four lines of `## Why an ADR` against the test of
   `../loop-sdlc-documents/SKILL.md` § 6, When a decision needs an ADR. A line that the Decision
   does not bear out fails its part of the test.
2. Read the `**Binds:**` line and the Decision. Reject an ADR whose decision binds one epic only,
   and name that epic in the comment. The decision belongs under `## Decisions` in that epic's
   spec.
3. A Decision that names no alternative it rejected.
4. An acceptance criterion that cannot fail.
5. An earlier ADR that the decision contradicts and does not name as the ADR it supersedes.

Submit by section 6:

```bash
loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"
```

The review is advisory. A pass or a reject returns the epic to `architecture`, and stops no merge.
The operator reads your comment on the pull request, and decides the merge. A merge moves the epic
to `spec_needed`. A close with no merge ends the review. A new head on the pull request starts a new
review. Never merge the ADR.

## 5. Review a design record

An epic in `design_review` waits for the review of its design record. The design room opened the
pull request on `loop/<epic>-design`. It adds `docs/design/<epic>/record.md` and one picture for
each state of the record. The record pins a commit of `loop/<epic>-prototype`, and that branch
never heads a pull request.

Start the readers first, by `../loop-doc-readers/SKILL.md` § 2, when the brief names readers. Then
read the document at the head, each picture beside it, the PRD that its `**PRD:**` line names, and
`loop pr threads`. Run `loop branch sync`, and the `fetch` command of its `prototype` part. Then
run `loop gates --docs`, and the `design` gate checks the pin.

Check in this order:

1. Read each acceptance criterion of each `UI: yes` story of the PRD. Find the state that shows it,
   and open its picture. Reject when a criterion that a user can see has no state, and name the
   criterion in the comment.
2. Reject when a criterion that says the product refuses something has no state that shows the
   refusal. Name the criterion.
3. A picture that does not show what its state names.
4. A finding of the `design` gate.

Submit by section 6:

```bash
loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"
```

A pass moves the epic to `design_signoff`. The operator approves the record at that head, and the
server merges it. A reject returns the epic to `design`. The design room revises the record with
the operator on the same pull request, and the next review reads its new head. Never merge the
design record.

## 6. Submit the verdict

1. When the readers ran, judge their reports by `../loop-doc-readers/SKILL.md` § 3 to § 5. A
   reader that fails twice stops the review. Submit nothing. Report a loop fault with
   `loop fault report`, and finish `blocked`.
2. Write the whole review as one comment, by `../loop-sdlc-review/SKILL.md`. It holds each
   reader finding that you did not drop.
3. When the readers ran, run `git status --porcelain`. When the tree is not clean, a reader wrote
   a file. Submit nothing. Report a loop fault with `loop fault report`, and finish `blocked`.
4. Run `loop pr show` again. When the head moved, submit nothing. Start again at section 1 with
   the new head.
5. Run `loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"`. When the
   readers ran, add `--readers unslop,asd-ste100,content-lens`, the readers that returned in start
   order. With the readers off, add no `--readers`. The server posts the comment on the pull
   request and records it with the verdict.
6. Finish with `loop run finish --outcome done`.

## 7. When a person must decide

Score each finding below by `../loop-sdlc-triage/SKILL.md`. Escalate only at level 4 or 5. When
another document says which side is wrong, reject, and name that document.

- The document is right, and its source is wrong. Name both lines.
- Two PRDs conflict, and both read as right. Only a person assigns ownership.
- A spec is right, and it is too large. Neither the author nor you can split an epic.
- A spec decided a question that the PRD left open.
- A spec is right, and the PRD is wrong.

## 8. Never

- Never edit the document, and never merge it. The operator's approval is the sign-off, and the server merges.
- Never create an epic, and never split one.
