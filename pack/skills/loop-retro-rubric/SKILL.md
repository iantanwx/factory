---
name: loop-retro-rubric
description: The three questions two judges answer about one retro learning. Where its fix lives, how many cases show it, and how much it changes the loop. Read this before you write a retro's learnings.
---

# loop-retro-rubric

A retro reads the record of one finished epic or bug. It writes learnings. A learning states what a
stage, a skill or a policy must do differently, and it names the cases in the record that show it.
This skill states the three questions that a judge answers about each learning.

`loop retro judge` reads this file as the rubric. It sends the whole file to each judge, with one
learning and its cases. Each judge answers alone, and it never reads the other judge's answer. A
convergence reader then scores how far apart the two answers are. `loop retro route` sends the
panels to the server, and the server routes each learning. The judges say where a fix lives and how
large it is. They never write the fix.

## What a judge reads

A judge reads the learning, its cases and this rubric. A case is one element of the item's record,
such as a run, a review round, an escalation, a loop fault, a comment, a note or a signal. A case
from an earlier retro has a reference and no text. A judge runs no command.

## Q1, target: where does the fix live?

| Answer | Where the fix lives |
| --- | --- |
| `loop_fault` | The loop itself: a verb, a gate, the server or a loop skill. The loop's maintainers fix it. |
| `pack_pr` | A loop skill or a loop runbook, changed in the loop's own project. |
| `project_policy` | A file `.loop/policy/<role>.md` of this project. |
| `userspace_skill` | A skill of this project, outside the `loop-` skills. |
| `project_ticket` | The project's own code. The fix is work for the project, and it is no rule for the loop. |

**One test tells the kernel from userspace.** Would the same fix be right in a project with another
stack and other tools? Yes is the kernel: `loop_fault` or `pack_pr`. No is userspace:
`project_policy` or `userspace_skill`.

**A judge answers `pack_pr` only in the loop's own project.** In every other project, a fix of the
kernel is a `loop_fault`. The judge prompt says when the retro is outside the loop's own project.

## Q2, evidence: how many distinct cases in the record show the learning?

| Answer | When |
| --- | --- |
| `one` | One case shows it, or each case is a copy of one event |
| `two_or_more` | Two or more distinct cases show it |

Two references to one event are one case. For example, a run and the note that the same run wrote
are one case. A learning that extends a held learning counts the held learning's cases too.

## Q3, change size: how much does the fix change the loop?

Q3 applies to `pack_pr`, `project_policy` and `userspace_skill`. For `loop_fault` and
`project_ticket`, answer `n/a`.

| Level | Answer | What the fix does |
| --- | --- | --- |
| 1 | `edit` | It changes a rule that exists in a stage, a skill or a policy |
| 2 | `extend` | It adds a rule or a check to a stage, a skill or a gate that exists |
| 3 | `add` | It adds a skill, a gate or a setting |
| 4 | `restructure` | It adds or removes a stage, a role or an item state, or it changes a transition |

Only levels 1 and 2 can land with no person. Levels 3 and 4 go to the operator.

## The reason

Each judge gives one sentence of reason. Name the cases and the rule that the reason rests on. The
convergence reader compares the two reasons, and a reason with no evidence gives it nothing to
compare.
