# ADR 0001: An unattended PRD author writes the PRD when the rubric routes new work to the loop

**Status:** Proposed
**Date:** 2026-10-24
**Ticket:** factorio-4n0

## Context

The tickertown parity audit of 2026-10-23 found four gaps in how new product work becomes a PRD.
The operator asked to "identify all gaps, draft requirements for them, then push them through the
entire SDLC process", and asked for an ADR. Tickertown is the repository at
`/home/operator/projects/alpha`, read on 2026-10-24 at the head of its main branch. This run read
the loop at the base `6bfba44`.

The epic body names the gaps. `loop work show factorio-4n0` refuses this run with
`CAPABILITY_DENIED` (`item_not_held`), because ticket factorio-o96 carries no `epic` fact. The
coordinator filed it on 2026-10-23, before the fix of loop fault F-16. So this run read the body
from the epic JSON in the coordinator's transcript, as the runs of ADR 0003 and ADR 0006 did.

The operator's correction of 2026-10-23 drives this ADR. The coordinator opened an intake room for
a gap with no score. The operator said: "close the other room. we should not need break out rooms
unless human intervention is truly required." The operator then named tickertown's rubric, and
asked the loop to port it and respect it. The plan decided the opposite for intake. Its Key
Decisions say: "Intake is an interactive brainstorm session that produces the PRD", chosen over
"the coordinator creating an epic from a short description for an unattended PRD author" (plan,
Product Contract, Key Decisions). R44 says: "On this path no unattended stage drafts the PRD."
This ADR amends that decision for the work that the rubric routes to the loop.

This run measured each fact below in source. The last list names what it assumed.

**G1: no stage writes a PRD.** Measured:

- `prdSubmit` refuses every caller but an intake room with `intake_room_only`
  (`internal/pipeline/prd.go:36`). It also creates the epic. So a PRD and its epic exist only
  when an intake room ends.
- The epic table has two entry rows: `prd_submit` to `prd_review`, and `create_architecture` to
  `architecture` (`internal/machine/epic.go:273`). No state holds an epic that waits for its PRD.
- The loop holds an `intake` run stage with a throughput entry (cap 8, schedule `20,50 * * * *`)
  and a mutex (`internal/config/throughput.go:102`, `internal/admission/mutex.go:17`). The
  scheduler skips it (`internal/sched/scheduler.go:439`), and the pack holds no runbook for it.
  The intake cap counts epics in `prd_review`, `prd_amending`, `spec_needed`, `spec_drafting`
  and `spec_review` (`internal/admission/caps.go:19`). `room open --kind intake` asks it.
- Tickertown runs two timed stages. `intake-author` chooses the next epic from the locked sources
  and creates it in `PRD Needed` (alpha `docs/agents/intake-author.md`). `prd-author` claims that
  epic and writes its PRD (alpha `docs/agents/prd-author.md`). A person reads the PRD first at
  `PRD Sign-off`. Both stages extract from Layer 1 LOCKED user stories in `pm-os-alpha/`, and
  `prd-author` says: "You do not invent any part of it."

**G2: no `--blocked-by` on an epic.** Measured:

- `loop epic create` takes `--architecture`, `--title`, `--body` and `--project` (`loop epic
  create --help`). Only the decomposer writes ticket blockers, through `blocked_by` in the
  decomposition file (`internal/pipeline/decompose.go:30`).
- A blocked ticket waits in `backlog`. The `dependency-advance` job applies `unblock` when each
  blocker landed. Admission never reads beads edges (`internal/sched/deps.go:15`).
- Tickertown's `pnpm intake apply --blocked-by` blocks a new epic on decisions and epics, and fires
  each blocker decision that waits in `Decision Pending` (alpha `scripts/sdlc/intake-apply.ts:81`,
  `scripts/sdlc/intake-fire.ts`).

**G3: a room opens only on a rubric score.** Measured:

- Ticket factorio-4q5 landed the gate of loop fault F-5 and bug factorio-xit. `room open` parses
  `--score` and `--choice`, and refuses levels 1 to 3 with `ROUTED_TO_LOOP`
  (`internal/room/room.go:246`).
- The last intake room, for epic factorio-j4x, asked the operator 11 questions about the product
  with the question tool in 10 minutes. Each one chose among options, such as how a project cap
  relates to the host cap. That is level 4 work.

**G4: the coordinator's route for new work.** Measured:

- `pack/runbooks/coordinator.md` section 8 sends level 1 to 3 work to `loop work create`, and
  level 4 or 5 to an intake room. `.loop/policy/coordinator.md` holds the same route.
- R48 says that the coordinator creates a ticket that skips the PRD "only when the operator
  explicitly asks for one". So level 1 to 3 product work that spans several pull requests has no
  route that keeps PRD-first.

Assumed, not measured:

- That most new work of a loop-managed project is level 1 to 3: the repository, a source it ports,
  or an audit already says what the work does. The nine parity epics of 2026-10-23 are such work.
  No count over more projects exists.
- That a PRD author with no locked source writes requirements a reviewer can check, because each
  one cites the operator's words or a file. No run tried it yet.

## Decision

| Gap | Verdict | Where it lands |
| --- | --- | --- |
| G1 | Port `prd-author`. Do not port `intake-author` | A new run stage and state, and `prd submit` |
| G2 | Port the block. Do not port the fire on block | `epic create --blocked-by`, `dependency-advance` |
| G3 | Keep as it landed | `room open` |
| G4 | Change the route | Coordinator runbook section 8 |

1. **A new unattended run stage, `prd-author`, writes the PRD of an epic that waits in a new
   state, `prd_needed`, and a person reads that PRD first at PRD sign-off.** We reject the intake
   room for all new work, because the operator said that a room opens only when "human
   intervention is truly required". We reject a PRD in the coordinator's chat, because R49 keeps
   that thread undisturbed, and a run has a checkout, a budget and a retry count.

2. **The coordinator scores each request for product work by the rubric. At level 1 to 3 it runs
   `loop epic create --request "<the operator's words>"`, which creates a PRD epic in
   `prd_needed`. At level 4 or 5, or the Choice `outside`, it opens an intake room as it does
   today.** We reject the PRD-less ticket that section 8 now files for level 1 to 3 work, because
   R48 lets only the operator waive the PRD, and work across several pull requests then has no
   epic to decompose. A ticket with `--request` stays for a bug, and for work the operator asks to
   do with no PRD.

3. **The PRD author cites only the epic body, the `request` fact and files of the repository.
   Each story quotes the operator's words from the `request` fact. A requirement with no source
   goes under Open questions, and a PRD the author cannot write goes to an escalation with a
   score.** We reject tickertown's locked-source model, because the loop has no locked source,
   and the plan defers "Intake from a configured source, such as TickerTown's locked sources"
   (plan, Scope Boundaries, "Deferred for later"). We reject a guess in place of a source, because
   no person reads the PRD before the doc-reviewer.

4. **The loop does not port `intake-author`, `pnpm intake next`, `pnpm intake coverage`, the
   order rule or the epic marker.** These are obsolete under the loop's design. The coordinator is
   the one entry for work (plan, Key Decisions, "Work enters through the coordinator"). It creates
   one epic for each request, so nothing chooses scope or order from a source list. We reject a
   port, because the only source list it reads is tickertown's `pm-os-alpha/`.

5. **`loop prd submit` also accepts a `prd-author` run that holds an epic in `prd_drafting`, and
   moves that epic to `prd_review`.** From an intake room it still creates the epic. We reject a
   second verb for the run, because the push, the pull request and the story read in `prdSubmit`
   are one act, and two copies drift. We reject `loop branch push` with `loop pr open`, because
   neither one records the PRD path and the story labels that the decomposer reads.

6. **`prd-author` gets its own throughput entry, and the `intake` entry stays the cap on epics in
   flight. `epic create` of a PRD epic asks the intake cap, and `prd_needed` and `prd_drafting`
   count toward it.** We reject the `intake` stage as the run stage, because one entry would then
   count epics and runs at once. We reject a second cap on epics, because one flow of epics under
   two caps lets both fill. The `prd-author` key is optional, with a default, as `overwatch` is,
   so a host config that an older release wrote still loads.

7. **`loop epic create --blocked-by <item,...>` creates the epic in a new epic state, `backlog`,
   with a beads edge to each blocker. `dependency-advance` applies `unblock` to its entry state,
   `prd_needed` or `architecture`, when each blocker landed.** We reject a dependency in the body
   only, because no stage reads prose. We reject a check of the edges at claim, because admission
   never reads beads edges, and tickets already use the `backlog` rule.

8. **`epic create --blocked-by` fires no decision.** The coordinator is the one caller of
   `epic create`, and section 6 of its runbook already fires each pending decision whose trigger
   happened. We reject a port of tickertown's fire on block, because tickertown needs it for an
   unattended caller that tells no person, and the loop's caller is the operator's interface.

