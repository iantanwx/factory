# Retro: learn from each finished epic and bug

## Stories

The coordinator opened this epic from the operator's request in factorio-vlle, "i think AAR ->
Retro. easier to understand", and from the kernel and userspace split of factorio-yzrt. The
operator chose the stories below in the intake room on 2026-10-31.

Today the loop keeps a lesson only when a person corrects an agent in a session. `loop-reflect`
then builds a mechanism. A lesson from an unattended run is lost: a review reject, a failed
attempt, an escalation, a loop fault or a red build. The next epic meets the same fault.

In this PRD, a **learning** is one statement of what a stage, a skill or a policy must do
differently, with the cases in the record that show it. A **signal** is an event during the work
that shows a fault, such as a ticket's build that fails twice.

### US-01 Run a retro when an epic reaches done or a bug closes

As the operator, I can rely on the loop to run one retro on each epic that reaches done and on
each bug that closes, and the retro reads that item's whole record, so that the loop keeps a
lesson from unattended runs.

> Chosen: read the full record of the epic or the bug. The option read: "review rounds and their
> findings, failed attempts, escalations and how they routed, loop faults, and the comments people
> left."
>
> "i'd say option 1. ideally, we want learnings to enter unattended as well. for example, if the
> coordinator thread finds CI has failed twice, something has gone wrong. that means implementer
> needs improvements. ..."
>
> "i accept all recommendations." The recommendation read: "Does a retro run on epics and bugs
> that finished before this ships? I recommend no, because otherwise every old item gets a retro
> at once."

### US-02 Record each signal when it happens

As the operator, I can rely on the loop to record each signal on its item when the signal happens,
so that the retro counts it when the epic reaches done or the bug closes.

> "option 1, clear." The option read: "Record it now, judge it at done. When the coordinator or
> overwatch sees a signal (CI red twice, two failed attempts, a second reject), it writes a retro
> note on the item. The retro still runs only on `epic.done` and `bug.closed`."

### US-03 Judge each learning against a scope rubric

As the operator, I can rely on two judges and Jev to judge each learning before anything lands,
so that a learning reaches the right place and a big change reaches me.

> "i'd say 1, but with an escalation policy via jev + judges."
>
> "i'd say we need another criteria: would this fundamentally change the loop? for example, would
> it add a new stage? add a new skill? or would is just modify an existing stage/skill. that's a
> slidign scale, and requires judgement that an AI can do, before it escalates. the rest of your
> criteria are fine to me."
>
> "i agree with option 1." The option read: "Between `extend` and `add`. Levels 1 and 2 land
> unattended. A new skill, gate, stage or state comes to you."

The rubric has three questions. Each judge answers each question alone.

| Question | What it asks | The answers |
| --- | --- | --- |
| Q1, target | Where does the fix live? | `loop fault`, `pack PR`, `project policy`, `userspace skill`, `project ticket` |
| Q2, evidence | How many distinct cases in the record show the learning? | `one`, `two or more` |
| Q3, change size | How much does the fix change the loop? | `edit`, `extend`, `add`, `restructure` |

Q1 tells the kernel from userspace with one test: would the same fix be right in a project with
another stack and other tools? Yes is the kernel. No is userspace.

| Q3 level | Name | What the fix does |
| --- | --- | --- |
| 1 | `edit` | It changes a rule that exists in a stage, a skill or a policy |
| 2 | `extend` | It adds a rule or a check to a stage, a skill or a gate that exists |
| 3 | `add` | It adds a skill, a gate or a setting |
| 4 | `restructure` | It adds or removes a stage, a role or an item state, or it changes a transition |

### US-04 Land each learning that the rubric passes, with no step for the operator

As the operator, I can let a learning that passes the rubric land with no step of mine, so that
the loop improves itself between my visits.

> "i'd say 1, but with an escalation policy via jev + judges." The option read: "Unattended,
> through the normal gates. A userspace learning becomes a pull request that changes
> `.loop/policy/<role>.md` or a userspace skill. The code-reviewer reviews it, and the merge
> mechanism merges it with no sign-off from you. A kernel learning becomes a loop fault. A
> project ticket goes into the backlog. You get one inbox notice per retro that lists what landed,
> and you don't have to act on it."
>
> "i accept all recommendations." The recommendation read: "What a `project ticket` learning
> becomes. vlle makes 'a unit of work is either an epic or bug'. I recommend filing it as a bug
> report, so the reproducer shapes it like any other."

### US-05 Escalate each other learning to the operator

As the operator, I can read each learning that the rubric does not pass as an escalation with both
judges' answers, so that I decide what the loop cannot.

> "i'd say 1, but with an escalation policy via jev + judges."

### US-06 Promote a rule that two projects hold to the kernel

