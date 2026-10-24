# ADR 0005: Carry over blockers, reasons and a ranked implementer queue, and port no labels and no premerge

**Status:** Proposed
**Date:** 2026-10-24
**Ticket:** factorio-9wh

## Context

The tickertown parity audit of 2026-10-23 compared the work-item verbs of tickertown with
`loop schema`. It found gaps in the queue, decision, questions and bug-reports commands. The
operator asked to "identify all gaps, draft requirements for them, then push them through the
entire SDLC process", and asked for an ADR.

Tickertown is the repository at `/home/operator/projects/alpha`. This run read it on 2026-10-24,
at the head of its main branch. This run read the loop at the base `86f03e6`, and the CLI at
server `v0.0.44`.

`loop work show factorio-9wh` refuses this run with `CAPABILITY_DENIED`, the case of loop fault
F-16. So this run read the epic body from the `loop epic create` command in the coordinator's
transcript.

The plan is `docs/plans/2026-10-21-1328-feat-standalone-sdlc-loop-plan.md`. Its smell triage
(Sources / Research) marks "labels and comment markers acting as state" as gone by design.

**Gap 1: no labels on create or edit.** Measured in source:

- Tickertown `queue create --label` and `queue edit --label/--unlabel`
  (`scripts/sdlc/issue.ts:14-25`) write Linear labels.
- Each label it documents carries a routing rule. `Design` marks work that Willis owns, and
  `report-hold.ts` then keeps the issue out of every queue (`issue.ts:19-20`). A hand-made report
  stays held until a person adds `Bug` (`issue.ts:155-157`). `questions prepare --to willis` adds
  `Decision` and `Story Change` (`scripts/sdlc/questions.ts:23-27`).