9. **`room open` keeps the rubric gate of factorio-4q5, with no exception for an intake room.** We
   reject an exception for intake, because after decision 2 an intake room serves only level 4 or
   5 work, and the gate already lets that through.

## Consequences

What changes:

- **The epic table.** `internal/machine/epic.go` gains `backlog`, `prd_needed` and
  `prd_drafting`. New rows: `create_prd` from the initial state to `prd_needed`, or to `backlog`
  with blockers. `unblock` from `backlog` to the entry state. `claim` from `prd_needed` to
  `prd_drafting` for `prd-author`. `prd_submit` from `prd_drafting` to `prd_review`. The
  `epicResume` map names where each new state returns.
- **The verbs.** `loop epic create` gains `--request` and `--blocked-by`. With no
  `--architecture` it needs `--request`, and creates a PRD epic. `loop prd submit` gains the run
  caller. The CLI surface regenerates `gen/` with `make proto`.
- **The PRD submit.** `internal/pipeline/prd.go`: `prdSubmit` accepts a run principal of stage
  `prd-author` that holds the epic, and applies `prd_submit` to it.
- **Config and admission.** `internal/config/throughput.go` adds the `prd-author` run stage.
  `internal/admission/caps.go` adds `prd_needed` and `prd_drafting` to `intakeCounted`.
- **The dependency job.** `internal/sched/deps.go` unblocks epics in `backlog`, beside tickets.
- **The pack.** A new runbook, `pack/runbooks/prd-author.md`, named in `pack/manifest.json`. It
  copies the PRD template of `loop-brainstorm`. Coordinator runbook section 8 routes level 1 to 3
  product work to `epic create --request`. `.loop/policy/coordinator.md` changes its route row
  to match.
- **The plan.** The Key Decision "Intake is an interactive brainstorm session that produces the
  PRD", and R44, now hold for level 4 and 5 work only. F4 gains a second entry path.

What does not change: a PRD from either path passes the doc-reviewer and the operator's sign-off
by merge (R46). The first reject goes to the amender, and the second to an intake room (AE7).

What this makes easy: the coordinator sends level 1 to 3 work into the pipeline, and the operator
reads it first at sign-off. Nobody goes to find a room for work the repository already answers.

What this makes hard: the PRD author has no locked source, so a weak `request` gives a weak PRD.
The doc-reviewer is the first reader, and the second reject still costs an intake room. The spec
decides what the PRD author does with a `request` too short to quote as a story.

A later reader must not:

- let the PRD author write a requirement that cites no source.
- open an intake room for work that the rubric scores at level 1 to 3.
- let any caller but the coordinator create a PRD epic.
- port `intake-author`, the order rule, `intake coverage` or the epic marker without a new ADR
  that names a configured source for intake.

Postponed on purpose: what counts as landed for a decision blocker. A ticket lands at merge, and
an epic at `done`. The spec of factorio-4n0 decides whether a settled decision, a moot one, or
both release the block.

## Acceptance criteria

- `loop gates --docs` passes.
- `grep -c '^\*\*Ticket:\*\* factorio-4n0$' docs/adr/0001-unattended-prd-author.md` prints `1`.
- `ls docs/adr | grep -c '^0001-'` prints `1`.
- `grep -c '^| G[0-9] |' docs/adr/0001-unattended-prd-author.md` prints `4`. Every gap has one
  verdict.
- `grep -rln --exclude='*_test.go' -e 'intake-author' -e 'sdlc-intake' -e 'intake coverage' pack
  internal cmd` prints nothing.

Epic factorio-4n0 owns these checks, through its spec and tickets:

- `loop prd submit --help` lists `run` among its callers.
- `loop epic create --help` lists the flags `request` and `blocked-by`.
- `test -f pack/runbooks/prd-author.md` succeeds, and `pack/manifest.json` names it.
- A test proves that `epic create --request` with no blockers creates an epic in `prd_needed`,
  and that a blocker creates it in `backlog`.
- A test proves that `dependency-advance` moves an epic from `backlog` once each blocker landed,
  and leaves it while one is open.
- A test proves that `prd submit` from a `prd-author` run moves the epic it holds to
  `prd_review`, and creates no second epic.
- A test proves that the server refuses `epic create --request` with `CAP_FULL` while the intake
  cap is full.