As the operator, I can rely on the coordinator to find a userspace rule that two projects hold,
and to report it as a loop fault, so that a lesson the judges misread as userspace reaches the
kernel.

> "i'm leaning to option 2, becuase option 1 sounds very over-engineered." The option read: "The
> coordinator compares projects on a timer." The room then narrowed it to the userspace changes
> that retro notices name since the last comparison, and the operator kept it in this epic:
>
> "keep it in this epic. it naturally fits together and should be done atomically."

## Scope check

US-02 records signals and needs nothing else. US-01 needs the new trigger. US-03 judges what US-01
finds. US-04 and US-05 act on the route of US-03. US-06 reads the notices of US-04.

**Stories:** 6
**Estimated total:** 2,600 lines
**Lands independently?** Yes. US-02 merges alone, because a note on an item needs no retro. US-01 merges alone and records its learnings as notes. US-03 merges after US-01 and records each route as a note. US-04 and US-05 each merge alone after US-03. US-06 merges after US-04.
**Promises provable alone?** Yes. "A signal is recorded when it happens" needs one item and one fake build. "A retro runs once on done" needs the trigger and one fixture item. "A learning seen once lands nothing" needs fake judges and a fake Jev. "A passed learning opens a pull request" needs one fixture project. "Two projects that hold one rule produce a loop fault" needs two fixture projects and the coordinator's comparison. It is the last promise to prove.
**Verdict:** proceed.

## Goal

When an epic reaches done or a bug closes, the loop finds what its runs got wrong, and it fixes the
stage, the skill or the policy that let it happen. A small fix that two cases show lands with no
step for the operator. A big change, an unclear one or one with a single case reaches the
operator, or waits for a second case. A rule that two projects learn on their own reaches the
loop's maintainers.

## Requirements

- The loop starts one retro when an epic reaches done.
  → § US-01
- The loop starts one retro when a bug closes.
  → § US-01
- The loop starts no second retro for an item that already had one.
  → § US-01
- The loop starts no retro for an epic or a bug that reached done or closed before this epic
  ships.
  → § US-01
- A retro reads the item's review rounds and their findings, its failed attempts, its escalations
  and their routes, its loop faults, the comments people left on it, and its signal notes. For an
  epic, the retro also reads the same record of each of its tickets.
  → § US-01
- The loop records a signal note on the item when a signal happens. The signals are a ticket whose
  build fails twice, an item with two failed attempts, and a document with a second reject.
  → § US-02
- A signal note names the signal, the item and the cases that show it.
  → § US-02
- A signal note changes no stage, no skill and no policy.
  → § US-02
- Two judges answer Q1, Q2 and Q3 about each learning, and neither judge reads the other's answer.
  → § US-03
- Jev scores how far apart the two judges' answers are, by the convergence scale of the triage
  rubric.
  → § US-03
  → .claude/skills/loop-sdlc-triage/SKILL.md § The convergence score
- A judge answers Q1 `pack PR` only in the loop's own project.
  → § US-03
- Q3 applies to the targets `pack PR`, `project policy` and `userspace skill`. Q3 does not apply to
  `loop fault` or `project ticket`.
  → § US-03
- A learning lands when both judges name one target, both answer `two or more`, and Jev's
  convergence and confidence are each at or above the triage rubric's thresholds.
  → § US-03
  → .claude/skills/loop-sdlc-triage/SKILL.md § The two thresholds a person may change
- A learning that Q3 applies to lands only when both judges answer `edit` or `extend`.
  → § US-03
- A learning that either judge answers `one` lands nothing. The retro records it as a note on the
  item, and a later retro counts it as a case.
  → § US-03
- A learning that would weaken or delete a rule that the operator recorded lands nothing.
  → § US-03
- A `project policy` or `userspace skill` learning lands as a pull request in the project. The
  project's review and merge rules land it, with no sign-off by the operator.
  → § US-04
- A `pack PR` learning lands as a pull request in the loop's own project, by the same rules.
  → § US-04
- A `loop fault` learning lands as a loop fault that names each case.
  → § US-04
- A `project ticket` learning lands as a bug report in the project, and the reproducer shapes it
  like any other bug report.
  → § US-04
- Each retro puts one notice in the operator's inbox. The notice lists each learning that landed,
  with its target and a link, and each learning that waits for a second case. The notice needs no
  answer.
  → § US-04
- Each learning that passes no route rule above, and is not held for a second case, reaches the
  operator as an escalation.
  → § US-05
- The escalation holds the learning, its cases, each judge's three answers and reasons, and Jev's
  reading.
  → § US-05
- On a timer, the coordinator compares each userspace change that a retro notice names since the
  last comparison with the userspace rules of the host's other projects.
  → § US-06
- When two projects hold one rule, the coordinator files one loop fault that names both projects
  and both rules.
  → § US-06
- The coordinator files no second loop fault for a rule that an open loop fault already names.
  → § US-06

