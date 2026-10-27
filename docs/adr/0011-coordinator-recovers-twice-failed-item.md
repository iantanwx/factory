# ADR 0011: The coordinator recovers a twice-failed item once, and no fixer recovery is built

**Status:** Proposed
**Date:** 2026-10-27
**Ticket:** factorio-1okx

## Context

ADR 0006 decision 5 gave each twice-failed item one unattended recovery in the fixer stage. On
2026-10-27 the operator split that decision out of epic factorio-jiv into this epic (question/6:
"Split (Recommended)"). ADR 0006 names factorio-jiv on its Ticket line, so this epic needs an ADR
of its own. This ADR amends ADR 0006 decision 5. By the rule of `loop-sdlc-documents`, the later
ADR wins a conflict.

This section marks each fact. **Measured** means read in source at `3ba50ff`. **Assumed** means
not measured.

### Why an item waits on the operator today

- **Measured.** `internal/admission/failures.go:12` sets `FailureLimit = 2`. Two counted failures
  at the same inputs hold the item, and only changed inputs admit a new attempt
  (`internal/admission/policy.go:391`, rule `retry_limit`).
- **Measured.** `pack/runbooks/coordinator.md:125` answers the `retry_limit` alert. It tells the
  coordinator to run `loop work edit` "with the operator's text". That phrase is the only step
  that brings the item to the operator.
- **Measured.** `pack/runbooks/coordinator.md` section 4 changed on 2026-10-27, after ADR 0010.
  The coordinator asks the operator first only when an act cannot be undone, spends money, changes
  production data, or needs a person. "Every other act is yours." A `loop work edit` can be undone
  by a second edit, and it is none of the four.
- **Measured.** `loop schema` lists `work edit` and `work note` for the `coordinator` principal.

So the reason that ADR 0006 gave to reject the coordinator, "because it brings each item to the
operator", no longer holds. Section 4 now tells the coordinator not to bring it.

### What the fixer recovery would cost

- **Measured.** Work entries 4 to 8 of `docs/plans/factorio-jiv-run-integrity-spec.md` at
  `b74c8bb3a0` build decision 5. They add a `recoveries` table, a `loop work recover` verb, a
  fixer allowance in `holdsItem`, `borrowed` and `escalation raise --item`, a sweep on the tick
  with four states, and two runbook sections. They touch about 15 files.
- **Measured.** The migration number that spec names, `0009`, is now taken by
  `internal/store/migrations/0009_alert_findings.sql`.

### What bounds a recovery

- **Measured.** `work note` stores only the last note on the item. `internal/machine/rows.go:23`
  defines `FactNote` as "the last note", and `notePut` overwrites it. A later run's note replaces
  the coordinator's note.
- **Measured.** No verb lists the history of one item. `loop schema` holds `run events` and no
  item events verb.

### What this room could not read

- **Not measured.** The number of `retry_limit` alerts. A room gets `CAPABILITY_DENIED` on
  `loop inbox list` and on `loop run list`. So the rate of twice-failed items is unknown.
- **Assumed.** The coordinator can find a better task from the item, its body and the failed runs
  that the alert names. ADR 0006 assumed the same of the fixer.

## Decision

The coordinator recovers each twice-failed item once, with its own text, and asks the operator only
when that recovery fails. The operator chose this on 2026-10-27: "Coordinator, once (Rec.)".

1. **The coordinator recovers the item.** On a `retry_limit` alert, the coordinator reads the
   failed runs, edits the item with `loop work edit` in its own text, and answers the alert with
   what changed. When a merged change or a ruling leaves a ticket with no work, it closes the
   ticket with `loop work supersede` instead.
2. **One recovery for each item, ever.** The coordinator records the recovery on the item, in a
   place that a later run's note cannot overwrite. When the item reaches a second `retry_limit`,
   the coordinator brings it to the operator, as today. The spec names the record.
3. **No fixer recovery.** The fixer keeps its one job, the escalation panel.

What I rejected:

- **The fixer stage, as ADR 0006 decision 5 chose it.** It needs about 15 files and a new state
  machine, a verb and a sweep. Section 4 of the coordinator runbook already allows the same edit
  with no new code. Its gain is a run budget and a transcript for each recovery. The unknown rate
  of `retry_limit` alerts does not show that the gain is worth the build.
- **The coordinator with a server bound.** The server would record each recovery edit and refuse
  a second one on the same held item. It costs about 3 to 5 files. The operator chose the runbook
  bound alone. So the one-recovery rule rests on the coordinator runbook, and no gate refuses a
  second edit.
- **No recovery.** Each twice-failed item would still wait on the operator. That contradicts
  section 4 of the coordinator runbook.

## Consequences

Files and commands that change, for the spec of epic factorio-1okx:

- `pack/runbooks/coordinator.md`: the `retry_limit` step drops "with the operator's text". It
  tells the coordinator to recover the item once in its own text, to record the recovery on the
  item, and to bring a second `retry_limit` on that item to the operator.
- The record of the recovery: the spec chooses it. It must survive a later `loop work note`, so
  the last-note fact alone does not hold it.
- No change to `internal/admission`, `internal/sched`, `internal/server`, `internal/machine`,
  `internal/escalation` or `internal/store/migrations`. No change to `pack/runbooks/fixer.md`.

What this makes easy: a twice-failed item gets a second task with no new code, and it reaches the
operator only after one recovery fails.

What this makes hard:

- No gate proves the bound of one recovery. A coordinator that skips its runbook can edit an item
  again, and each edit makes new inputs. Then the failure limit does not bound the attempts on
  that item.
- Each recovery spends the coordinator's context, and it waits for the coordinator's next wake.

Postponed on purpose:

- A server bound on recovery edits. A coordinator that edits one item twice forces it. A loop fault
  or an operator report of that event is the trigger.
- A fixer recovery. A rate of `retry_limit` alerts that loads the coordinator forces it. The
  coordinator can count them with `loop inbox list`.

A later reader must not:

- Reset the failure count of an item, or give an item a second recovery without the operator.
- Bring a first `retry_limit` to the operator when an edit can recover it.
- Add a `loop work recover` verb or a `recoveries` table without an ADR that amends this one.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `grep -rn 'work recover' pack internal` prints nothing.
- `grep -rln 'recoveries' internal/store/migrations` prints nothing.
- `grep -n 'factorio-1okx' docs/adr/0011-coordinator-recovers-twice-failed-item.md` prints the
  Ticket line.
- `loop gates --docs` passes.

These criteria wait on later work, and epic factorio-1okx owns each one:

- `grep -n "operator's text" pack/runbooks/coordinator.md` prints nothing.
- `grep -n 'retry_limit' pack/runbooks/coordinator.md` prints a step that names one recovery and a
  second limit that goes to the operator.
- `make pack-check` passes.
