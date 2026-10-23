# ADR 0009: A timed server sweep wakes the coordinator, and it finds every filed issue

**Status:** Proposed
**Date:** 2026-10-23
**Ticket:** factorio-a7r

## Context

The operator, 2026-10-23T14:55Z: `fix the coordinator wake. overwatch should find the filed
issues. i know we agreed that other projects would inbox this one, but on second thoughts it
should be subsumed by overwatch. why didnt overwatch make it into this port? it is a critical part
of the sdlc.`

This section marks each fact. **Measured** means read in source or in the store at this commit.
**Assumed** means not measured.

### The loop wakes the coordinator only for a new inbox entry

- **Measured.** `internal/inbox/notify.go` rings once for each inbox entry newer than its cursor.
  `RingText` names each entry by id, kind and awaits. It asks for nothing else.
- **Measured.** `internal/server/inbox.go` calls `Notifier.Poke` on each watch interval and after
  each inbox change. With no new entry, `Poke` rings nobody.
- **Measured.** `pack/runbooks/coordinator.md` section 1 runs four reads on a wake:
  `loop inbox wait`, `loop inbox list`, `loop fault list --status open` and `loop status`.
- **Measured.** The plan dropped the timed wake on purpose. R22
  (`docs/plans/2026-10-21-1328-feat-standalone-sdlc-loop-plan.md:137`) lists `alert-check` and
  `supervisor-wake` as orca and Linear workarounds. KTD21 (`:419`) makes `notify` the one doorbell.
- **Measured.** U15 (`:1057`) says the coordinator runbook comes from `overwatch.md`. No part of
  the overwatch sweep reached the port.

### What that cost

- **Measured.** Loop fault F-4: fixer runs r-332c1c7116a7, r-53f391c45f88 and r-131cf44e5259
  finished `done`. Escalations 1, 2 and 3 stayed `awaiting_panel` the whole time.
- **Measured.** A stalled escalation makes no inbox entry. So no ring fired, and the coordinator
  did not see the stall until the operator asked.

### Tickertown's overwatch

- **Measured.** `scripts/sdlc/overwatch.ts` (alpha) runs 25 parts as one report with one exit
  code. Exit 0 means no part holds work, 1 means work, and 2 means a failed read.
- **Measured.** `docs/agents/overwatch.md` (alpha) section 1 gives the reason for one command.
  On 2026-10-12 a wake skipped `pnpm questions list` and read one stage of `pnpm dispatch` only.
- **Measured.** `scripts/sdlc/supervisor-wake.ts` (alpha) types `Follow docs/agents/overwatch.md.`
  into the resident session. It names no entry.
- **Measured.** `scripts/sdlc/timer-table.ts:98` (alpha) fires `supervisor-wake` at
  `3,13,23,33,43,53 * * * *`. That is six wakes an hour. The runbook still says 30 minutes.
- **Measured.** `scripts/sdlc/alert-check.ts` (alpha) wakes the session only when an unread alert
  exists. It exits 1 when none exists.
- **Measured.** `docs/solutions/2026-10-16-supervisor-wake-prompt-gate.md` (alpha) records three
  wakes that orca refused with `agent_prompt_blocked`. A wake with no acceptance receipt never
  counts as delivered, and it never authorizes a resend.
- **Measured.** `internal/agenthost/orca/notify.go` ports that receipt rule. It returns
  `ErrNotifyRefused` when orca gives no acceptance receipt.

### Filed issues and the earlier agreement

- **Measured.** KTD31 puts every loop fault in one store for the instance. Each new fault adds an
  inbox entry with `awaits: coordinator`.
- **Measured.** A bug report waits in status `reported` in its own project. No inbox entry names
  it. Only the reproducer stage reads it.
- **Measured.** A run reads only the item it holds. This run's `loop work show factorio-a7r` got
  `CAPABILITY_DENIED`, rule `item_not_held`.
- **Measured.** No file in this repository records the agreement that other projects inbox this
  project. The operator's message above is its one written trace.
- **Assumed.** The agreement meant this: a run or a coordinator in another project files each
  loop issue into this project, so that this project's coordinator sees it.

### Not measured

- **Assumed.** One coordinator turn costs more than one server sweep. Nobody counted the tokens.
- **Assumed.** The server can make every ported read with the credentials it already holds. The
  reactor already reads GitHub as the App. Nobody built a part to prove it.

## Decision

**The timed wake.** A native server job, `overwatch`, runs the sweep on its throughput schedule.
It rings the coordinator only when a part holds work or a read failed. We reject a ring on every
tick, as `supervisor-wake` did. That ring costs a model turn when nothing waits. We also reject the
inbox doorbell as the only wake, because F-4 stalled with no inbox entry.

**Who fires it, and how often.** The scheduler fires `overwatch` from its own KTD30 throughput
entry, cap 1, default schedule `3,13,23,33,43,53 * * * *`. We reject a constant in code, because
KTD30 puts every schedule in host config. We reject the 30-minute cadence in `overwatch.md`. A quiet
tick rings nobody, so a ten-minute tick costs no model turn.

**What the wake text asks for.** The ring names each part that holds work, and it asks for the whole
sweep: `loop overwatch sweep`, then runbook section 1. We reject a ring that names inbox entry ids,
as `RingText` does. An entry list hides every stall that made no entry.

**Who reads, and who acts.** The server makes every read, and the coordinator acts on the report.
We reject a list of reads in the runbook, because tickertown's 2026-10-12 wake skipped one of them.

**Which parts port.** The table below sorts all 25 tickertown parts. A part ports when the loop has
the same fault and no other mechanism reads it. We reject a part that reads Linear or orca's timer
table, as R22 directs.