## Acceptance criteria

- **A1.** An epic that reaches done gets exactly one retro.
- **A2.** A bug that closes gets exactly one retro.
- **A3.** An epic or a bug that has not reached done or closed gets no retro.
- **A4.** A ticket whose build fails for the second time carries a signal note before the epic is
  done.
- **A5.** A signal note changes no stage, no skill and no policy.
- **A6.** A retro's learning names each case it rests on, and each case is in the item's record.
- **A7.** A learning that either judge answers `one` lands nothing, and it is a note on the item.
- **A8.** A later retro that finds a second case of a held learning counts both cases.
- **A9.** A learning on which the two judges name different targets reaches the operator.
- **A10.** A learning that either judge answers `add` or `restructure` reaches the operator, and
  nothing lands.
- **A11.** A learning whose Jev convergence or confidence falls under its threshold reaches the
  operator.
- **A12.** A learning that Jev could not score reaches the operator.
- **A13.** A learning that both judges answer `project policy`, `two or more` and `edit`, with Jev
  above both thresholds, opens a pull request in the project, and the loop asks the operator nothing.
- **A14.** A retro outside the loop's own project never opens a pull request in the loop's own
  project.
- **A15.** A learning that would delete a rule the operator recorded reaches the operator, and
  nothing lands.
- **A16.** Each retro puts exactly one inbox notice, and the notice asks no question.
- **A17.** Each escalation from a retro holds both judges' answers and Jev's reading.
- **A18.** A userspace rule that two projects hold produces one loop fault that names both
  projects.
- **A19.** A userspace rule that one project holds produces no loop fault.
- **A20.** An epic or a bug that reached done or closed before this epic ships gets no retro.
- **A21.** A `project ticket` learning that lands is a bug report in the project.

## Boundaries with other epics

- **The kernel and userspace split.** factorio-yzrt owns it: which skills are kernel, where a
  userspace skill lives, and how a project pins its own. This epic writes a userspace learning
  only into the places that factorio-yzrt defines. This epic waits on factorio-yzrt.
- **The names.** factorio-vlle owns the vocabulary: "retro", the event names `epic.done` and
  `bug.closed`, and which bug states count as closed. The operator accepted the recommendation to leave the
  closed states to factorio-vlle. This epic uses the names that factorio-vlle settles, and it
  waits on factorio-vlle.
- **The triage panel.** `loop escalation judge` and the triage rubric own the two judges, Jev, the
  convergence scale and the two thresholds. This epic adds the retro's three questions. It changes
  neither the triage rubric nor its thresholds.
  → .claude/skills/loop-sdlc-triage/SKILL.md
- **Corrections in a session.** `loop-reflect` owns a correction that a person makes in a session,
  and the correction ledger. This epic reads the record of unattended runs, and it adds no ledger
  row.
  → .claude/skills/loop-reflect/SKILL.md
- **Overwatch.** factorio-a7r owns the sweep, which reads and never writes. When the sweep finds a
  signal, the coordinator or the server writes the note. The sweep itself writes none.
- **Loop faults.** `loop fault report` owns the fault record. This epic files faults through it.
- **New stages in other epics.** factorio-ku99 (an ADR stage), factorio-nhb5 (a QA stage),
  factorio-1r94 (a simplify stage) and factorio-4mb8 (a judged doc review) each add a stage that
  claims an item. The retro is the first stage that an event starts. Their runs produce records
  that a retro reads, and none of them owns the event trigger.
- **Checked against:** the PRD of factorio-j4x (`docs/prd/project-scoped-settings.md`), the
  requests of factorio-yzrt, factorio-vlle, factorio-ku99, factorio-nhb5, factorio-1r94 and
  factorio-4mb8, ADRs 0001 to 0012, the triage rubric, `loop-reflect`, `loop-encode-lessons` and
  the correction ledger. factorio-yzrt and factorio-vlle hold no PRD yet, so this epic waits on
  both. No conflict found.

## Out of scope

- Corrections that a person makes in a session. `loop-reflect` owns them.
- A host ledger of lessons across projects. Never, by the operator's words "option 1 sounds very
  over-engineered" (US-06).
- A retro that a signal starts before the epic reaches done. Never, by the operator's choice "option 1,
  clear." (US-02).
- Bugs that a person files against an epic after it reaches done. A later trigger would own them, and
  the operator chose the record of the item alone (US-01).
- A retro on an epic or a bug that finished before this epic ships. Never, by the operator's
  acceptance of the room's recommendation (US-01).
- The kernel and userspace split itself. factorio-yzrt owns it.
- The rename of roles, phases and paths. factorio-vlle owns it.
- A change to the triage rubric or its thresholds. The triage rubric owns them.

## Open questions

None. The operator answered the three questions of the room on 2026-10-31 with "i accept all
recommendations."