- The plan defers the Willis routing as tickertown policy (Scope Boundaries, "Deferred to Follow-Up
  Work").
- The loop's `work create` takes `kind`, `title`, `body`, `epic`, `story`, `request` and
  `project`. The only labels in the loop are story labels, checked against the merged PRD
  (`internal/pipeline/adhoc.go:65-69`). `internal/beadsx` holds no label read or write.

**Gap 2: blockers.** Measured in source and on the server:

- Tickertown `queue create --blocked-by` writes each relation, reads it back, and starts the
  ticket in `Backlog` (`scripts/sdlc/issue-relate.ts:14-26`). It refuses an unknown blocker and an
  epic as the blocker of a ticket. `queue block|unblock <ID> --by <ID>` adds or deletes one
  relation (`issue-relate.ts:35-52`).
- Tickertown measured the fault: `queue create --blocked-by TIC-750` once created TIC-757 in
  `Todo` with no relation, and exited 0 (`issue-relate.ts:7-12`).
- The loop writes a blocks edge only in `epic decompose` (`internal/pipeline/decompose.go:137-143`).
  `work create` and `work shape` take no blocker.
- `loop work block factorio-dk4` on v0.0.44 answered exit 2, `NOT_IMPLEMENTED`. The schema gives
  the verb no flag. The ticket table has no `block` row.
- `work unblock` moves `backlog` to `todo` and touches no edge (`internal/machine/ticket.go:149`).
  The dependency-advance job applies it when every blocks edge leads to a done ticket. It holds a
  ticket with a canceled blocker for a person to unblock (`internal/sched/deps.go:66-72`). So an
  unblocked ticket keeps its edge to the canceled blocker.
- `ticketResume` sends every escalated ticket without a pull request to `todo`, also one escalated
  from `backlog` (`internal/machine/ticket.go:34-39`). Admission reads edges of a `todo` ticket
  only through the optional `Deps` reader (`internal/admission/deps.go:29`). No production caller
  sets `Dependencies` in the server options. `grep -rn "Dependencies:" cmd internal` finds nothing
  outside tests. So a ticket escalated from `backlog` resumes claimable while its blocker is open.

**Gap 3: cancel and the resume target.** Measured in source:

- Tickertown `queue cancel` requires `--reason-file`. The reason must name an ADR, a ticket or a
  URL. It refuses a stage-owned state, a live claim, an open pull request, an unfinished child,
  an epic and a decision (`scripts/sdlc/queue-cancel.ts:1-40`). It came from ADR 0050 and ADR 0051
  of tickertown, which made six tickets moot.
- The loop's `work cancel` takes no reason. It starts only from `escalated` for a ticket, and from
  `reproducing` or `escalated` for a bug (`ticket.go:183`, `internal/machine/bug.go`).
- The loop's `work supersede` closes a ticket from `backlog`, `todo` or `in_progress` with
  evidence the server checks: a commit on the base, a done item or an ADR, plus a required reason
  (`internal/github/verbs.go`, `SupersedeEvidence`).
- Tickertown `queue resume --to <state>` exists because an escalation from some states records no
  return state. `resume` then asks a person for one (`scripts/sdlc/handoff.ts:40-43`).
- The loop records the resume target in the escalate write for every state. A ticket returns to
  `todo` (`ticket.go:34`), a bug to `reported` (`bug.go:36`), and an epic by the `epicResume`
  table (`internal/machine/epic.go:37-50`). `answerResume` refuses any other target
  (`internal/machine/rows.go:204-215`).

**Gap 4: decisions.** Measured in source:

- Tickertown requires `--by <date>` or `--waits-for-a-person "<why>"` on `decision file`
  (`scripts/sdlc/decision.ts:77`, `scripts/sdlc/decision-notice.ts:10-17`). Before that rule, 22
  decisions carried a trigger that no reader read, and 5 of those triggers fired before anyone read them
  (`decision.ts:8-10`).
- Tickertown `fire` and `defer` take `--why`. `settle` and `moot` require `--note-file`
  (`decision.ts:80-83`).
- Tickertown fires no decision on its own. Its reason: `fire` wrote `Needs Human`, `breakout.ts`
  opened a room for each ticket in that state, and a room paged the owner (`decision.ts:45-52`).
  The coordinator reads each date that passed and fires the decision by hand
  (`docs/agents/overwatch.md:600-612`).
- The loop's `decision file` takes an optional `--by`. The decision-timers job fires a pending
  decision on its `--by` date (`internal/decision/decision.go:103-107`). A fired decision is an
  inbox entry that waits on a person (KTD21). No timer opens a room (R49).
- The loop's `fire`, `defer` and `moot` take no flag but `--project`. `decision settle` applies
  `answer` with no parameters, so it records no answer text (`internal/machine/verbs.go:393-403`).

**Gap 5: questions.** Measured in source:

- Tickertown `questions answer` takes `--header` and `--choice` for each question, and resumes the
  escalation after the last answer. `--hold <act>` on the last answer resumes nothing, and records
  the act the coordinator does first. `questions await --until <time> --resume` lets `advance`
  resume the ticket at that time (`scripts/sdlc/questions.ts:11-29`). `--choice room` opens a room.
- The loop's `inbox prepare --body [--entry]` writes one free-text question row. `inbox answer`
  on an escalation resumes the item at once, and reads no question row
  (`internal/inbox/answer.go:122-147`).
- The loop's `inbox defer --until` hides the entry until that time, and records no reason. For a
  decision it also moves the decision back to `pending` (`answer.go:207-262`).

**Gap 6: bugs.** Measured in source:

- Tickertown `bug-reports file` takes `--source`, `--from`, `--needs`, `--blocked-by`,
  `--assignee` and `--screenshot`. `shape` takes `--priority <1-4>` and `--blocked-by`. `route`
  takes `--kind duplicate|unbuilt|machinery` (`scripts/sdlc/bugs.ts:18-26`).
- Its `--source` values name tickertown stages, such as `qa-stagehand`, `qa-impeccable` and
  `code-review` (`scripts/sdlc/bugs-shape.ts:46-49`).
- Its duplicate rule at filing compares the proof that the report's `## Reproduction` section
  names (`bugs-shape.ts:186-205`, `scripts/sdlc/bugs-file.ts:184`).
- Its `unbuilt` route keeps the report open. `advance` closes the report when the building issue
  reaches `Done`, and returns it to the queue when that issue ends with no build
  (`scripts/sdlc/bugs-report-route.ts:12-15`). TIC-771 once sat open 16 hours after its target
  closed (`scripts/sdlc/bugs-route-close.ts:4-9`).
- In the loop only the coordinator files a bug, from the operator's report
  (`internal/pipeline/adhoc.go:42-54`).
- The reproducer writes a Priority section into the shaped body (`pack/runbooks/reproducer.md:61-62`).
  No code reads it.
- The reproducer closes a "not built yet" report with `work duplicate --of <the building item>`
  (`reproducer.md:79-80`). The report then stays closed when that item ends as `canceled`.

**Gap 7: the implementer queue order.** Measured in source:

- Tickertown ranks new work: a production bug, an overdue ticket, a resume, a rework, the ticket
  that the most open work waits on, then the oldest (`scripts/sdlc/implementer-rank.ts:46-70`).
  A repair goes ahead of all other new work. A ticket is overdue after 9 ticks of 10 minutes
  (`implementer-rank.ts:133-158`).
- Tickertown measured the cost of oldest-first on 2026-10-12. TIC-454 held a reviewed pull request
  that six issues waited on. The batch left it on each tick from 04:03Z to 09:23Z
  (`implementer-rank.ts:12-30`).
- The loop's work query walks the claim rows in table order (`internal/admission/policy.go:132-147`).
  For the implementer that is `todo` before `repair` (`ticket.go:151-155`). Within one state it
  takes the order of `bd list`.

**Gap 8: premerge.** Measured in source:

- Tickertown `premerge` reads the merge evidence of one pull request for its merger agent
  (`scripts/sdlc/premerge.ts:1-7`).
- The epic body says that the loop's server "does not check required checks itself". The source
  contradicts this. `PassCI` refuses a review pass unless every check on the head is green and
  each check that the branch rules require reports there. A head with no check is never green
  (`internal/github/verbs.go:416-470`). The `direct` mechanism reads CI on the reviewed head again
  before it merges (`internal/merge/direct/direct.go:96-106`).
- The loop has no merger agent. The server hands an eligible pull request to the merge interface
  (KTD29). `loop merge status` lists each pull request a mechanism holds.

**What I assumed, and did not measure:**

- The order that `bd list --json` returns within one status. I did not read beads' source.
- That a coordinator answers an escalation while a question it prepared about it is still open. I
  found no such case in the inbox, because only the coordinator can read the inbox.
- That a canceled building item has left a "not built yet" report closed in a loop project. No
  loop project filed such a report so far.
- That the operator's bug reports name a screenshot by its path on the host, as text.

## Decision

1. **Port no labels.** Each label that tickertown documents carries a routing rule, so a label
   is state outside the transition table (R5). The plan marks labels acting as state as gone by
   design, and defers the Willis routing. The loop already carries the two typed labels it needs:
   the item kind, and the story labels that `--story` checks against the PRD. I rejected a free
   label set on beads items, because nothing in the loop would read it. A later rule that read it
   would bring back the smell that the plan rules out.

2. **Write blockers at create, at shape and with `work block`.** `work create` and `work shape`
   take `--blocked-by <item>`, and may repeat it. The server refuses an unknown item, and an epic
   as the blocker of a ticket, before any write. A ticket with a blocker starts in `backlog`.
   `work block <item> --by <blocker>` adds one edge and moves a `todo` ticket to `backlog`.
   `work unblock <item> --by <blocker>` deletes one edge, and moves the ticket to `todo` when no
   open blocks edge remains. An escalated ticket with an open blocks edge resumes to `backlog`. I
   rejected blockers only in `epic decompose`, as today, because a PRD-less ticket and a shaped bug
   then cannot wait on anything. I rejected a `work block` with no `--by`, as the schema has it
   now, because no event ends a block that names no blocker. I rejected an unblock that leaves the
   edge, because the record then says the ticket still waits.

3. **Require a reason on `work cancel`, and keep its states.** The reason is a fact in the same
   write as `canceled` (R12). I rejected tickertown's cancel from `backlog` and `todo`. The loop's
   `work supersede` closes those states with evidence that the server checks, and that is the
   case that tickertown built its cancel for. I rejected a cancel with no reason, because a later
   reader cannot check why a terminal item closed.

4. **Add no `--to` resume target to `inbox answer`.** The loop writes the resume target in the
   escalate write, for every state, and the answer lands only there. Tickertown's `--to` exists
   because its escalations from some states record no target. The loop has no such state, so
   this gap is obsolete under R12. I rejected a person-named target, because it would bypass the
   transition table (R5).

5. **Keep the `--by` fire, and require a notice on every decision.** The decision-timers job keeps
   firing a pending decision on its `--by` date. Tickertown forbids that fire because its fire
   opened a room that paged the owner. In the loop a fired decision is an inbox entry that the
   coordinator reads first (KTD21), and no timer opens a room (R49). So that reason is obsolete. I
   rejected a fire by hand only, because tickertown measured 22 decisions that no reader read.
   `decision file` requires `--by` or `--waits-for-a-person "<why>"`, and refuses both or neither.
   I rejected today's optional `--by`, because a decision with no date then has nothing that
   notices its trigger.

6. **Record why on every decision move.** `decision fire` and `decision defer` require `--why`.
   `decision settle` and `decision moot` require `--note`. Each one is a fact in the same write as
   the move. I rejected the moves with no text, as today, because `decision settle` then closes a
   decision as answered and records no answer.

7. **Keep free-text questions, and refuse an early answer.** `inbox prepare` keeps one free-text
   question for each call. The coordinator asks the operator in its chat, and writes the chosen
   option into `inbox answer --body`. I rejected tickertown's header and choice fields. They serve
   Linear comment markers and a `--choice room` that opens a room, and in the loop only the
   coordinator opens a room (R49). `inbox answer` on an escalation refuses while a question
   prepared about that entry has no answer. I rejected an automatic resume after the last
   question's answer, because the escalation's answer is the text the item receives, and only the
   coordinator writes it.

8. **Give `inbox defer` a reason, and add no timed resume.** `inbox defer --until` requires
   `--reason`, which names the act or the event the entry waits for. The entry returns with the
   reason. I rejected tickertown's `questions await --until --resume`, because the server cannot
   see whether a person's act happened, so a resume at a time is a guess. I rejected a defer with
   no reason, because the server restarts the coordinator session after a crash (KTD21), and the
   new session cannot tell what the entry waits for. I rejected tickertown's
   `questions answer --hold <act>`, which records the answer and holds the resume until the act.
   In the loop the coordinator defers the entry with `--reason` naming the act, does the act, and
   then answers. So the item resumes only after the act, the reason records the act, and the
   answer needs no second mode that resumes nothing.

9. **Carry priority and a "not built yet" wait on bugs, and port no other bug field.**
   `work shape` requires `--priority 1-4`, with the meanings in `reproducer.md` section 4. A
   report that an open item builds waits in a new bug state with that item as a fact. The
   dependency-advance job closes it when that item reaches `done`, and returns it to `reported` when
   that item reaches `canceled`. I rejected the close as a duplicate, as today, because a canceled build then
   loses the report. I rejected `--source`, `--from` and `--needs`. Only the coordinator files a
   bug in the loop, so the source is always the operator. `needs` is a resource lock of
   tickertown's QA stages, which the plan defers. I rejected screenshot attachments, because beads
   holds text and the reproducer runbook says a screenshot is never the proof. I rejected a
   duplicate check at filing, because the operator's report holds no proof to compare. The
   reproducer proves a duplicate by its test.

10. **Rank the implementer queue.** The work query takes, in order: a ticket shaped at priority
    1, a repair, a ticket overdue in `todo`, a resume, a rework, the ticket that the most open
    items wait on through the whole chain, then the oldest. The overdue wait is a host-config value
    (KTD30). I rejected today's table order, which takes new work before a repair and leaves ties
    to `bd list`. I rejected tickertown's production-bug marker, because no loop bug carries a
    source, and priority 1 covers the same case.

11. **Add no premerge verb.** The server already checks CI at the review pass and again in the
    `direct` mechanism. Tickertown's premerge served a merger agent, and the loop replaced that
    agent with the merge interface (KTD29). No stage merges, so no run would act on a verdict. I
    rejected a run-callable verdict for that reason.

## Consequences

Files, commands and stages that change, for the spec of epic factorio-9wh:

- `internal/cli/surface.go`: `work create` and `work shape` gain `--blocked-by`. `work block` and
  `work unblock` gain `--by`. `work cancel` gains `--reason`. `work shape` gains `--priority`.
  `decision file` gains `--waits-for-a-person`. `decision fire` and `decision defer` gain `--why`.
  `decision settle` and `decision moot` gain `--note`. `inbox defer` gains `--reason`. `gen/`
  regenerates with `make proto`.
- `internal/machine/ticket.go`: a `block` row from `todo` to `backlog`. `unblock` deletes an edge.
  `ticketResume` returns `backlog` for a ticket with an open blocks edge, and the `answer` row's
  `ResumeTo` accepts `backlog` beside `todo`, or `answerResume` refuses it (`internal/machine/rows.go`).
  `cancel` carries a reason fact.
- `internal/machine/bug.go`: a waiting state for a report that an open item builds, with its
  edges out to `shaped`, `duplicate` and `reported`. `cancel` carries a reason fact.
- `internal/machine/decision.go`, `internal/machine/verbs.go` and `internal/decision/decision.go`:
  the notice rule at `file`, and a text fact on each move. The table rows of `DECISION_MOVES`
  stay as they are.
- `internal/pipeline/adhoc.go` and `internal/machine/verbs.go` (`shapedTicket`): blockers at
  create and at shape.
- `internal/inbox/answer.go`: the refusal of an early escalation answer, and the defer reason.
- `internal/admission/policy.go`: the implementer order of decision 10.
- `internal/sched/deps.go`: the dependency-advance job also closes or returns a waiting bug.
- `pack/runbooks/reproducer.md`: the "not built yet" route, and `--priority` on `work shape`.
  Beads already holds a native priority, 0-4, that the loop sets to 2 for every item
  (`internal/beadsx/beadsx.go`, `internal/machine/seams.go`). The spec states whether
  `--priority` writes that field or a fact.
- `pack/runbooks/coordinator.md`: `--reason` on `inbox defer`, and the order of question answers.
- Stages: the stage set stays the same. The implementer, reproducer and coordinator change behaviour.

What this makes easy: a ticket can wait on any item, and the record shows why each item closed or
waits.

What this makes hard:

- Each decision, cancel and defer now needs a sentence of text. The server refuses a caller that has none.
- A new bug state adds edges that the acceptance suite must cover.
- The rank reads every open item's chain on each peek. The spec must bound that read.

Postponed on purpose:

- Labels for a tracker projection. A tracker adapter may need them. The plan defers every adapter
  implementation, and the first adapter forces the decision.
- A production source on bugs. A loop project with a source that files bugs without the operator
  forces it.

A later reader must not:

- Add a label, a marker or a free field that a stage reads as state.
- Unblock a ticket and leave its edge in beads.
- Close a decision or an item with no recorded reason.
- Resume an item to a target that the escalate write did not record.
- Resume an item at a time with no act or answer behind it.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `grep -c '^\*\*Ticket:\*\* factorio-9wh$' docs/adr/0005-work-item-fidelity.md` prints `1`.
- `grep -rn -e '--label' -e '--unlabel' pack/runbooks pack/skills` prints nothing.
- `grep -rln premerge pack/runbooks pack/skills` prints nothing.
- ``grep -rn 'inbox answer[^`]*--to' pack`` prints nothing.
- `loop schema | grep -o '"path":"[a-z]* [a-z]*"' | grep -c -E 'label|premerge'` prints `0`.
- `loop gates --docs` passes.

Epic factorio-9wh owns these, because each waits on its spec and tickets:

- `loop work create --help`, `loop work shape --help`, `loop work block --help` and
  `loop work unblock --help` each list `blocked-by` or `by`.
- `loop decision file --title t --trigger t` exits 2 with code `USAGE`.
- A test proves that `work peek --stage implementer` returns a repair before a new ticket.
- A test proves that an escalated ticket with an open blocks edge resumes to `backlog`.
