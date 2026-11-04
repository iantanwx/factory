---
name: loop-sdlc-review
description: The rules every reviewer in the loop shares. The code-reviewer and the doc-reviewer runbooks assume them. Read this with loop-sdlc-turn before you review anything.
---

# loop-sdlc-review

Every review in the loop reads one piece of work and writes one verdict. The code-reviewer reads
the pull request of one ticket. The doc-reviewer reads the pull request of one PRD or one spec.
This skill states what every review does the same way. Your runbook names the work and lists its
faults in order.

In this skill, "the author" is the run or the person who wrote the work you read.

## 1. The review only reads

A reviewer pushes no commit, opens no pull request and edits nothing in the work. A reviewer who
edits the work then reviews its own writing.

Keep your checkout at the head you review, with a clean tree. Write every scratch file in a
directory from `mktemp -d`.

## 2. A gate proves form, and you prove meaning

Do not run the whole gate suite again. A second green run tells you nothing the first did not, and
no gate checks whether the work is true.

| A gate proves | It does not prove |
| --- | --- |
| A citation resolves | The source says what the work claims |
| The code builds and the linter is clean | The code does what the item promised |
| The tests pass | Any test would fail if the code were wrong |

Meaning means that you open what the work points at: the cited source, the item, and the test run
at the merge base. No later stage does this.

You may run any single gate you need as evidence. Name the command and quote its output in the
comment. Never pass a check you could not run. A missing result counts as a failed result.

## 3. Put the worst finding first

Test each finding with one question: does a later stage copy this mistake? A wrong citation spreads
into every contract built on it, and no gate sees it. That finding goes first.

## 4. Report every fault in one round

One round costs a full cycle of author and review. Read the whole work before you reject any part
of it. If you name one fault of three, you pay three cycles.

**A later round judges two things only:**

1. Each finding that an earlier round named.
2. The diff since the last reviewed head.

`loop review rounds <item>` names each earlier round, its head and its verdict. A region that no
earlier round named, and that the new diff does not touch, stays out of the verdict. Name it in the
comment as outside this round. Reviewers who raise late what an earlier round could have raised
push an item to many rounds.

A finding that reverses an earlier round's pass names that round and says why the reversal is
right.

## 5. One round writes one comment

Write the whole review as one comment, and send it with the verdict:
`loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"`. The server posts
the comment on the pull request, and it records the comment with the verdict. The author's next run
reads it. Post no second comment with `loop pr reply`. Two comments that list the same faults read
as two reviews, and the author answers both.

**Every finding states three things:** what is wrong, where it is, and what the source says. A
reason with only the "what" makes the author repeat your work.

**Every reference is a permalink that names a full commit.** Never write a bare file name or a
branch link. A branch link moves when the branch moves. Pin a file the pull request adds to the
head you reviewed. Pin a file the base already holds to the merge base.

**A pass says what you checked, not that you checked.** "C5 holds, and this test fails without the
rule" helps the next reader. "Looks good" does not.

## 6. Judge the work against its own instructions

Pass work that keeps every promise it made, even when you prefer another approach. Leave the better
idea in the comment. A reject on taste spends a round and proves nothing. The reviewer does not
redesign.

Never reject on style that no linter flagged. The linter holds the style rules. A style rule that
matters belongs in the project's standards and in its linter.

One exception holds. In round 1 of a doc review, a reader's prose finding may reject alone when you
check it against the text and it holds. A reader is a subagent of the doc-reviewer, and no linter
reads a document's prose. A code review has no such exception.

In round 2 and later, a prose finding does not reject alone. Name it in the comment as not
blocking, with its proposed edit. Only a content finding rejects.

The reviser applies each round's proposed edit as written. So a later round rejects no text that an
earlier round's proposed edit wrote, unless the finding scores level 4 or higher by
`../loop-sdlc-triage/SKILL.md`. Name that finding in the comment as not blocking, and name the round
whose proposed edit wrote the text.

## 7. Reject what the author can repair, and score the rest

Reject when the author can repair the fault from your comment alone. Escalate only what nobody in
the loop can answer, and score it first against `../loop-sdlc-triage/SKILL.md`.

Three shapes may need a person. The shape alone decides nothing:

1. The work is right, and the source it cites is wrong.
2. Two approved documents contradict each other.
3. The work matches its instructions, and the instructions were wrong.

For each shape, ask one question. Does a third document, or plain arithmetic, say which side is
wrong? Then the finding is level 3, `derived`, and it stays in the loop. Reject the work, and state
the derivation in the comment. A reject that names which document wins can be acted on.

### The two requirement rules

`../loop-sdlc-documents/SKILL.md` states two rules that bind every requirement. No gate reads
either rule, so you apply both. Read the requirements of the work, and each requirement it cites.

- **Assert a vendor's outcome, never a vendor's mechanism.** Reject a requirement that asserts a
  vendor's hostname, record value or internal step. Name the outcome that the author can assert
  instead. A test that reads such a value fails when the vendor moves, and proves nothing about
  the project.
- **A later ADR wins a conflict with the ADR it amends.** Search `docs/adr/` for each ADR that
  cites an ADR the work follows. Reject work that follows the earlier ADR where a later ADR
  disagrees. The higher number wins, whatever word the later ADR chooses for itself. That rule
  picks the side, so the finding is level 3, `derived`. Name both ADRs and the requirement of each.

## 8. Submit at the head you read

Read the head before you start, with `loop pr show`. Read it again before you submit. When the head
moved, do not submit. A verdict at an unread head approves text that nobody read.

```bash
loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"
```

The server reads the pull request's head again at the write. When it is not `--head`, the verdict
gets `STATUS_CHANGED` with rule `head_changed`, and nothing is recorded or posted. Review the new
head that the hint names, and submit again.

Before you review, read `loop review rounds <item>`. When a round already holds a verdict at this
head, do not review again. Finish the run from that verdict. A run that crashed after its comment
must not post a second review.

## 9. Never

- Never edit, push or merge the work.
- Never pass a check you did not run, or a claim whose source you did not open.
- Never write a verdict at a head you did not review.
- Never let a subagent judge. A subagent may find. Only the reviewer judges each finding and
  submits the verdict. The verdict needs one reader who read all of it, and that reader is the
  reviewer.
