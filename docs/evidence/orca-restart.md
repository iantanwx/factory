# Orca restart: can a run be read again after orca restarts?

**Unit:** U7 (agent-host interface and orca implementation)
**Measured:** 2026-10-21, on the devbox, with orca 1.4.232. The operator approved the restart. The orchestrator ran it.
**Result:** An orca-serve restart kept a terminal-hosted run's identity, liveness and process. AE2 holds for orca-serve restarts of terminal-hosted runs. An orca daemon restart was not measured. A later read-only review of an earlier daemon death is in "Orca updates and daemon death" below.

## Question

The U7 execution note asks this before re-adoption is built on it. Start a run through orca, restart orca, and then try to read the run's identity, liveness and settlement again. A run's identity is host + terminal handle + incarnation (`agenthost.RunID`).

## Procedure

1. Start a probe terminal in an existing checkout. The command writes a heartbeat file every second:

   ```sh
   orca terminal create --worktree path:<existing orca worktree> \
     --title loop-restart-probe \
     --command 'while true; do date +%s > /tmp/loop-orca-probe/beat; sleep 1; done' --json
   ```

2. Record the identity from the create receipt: `result.terminal.handle`, `result.terminal.ptyId`, `result.terminal.incarnationId`, `result.terminal.executionHostId` and `_meta.runtimeId`.
3. Read the terminal before the restart: `orca terminal show --terminal <handle> --json`.
4. Restart orca-serve: `systemctl --user restart orca-serve.service`. Record the unit's MainPID before and after the restart.
5. Read the same handle again: `orca terminal show --terminal <handle> --json` and `orca terminal read --terminal <handle> --json`.
6. Confirm that the heartbeat file still advances.

The raw receipts are in `/tmp/loop-orca-probe/` (`create.json`, `show-before.json`, `show-after.json`, `result.md`). Synthetic copies of these receipts, with every real id, path and number replaced, are U7 test fixtures in `internal/agenthost/orca/testdata/probe_*.json`. `TestRestartProbeReadopts` replays them.

## Observations

| Read | Before the restart | After the restart |
| --- | --- | --- |
| orca-serve MainPID | pid A | pid B (changed) |
| `_meta.runtimeId` | runtime R1 | runtime R2 (changed) |
| `terminal.handle` | handle H | same |
| `terminal.ptyId` | pty P | same |
| `terminal.incarnationId` | incarnation I | same |
| `connected`, `orphaned`, `writable` | true, false, true | true, false, true |
| `terminal read` status | running | running |
| heartbeat file | advancing | still advancing |

The restart happened at about 15:40:54 +08.

The restart also had a side effect. It killed the orca native chat session that ran the orchestrator, and every in-process subagent of that session (Bash exit 144). Orca resumed the chat transcript afterwards, but the in-flight work was lost. The probe terminal was a PTY that the orca daemon owns, and it lost nothing.

## Conclusions

1. **AE2 holds for orca-serve restarts of terminal-hosted runs.** A complete inventory after the restart lists the same handle and incarnation, so an exact identity match re-adopts the run. `Inventory.Lookup` returns `present`, and `Inventory.Readopted` moves the session's recorded epoch to the new runtime id.
2. **Every run, room and coordinator must start as a terminal session.** It never starts an orca native chat session, which did not survive the restart. The orca implementation starts each session through `orca agent launch --surface terminal`, which overrides the operator's Chat UI setting, and it closes any session whose launch reports another surface. (It first used `orca terminal create --command`, but orca then did not treat the process as an agent and gave it none of its harness flags, so a harness whose settings deny shell commands stopped at its first one.)
3. **A changed runtime id alone is no evidence of a lost run.** The runtime id changed, and the run did not. The implementation never reads a run as exited or absent because the epoch changed.
4. **An orca daemon restart was not measured.** The PTY belongs to a separate daemon process, and only orca-serve restarted. Until a measurement proves otherwise, the implementation treats the daemon-restart case as `unknown`.

## What the implementation does

- `Settle` reads `terminal show` on the recorded handle. It needs the same handle, incarnation and execution host. A stale handle (`terminal_handle_stale`), another incarnation, a failed read, malformed JSON or a missing CLI each settle `unknown`, never `exited`. Only a `terminal wait --for exit` success proves an exit.
- `Inventory.Lookup` returns `present` on an exact identity match, whatever the epoch. It returns `absent` only when all of these hold:
  - the inventory is complete: not truncated, the total equals the rows, no host omitted, every row has a handle and an incarnation, and a runtime id is present
  - the inventory's epoch equals the epoch at which the server last saw the session
  - no row carries the session's incarnation or its PTY under another handle
