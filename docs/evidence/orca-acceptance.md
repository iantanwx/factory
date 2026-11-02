# Orca acceptance lane: the first green run

**Unit:** factorio-9ac (ADR 0008), contracts C13 and C23
**Date:** 2026-10-27, the `orca-acceptance` tick of 17:30 UTC (2026-10-28 01:30 +08). The job wrote the log at 17:34:43 UTC.
**Orca:** 1.4.242
**Loop head:** v0.0.177, commit `cbcae052ef3d0fa24c9b97452b9d64d798b582d5`
**Sessions:** 13 declared by the lane plan, against the cap of 20
**Log:** `/home/operator/.local/state/loop/orca-acceptance/logs/20261027T173443.897Z.log`
**Result:** Green. Both lane binaries exited 0, and F1 passed. The job raised no fault.

## How the run was made

The native job `orca-acceptance` ran the lane on its default schedule, on the devbox. Nobody ran
`make orca-acceptance` by hand. The job fetched the lane binaries of v0.0.177, made the fixture
`~/.local/state/loop/orca-acceptance/fixture` and registered it with orca. It then ran
`orca-contract.test` and `orca-acceptance.test` as `docs/operating.md` ("The orca acceptance lane")
states.

## The log

```
fetch the lane binaries of loop v0.0.177 for linux/amd64
made the lane fixture /home/operator/.local/state/loop/orca-acceptance/fixture
registered the lane fixture with orca
== orca-contract.test exit 0 ==
quota: claude quota unavailable: Claude Code sends its requests to ANTHROPIC_BASE_URL, and orca reads only the logged-in account
fake-only: incomplete_inventory_never_absent needs InventoryBreaker
fake-only: never_ready_yields_not_ready needs ReadinessBreaker
fake-only: quiet_after_ended_turn_reads_ended needs TurnDriver
fake-only: tool_call_running_never_ended needs TurnDriver
fake-only: recorded_mismatch_refuses_start needs ModelDropper
fake-only: missing_receipt_starts_session needs ModelSilencer
fake-only: notify_refused_is_an_error needs NotifyRefuser
PASS
== orca-acceptance.test exit 0 ==
quota: claude quota unavailable: Claude Code sends its requests to ANTHROPIC_BASE_URL, and orca reads only the logged-in account
PASS
swept the worktree /home/operator/orca/workspaces/fixture/loop-demo-code-reviewer-demo-3wb-add-a-greeting-r-f08b1e4d577a
swept the worktree /home/operator/orca/workspaces/fixture/loop-demo-implementer-demo-3wb-add-a-greeting-r-8df339c2f8e7
```

## What each field rests on

| Field | Source |
| --- | --- |
| Date | The log's name, which the job takes from the clock when it writes the log |
| Orca | The nightly update log `~/.local/state/orca-update/nightly-20261028.log` moved orca from 1.4.241 to 1.4.242 at 00:01 +08, before the tick. `orca --version` printed `1.4.242` after the run. The log itself does not name the orca version |
| Loop head | The log's first line names v0.0.177. `git rev-parse v0.0.177^{commit}` prints the commit |
| Sessions | `LanePlan` at v0.0.177 totals 13: 10 for the contract scenarios that run on orca, 1 for `TestOrcaAgentRunsShellCommands` and 2 for F1. `TestEveryLaneTestDeclaresItsSessions` asserts the total. Each real test refuses a start past its declared count, so the run started 13 sessions or fewer |
| F1 passed | The scenario binary runs only `^TestF1RealAgentsTakeATicketFromClaimToMerge$`. It exited 0 and printed `PASS`, with no `no tests to run` line. F1 has no skip path. It asserts `done`, one merge at the reviewed head, and exactly 2 stage sessions with no coordinator |

## Notes

- The quota gate could not read the Claude quota, because Claude Code on this host sends its
  requests to `ANTHROPIC_BASE_URL`. The gate prints the reason and goes on, as the spec states. The
  lane plan bounds the spend.
- The swept worktrees are the F1 implementer's and code-reviewer's. Their Claude Code transcripts
  ran from 17:32:00 to 17:33:08 UTC and from 17:33:16 to 17:34:38 UTC.
- v0.0.177 holds every ticket of the epic except factorio-9o2q (PR #209, first released in
  v0.0.185). That ticket gives the F1 fixture config every stack gate, and adds an acceptance test
  of it. F1 passed on v0.0.177 without it. The coordinator named this log as the record in a note
  on factorio-z6wg.
- F1 has run a third session since factorio-t9i2. PR #368 turned the simplify stage on by default,
  and the lane log of 2026-11-02T17:32Z on v0.0.325 showed F1 stop with its ticket in
  `in_simplify` (loop fault F-74). F1 now dispatches a real simplify run after the implementer, and
  the lane plan declares 3 sessions for it. This record of v0.0.177 still holds for that release.