| Part | Verdict | Why |
| --- | --- | --- |
| `stalls` | Port | Work that stopped moving. F-4 is this case |
| `sweep` | Port, inside `stalls` | Lost and stuck work that no queue holds |
| `backlog` | Port | Items held by a blocking root, and the root that holds them |
| `questions` | Port | Escalations and prepared questions, by state |
| `decisions` | Port | A decision whose trigger is an event. `decision-timers` reads dates only |
| `claims` | Port | A lease whose run is dead |
| `pulls` | Port | A ticket that holds two open pull requests |
| `worktrees` | Port | Each checkout against its run, its stage and its item |
| `ci` | Port | CI time on the base branch and in the merge queue |
| `flaky` | Port | A test that failed and passed at one commit |
| `audit` | Port | The dependency audit of the base branch. Project config names its workflow |
| `main` | Port | A red run on the base branch |
| `timer-health` | Port | Each stage's and job's recent ticks. Read from the loop's store |
| `inbox` | Port, as inbox reachability | A coordinator session that runs, and the receipt of the last ring |
| `server` | Port | The installed service, as `loop host doctor` checks the host |
| `bugs` | Port, as filed issues | See the next decision |
| `night` | Tickertown policy | The nightly ingest of one product |
| `frames` | Tickertown policy | The shot harnesses of one product |
| `handoffs` | Tickertown policy | Frame owners that tickertown's ticket template names |
| `watchers` | Tickertown policy | Sentry poll units of one product |
| `board` | Obsolete | `loop inbox list --awaits person` replaces it (KTD21) |
| `checkout` | Obsolete | It is `pnpm refresh`, and R22 drops `refresh` |
| `dispatch` | Obsolete | R22 drops `dispatch`. The scheduler admits each run |
| `timers` | Obsolete | KTD30 config load refuses a bad entry. No orca plan can drift |
| `replies` | Obsolete | It reads a person's reply on a Linear ticket. The loop has no Linear ticket |

**Filed issues.** The sweep reads every open loop fault and every `reported` bug in every
registered project. We reject the agreement that other projects inbox this project. A run cannot
write to an item outside its own project. A filing that one project forgets loses the issue with no
trace.

**What overwatch does with a finding.** The coordinator scores each finding by `loop-sdlc-triage`.
At levels 1 to 3 it acts, or it files a fault or a bug. At levels 4 and 5 it prepares an inbox
question for the operator. We reject tickertown's rule that the overwatch merges its own machinery
fix. `loop-sdlc-turn` section 8 says that no agent merges. We also reject a page for each finding,
because a page must be earned.

## Consequences

This supersedes the agreement that other projects inbox the loop's own project. A project files a
loop fault with `loop fault report`, and a bug in its own project. The sweep finds both.

What changes:

- **A native job.** `internal/config/throughput.go` adds `overwatch` to `nativeJobs` and to
  `DefaultThroughput`. Config load refuses a host config with no `overwatch` entry. So
  `loop host upgrade` must add the default entry, or the operator must add it first.
- **A server package.** A new package holds one function for each ported part. Each part returns
  a code and its lines, with tickertown's exit convention. The job and the verb call the same code.
- **A verb.** `loop overwatch sweep` prints the report as one envelope, for the coordinator and the
  host. The CLI surface regenerates `gen/` with `make proto`.
- **The ring.** `internal/server` rings with fixed server text. The text names parts and holds no
  agent text, as KTD21 requires. `internal/inbox/notify.go` keeps its doorbell for new entries.
- **A failed ring.** An unaccepted ring is never resent. The next tick sweeps again, and the
  `inbox` part reports the failed receipt.
- **The coordinator runbook.** `pack/runbooks/coordinator.md` section 1 runs `loop overwatch sweep`
  first. A new table maps each part to the section that acts on it. Sections 3 and 9 act on parts.
- **The glossary.** `pack/skills/loop-sdlc-glossary` defines overwatch, sweep and part.
- **Project policy.** A project's `.loop/policy/coordinator.md` may name added checks.
  Tickertown's four policy parts go there.

What this makes easy: a stall with no inbox entry reaches the coordinator within one tick.

What this makes hard: each ported part is code that the loop maintains. A part with no fault behind
it costs reads on every tick.

A later reader must not:

- make the coordinator compose the stall list from its own reads. The sweep is the one producer.
- ring with entry ids as the only wake.
- resend a ring that orca did not accept.
- ask another project to file loop issues into this project's inbox.
- port a tickertown policy part into the loop's code.

Postponed on purpose: the cadence of each part. The spec of factorio-a7r decides whether a slow
part, such as `audit`, reads on fewer ticks.

## Acceptance criteria

- `loop gates --docs` passes.
- `grep -c '^\*\*Ticket:\*\* factorio-a7r$' docs/adr/0009-overwatch.md` prints `1`.
- `ls docs/adr | grep -c '^0009-'` prints `1`.
- `grep -rlE 'inbox (this|the loop.s own) project' pack .loop/policy docs --include='*.md' | grep -v '^docs/adr/0009-'`
  prints nothing. No document keeps the superseded agreement.
- `grep -c '^| `' docs/adr/0009-overwatch.md` prints `25`. Every tickertown part has one verdict.

Epic factorio-a7r owns these checks, through its spec and tickets:

- `go test ./internal/config/...` proves that `ThroughputKeys` holds `overwatch`.
- A test proves that a sweep with an `awaiting_panel` escalation and no inbox entry rings once.
- A test proves that a quiet sweep rings nobody.
- `loop schema` lists `overwatch sweep`.