- A session whose epoch changed reads `unknown` until a complete inventory of the new epoch reads its exact identity again. The server then stores the result of `Inventory.Readopted`, and a later absence in that epoch can read `absent`.
- A row that carries the session's incarnation or PTY under a new handle (`Inventory.Traces`) keeps the session `unknown`. This is the daemon-restart case that nothing has measured. KTD9 then keeps the lease for two run budgets and raises a coordinator-owned alert. Nothing reclaims a traced run automatically.
- A session whose terminal died with an orca restart never matches again, so it reads `unknown` in every later epoch. Past the age bound of two run budgets, `Inventory.Untraced` reads a complete inventory of any epoch: when it lists neither the session's exact identity nor a trace, the server settles the run through `settleEnded` as `crashed`. The run counts once, its lease returns to the stage-correct state, its checkout stays, and the crash alert is raised. An incomplete or failed inventory read settles nothing. The operator decided this on 2026-10-24 ("Reclaim if no trace", loop fault F-21).

## Orca updates and daemon death (read-only review, 2026-10-21)

A read-only review on 2026-10-21 found these facts. Nobody restarted, stopped or updated orca for it. Paths are on the devbox.

| Fact | Source |
| --- | --- |
| Orca's own auto-update is off (`orca status --json` reports `automatic: false`). A box cron job, `/etc/cron.d/agentbox-orca-nightly`, runs `~/.dotfiles/bin/orca-update` at 00:01 +08. It stops `orca-serve.service`, swaps the install directory, starts the unit and health-checks it for 30 s. Downtime was about 6 s on 2026-10-21. | The cron file, the update script, `~/.local/state/orca-update/nightly-*.log`, and the `orca-serve` journal. |
| The terminal daemons (`daemon-entry.js`, one for each protocol version, socket `~/.config/orca/daemon/daemon-v<N>.sock`) run in their own transient systemd scopes, outside the `orca-serve` cgroup. An update never restarts them. A protocol bump starts a new daemon for new terminals, and orcad reconnects to every old daemon. An old daemon exits only when it holds no session. | `systemctl --user status orca-serve`, the scope list, `~/.config/orca/logs/daemon.log*` (`client-hello-accepted` from every daemon after an update, then `session-attached`; `shutdown reason:"idle"`), and a shell that has lived on one daemon since 2026-09-23 through about 15 updates (`orca terminal list`). |
| Native chat sessions are children of orcad inside the `orca-serve` cgroup, so every update kills them. | `systemctl --user status orca-serve`, and the chat loss in "Observations" above. |
| On 2026-10-03 the kernel OOM killer killed a terminal daemon twice. Orca then re-created the same PTY ids on a new daemon, each with a fresh, empty shell (`hasCommand:false`). The old processes were gone. | The kernel OOM record and `daemon.log*`. |

Still unknown: whether orca gives a terminal a new incarnation after a daemon death.

What the implementation does with this:

- A row with the same handle and another incarnation is never the run. `Settle`, `Ended`, `Notify` and `Stop` read the handle as stale, so liveness reads `unknown`, nothing reads ended, and no input or close reaches the fresh shell. `Inventory.Lookup` never reads `present`, and `Inventory.Readopted` never re-adopts the row.
- When the run recorded the PTY, the same PTY under the new incarnation is a trace, so `Lookup` reads `unknown`. When the run recorded no PTY, a complete inventory of the same epoch reads `absent`.
- If orca keeps the same incarnation on the fresh shell, the identity matches, and the run reads `present` although its process died. Only the transcript and quiet-period rule of KTD8 then ends the run. No measurement covers this case.
- `TestDaemonDeathNewIncarnationIsNotTheRun` in `internal/agenthost/orca` and in `internal/agenthost/fake` proves the first two points. `fake.Host.DaemonDeath` replays the event.

## Still unmeasured

- **An orca daemon restart.** To measure it, repeat the procedure, but restart the process that owns the PTYs instead of `orca-serve.service`. The result supports re-adoption only if the same handle and incarnation answer `terminal show` afterwards. If the process survives under a new handle, or if it dies, the implementation's `unknown` reading stays correct. Re-adoption by incarnation would then need its own rule.
- **A host reboot.** A reboot kills every PTY. U8 classes such runs from the server's outage record (KTD8 `infra`), and not from the agent host.
- **Exit visibility for an `exec`-started harness.** Nothing measured whether `terminal wait --for exit` still answers for a terminal whose process exited, or whether orca drops the terminal at once. If orca drops it at once, `Settle` reads `unknown`, and the exit shows only as `absent` on a complete inventory. The build-tagged contract run (`go test -tags orca`) exercises this path.
