# Beads contract evidence (U3)

Date: 2026-10-21. Host: the devbox (linux/amd64).

## Pinned binaries

| Binary | Version | sha256 |
| --- | --- | --- |
| `bd` | 1.3.1 (c1c4b642a) | `a8f48d771b9e11eccfced4aed72923b59eda746f7a9b425e7ca1af7a51251653` |
| `dolt` | 2.2.0 | `72352f1d882472a7a8d6124e9c4dc12f80e2c2359ea95f1424c032e9250e35e6` |

The adapter refuses any other version. `NewBD` refuses a `bd` whose
`version --json` is not exactly `1.3.1`. `Supervisor.Start` refuses a `dolt`
whose `dolt version` is not exactly `2.2.0`. Stub binaries prove both
refusals (`TestNewBDRefusesOtherVersion`, `TestCheckDoltVersionRefusesOther`).

## How to run

- Fake and stub tests: `go test ./internal/beadsx/...`
- Real binaries: `go test -tags beads ./internal/beadsx/...`. `LOOP_BD` and
  `LOOP_DOLT` override the paths. Each test starts its own Dolt on a socket in
  its own temp state directory, and it stops that Dolt by its own PID.

## Atomicity (the execution note and stop condition 1)

This test ran first, before any adapter code: `TestFirstAtomicityRaw` drives
the raw binaries. Result: PASS. One `bd update <id> --if-status open --status
implementing --metadata '{"loop.seq":"1","loop.key":"k1"}'` set the custom
status and both keys. A read showed all three. A second update with a stale
expected status exited 13, and the read after it showed no change.

The same result on the adapter, on the real binaries:

- `AtomicStatusAndMetadata`: PASS. One conditional update sets a custom status,
  `loop.seq` (the KTD6 intent sequence) and `loop.key`. A read shows all three.
- `ConcurrentUpdatesOneWinner`: PASS. 8 concurrent updates from one expected
  status gave one winner. Each of the other 7 got `ErrCASMiss` (exit 13), and
  no loser's metadata is present. The orchestrator's earlier manual probe gave
  15/15 races with one winner each.
- `MissWritesNothing`: PASS. A miss leaves status, metadata and the journal
  unchanged.

## Durability after SIGKILL (stop condition 2)

`TestDurabilityAfterSIGKILL`: PASS, 10 of 10. Each round makes one conditional
update, then sends SIGKILL to `dolt sql-server` as soon as `bd update` returns,
then waits for the supervisor's restart. After each restart, the bead shows
the new status and intent sequence. The orchestrator's manual probe gave the
same result (10/10). No Dolt setting was changed for this: the default
`dolt.auto-commit=on` applies.

## Exit codes and errors

- Exit 13 means "every failure is a stale `--if-status` or `--if-assignee`
  guard. Nothing was written". The adapter maps it to `ErrCASMiss`, and only
  exit 13 maps to it.
- An unknown id exits 1 with "no issue found matching". The adapter maps it to
  `ErrNotFound`. `Update` resolves the id before the guard, so an unknown id is
  never a miss.
- A stopped server exits 1 with "Dolt server unreachable ... connection
  refused" in about 60 ms. bd does not retry and does not start a server in
  socket mode. The adapter maps it to `ErrUnavailable`. A cancelled or expired
  context, and a bd killed by a signal, also map to `ErrUnavailable`, because
  the result is unknown. The caller re-reads the bead before it decides.
- `TestRestartGapIsUnavailable`: PASS. The test kills the server. An update and
  a read during the restart gap both return `ErrUnavailable`, never a miss.
  The supervisor then restarts the server and sends one `RestartEvent`. The
  same update then succeeds.
- Malformed output (not JSON, a wrong `schema_version`, a wrong shape, a
  journal line out of order) returns `ErrMalformed` and never panics.
- A cancelled context kills bd and its whole process group
  (`TestContextCancelKillsBD`).

## JSON envelope

`BD_JSON_ENVELOPE=1` makes every `--json` output one object:
`{"data": ..., "schema_version": 1}`. Errors appear as `data.error`, on stdout
or as the last line of stderr. The adapter sets it on every call and refuses
any `schema_version` other than 1. `bd events tail` is the exception: it
writes JSON lines with no envelope.

## Metadata

- `--set-metadata k=v` parses each value as JSON when it can. `1` becomes the
  number 1, `true` becomes a boolean, and `"009"` keeps its quotes. So the
  adapter never uses it.
- `--metadata '<json object>'` merges keys into the stored object and keeps
  string values exact. The adapter writes every set as a JSON string.
- bd refuses `--metadata` together with `--unset-metadata`. So the adapter
  writes an unset as JSON `null` in the same `--metadata` object, and every
  read drops a null key. The test `MetadataStringsMergeAndUnset` proves that
  `1`, `007`, `true`, `1.50`, an empty string and a JSON text round-trip
  exactly, and that the unset key reads as absent. Warning:
  `bd list --has-metadata-key` still matches a null key. The adapter does not
  use that flag.

## Second guard: metadata value (KTD6 step 2)

`bd update` has no guard on a metadata value. Its only guards are
`--if-status` and `--if-assignee`. The two combine in one write: when either
fails, bd writes nothing and exits 13 (`AssigneeGuard`: PASS on both
implementations). So the epoch can be fenced in the same write only if U5
stores it in the assignee field (for example `epoch:7`). The adapter exposes
`UpdateSpec.IfAssignee` and `UpdateSpec.Assignee` for this. The adapter never
uses `--claim`, so no lease protects the assignee. U5 decides whether to use
the assignee as the fence. If it does not, the per-item lock stays the only
epoch guard at step 2.

## Events: two journals, and ordering

bd 1.3.1 has two event records:

1. The audit events of `bd history <id> --events`. Event ids are UUIDs, and
   `created_at` has 1-second resolution. They give no total order within one
   item. The orchestrator's probe found this, and it is true.
2. The durable events journal of `bd events tail` (table
   `bd_events_journal`). It is off by default. `EnsureWorkspace` turns it on
   (`events-journal true`). Each record carries `seq`, which bd documents as
   "gapless, strictly increasing in commit order, never reused". Each record
   holds the full issue after the mutation, metadata included. Retention is 7
   days or 100,000 rows by default. A read below the retained floor fails with
   `events_journal_truncated` (the adapter maps it to `ErrJournalTruncated`).

The adapter reads journal 2. On it, the plan's scenario holds as written:
`JournalRecordsOneUpdateOnce` (PASS) finds the update with one idempotency key
in exactly one record, with strictly increasing seq. A miss adds no record.
KTD6 still decides "applied" from the bead's `loop.seq` (at or above the
intent's own), and uses the journal only as a second signal. R30 delivery can
checkpoint on `seq`. Warning: the journal is per replica and per branch, and
`bd sql` writes are not journaled.

## Lookup by idempotency key

- Current metadata: `bd list --metadata-field loop.key=<key>` finds the issue
  whose current metadata holds the key (`Filter.Metadata`).
- History: the journal has no filter by issue or by key. `Events(id)` reads
  the whole retained journal and keeps one issue's records, and a caller then
  scans `Issue.Metadata`. This costs one full journal read. It is enough for
  divergence alerts, and too slow for a hot path.
  `LookupByIdempotencyKey`: PASS.

## Custom statuses and types

`bd config set status.custom "name:category,..."` (categories `active`,
`wip`, `done`, `frozen`) and `bd config set types.custom "a,b"`. bd validates
each name against `[a-z][a-z0-9_-]*`, refuses a collision with a built-in, and
refuses an unknown category. The adapter and the fake apply the same rules.
`CustomStatusesAndTypes`: PASS. It walks one issue through five custom
statuses and creates one issue of each custom type.

## Telemetry

bd 1.3.1 turns anonymous usage metrics on by default. On its first run it
writes `metrics.disabled: false` and the endpoint
`https://gastownhall-eventsapi.com/mp/collect` into
`$HOME/.config/bd/config.yaml`, and it queues events under `$HOME/.beads/`.
It reads `XDG_CONFIG_HOME` when that file exists there, but it writes under
`$HOME/.config`. Dolt 2.2.0 also writes usage events under its root
(`~/.dolt/eventsData`), and it checks online for a newer version.

How the adapter neutralizes it:

- Every bd call gets an environment built from nothing but `PATH`. `HOME` and
  `XDG_CONFIG_HOME` point at `<state>/beads/home`.
- `EnsureHome` writes `metrics.disabled: true` into that home before the first
  bd call.
- Every bd call also sets `DO_NOT_TRACK=1`. bd honors it: `bd metrics` reports
  OFF and writes no file.
- The guard refuses a workspace whose effective `metrics.disabled` is not
  `true`, or whose home config does not turn metrics off (`Metrics`: PASS).
- The supervisor writes `metrics.disabled` and `versioncheck.disabled` into
  Dolt's global config under `<state>/beads/dolt/root`, and it sets
  `DOLT_ROOT_PATH` there with `DOLT_DISABLE_EVENT_FLUSH=1`. A probe showed no
  event file after a server run.

Disclosure: before the knobs were known, two manual probes ran bd with metrics
on in a throwaway home (one `bd init`, one `bd metrics`). Each may have sent
one anonymous event. The user-global `~/.config/bd/` was not touched.

## Other environment knobs

- `BEADS_ACTOR=loop` sets the audit actor (the `--actor` help names it).
- `BEADS_DIR=<workspace>/.beads` selects the workspace. The adapter also runs
  bd with the workspace as its working directory.
- `BEADS_DOLT_AUTO_START=0` and config `dolt.auto-start false`. bd states that
  it never starts a server in socket mode.
- `BD_NON_INTERACTIVE=1`, `BD_NO_PAGER=1`, `NO_COLOR=1`,
  `BD_NO_DEP_TYPE_WARNING=1`.

## Workspace

`EnsureWorkspace` creates `<state>/beads/ws/<project>` with `bd init --server
--external --server-socket <sock> --prefix <project> --non-interactive -q
--skip-agents --skip-hooks --role maintainer`. `--skip-agents` stops
AGENTS.md, CLAUDE.md, `.agents/` and the Cursor files (`TestWorkspaceIdempotentAndIsolated`
checks that they are absent). bd init still writes a git repository and a
`.gitignore` into the workspace directory. bd warns on every call until git
config `beads.role` is set, so the workspace sets it in its own repository.
The prefix is the project id, and it also names the project's database on the
shared server. Two projects on one server do not read each other's issues. A
second `EnsureWorkspace` keeps the data. `metadata.json` records the socket
path. The adapter never uses the port.

## Guard

bd 1.3.1 has no setting that turns on a timed resume or a lease reclaim. A
defer with a date wakes to `open` on the next ready read, and `bd reclaim`
reverts a stale claim to `open`. So the guard refuses the state that arms
them, and the adapter never defers, claims, reads `bd ready` or reclaims. The
guard refuses:

- an issue that is `deferred` or has `defer_until` (`DeferUntil`: PASS),
- an issue with `lease_expires_at`, which only `--claim` makes
  (`LeaseReclaim`: PASS),
- `export.auto`, `import.auto`, `backup.enabled` or `dolt.auto-push` turned on,
  or `events-journal` turned off (`AutoExport`: PASS),
- a Dolt remote or `sync.remote` (`DoltRemote`: PASS),
- metrics on (`Metrics`: PASS),
- a workspace that is not in server mode.

## Dolt supervision

- Dolt 2.2.0 always opens a TCP listener and refuses port 0. The supervisor
  binds 127.0.0.1 on a free port beside the socket. Warning: the root user has
  no password, so any local user can reach that port. U4 and U16 should
  assume a single-user host, or add a Dolt user.
- After a SIGKILL, Dolt leaves its socket file. A new server then logs "unix
  socket set up failed: file already in use" and serves TCP only. The
  supervisor removes the stale socket before each start of its own process,
  and `Start` refuses a socket that another live process serves.
- The health check reads the MySQL handshake on the socket. A server that
  fails 3 checks in a row is killed by its PID and restarted. Backoff starts at
  200 ms and doubles up to 30 s.
- `Stop` sends SIGTERM to its own PID, then SIGKILL when the context ends.

## Dependencies

`bd dep add <a> <b>` with an id of another prefix succeeds and stores a
cross-repo edge. The adapter checks that the target exists in the project
first, so an unknown target is `ErrNotFound`.
