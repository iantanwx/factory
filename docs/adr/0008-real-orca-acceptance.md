# ADR 0008: Prove the agent host and one real-agent flow against real orca in a daily native job

**Status:** Proposed
**Date:** 2026-10-24
**Ticket:** factorio-9ac

## Context

The operator wrote on 2026-10-23: "i expect full functional equivalence with acceptance tests
against a real orca backend". The epic asks four questions. Which scenarios run against real orca?
Where do they run, in CI or as a host job? How is quota spend bounded? How does each parity epic
run its acceptance criteria against real orca?

This run read tickertown, the repository at `/home/operator/projects/alpha`, on 2026-10-24 at the
head of its main branch. It read the loop at the base `0d6a85f`. On this host's release, v0.0.22,
`loop work show` refuses an implementer run that reads its epic (loop fault F-8). So this run read
the epic body from the `loop epic create` script in the coordinator's scratch directory.

**Gap 1: the acceptance suite never meets real orca.** Measured in source:

- `make acceptance` (`Makefile:51-57`) runs the real server over real beads. The world holds the
  fake agent host (`acceptance/world.go:89` and `:191`). It runs on the fake host's clock
  (`acceptance/world.go:309`), and it always starts a coordinator (`acceptance/world.go:285`).
- The CI job `acceptance` runs on `ubuntu-latest` (`.github/workflows/ci.yml:37-70`). No step
  installs orca or a harness login.
- Plan R15 states that the suite proves "behaviour, not orca or Linear mechanics". The plan's
  Verification Contract runs the orca contract "on a host that runs orca", and `make gates` runs
  only the rows "that need no outside host".

**Gap 2: the orca contract runs only by hand.** Measured in source:

- `make orca-contract` (`Makefile:59-63`) runs `go test -run TestOrcaContract`
  (`internal/agenthost/orca/contract_orca_test.go:71`). That pattern does not match
  `TestOrcaAgentRunsShellCommands` (`:117`), so no make target runs the shell-command test.
- Both tests need `LOOP_ORCA_ROOT`, `LOOP_ORCA_REPO` and `LOOP_ORCA_MODEL`. Both skip when one is
  missing, so an empty environment reads as a pass.
- The contract holds 16 scenarios (`internal/agenthost/agenthosttest/contract.go:97-114`). The orca
  driver offers only `ArgvReader` and `CheckoutObserver`. So 6 scenarios skip on real orca. They
  need a fault that only the fake can inject: an incomplete inventory, a start that never becomes
  ready, a scripted turn with a moved clock (2 scenarios), a dropped model, and a refused notify.
  The other 10 scenarios each start at most one session.
- No file in `docs/evidence/` records a run of `make orca-contract`.

**Gap 3: real orca faults reached main, and no test caught them.** Measured in git history:

- `60a94ea` fixed an adapter that started each agent with `orca terminal create --command`. Orca
  then gave the agent none of its harness flags. The coordinator could run no loop command. A
  live coordinator start found the fault, and no test did. The same commit added
  `TestOrcaAgentRunsShellCommands`.
- `5d938f3` fixed a quota read that described the wrong account when Claude Code sends its
  requests through a proxy.
- The host updates orca each night. `/etc/cron.d/agentbox-orca-nightly` runs at 00:01 +08
  (`docs/evidence/orca-restart.md`), which is 16:01 UTC. This host runs orca 1.4.237 today. The
  sim fixtures record 1.4.232 and 1.4.237 (`internal/agenthost/orca/sim_test.go`).

**Gap 4: no quota reading bounds spend on this host.** Measured in source and host config:

- `internal/agenthost/orca/quota.go:21` reads `orca account list`. It returns
  `ErrQuotaUnavailable` for Claude when Claude Code sends its requests through a proxy.
- `~/.claude/settings.json` on this host sets `ANTHROPIC_BASE_URL`. So the loop has no Claude
  quota reading here.

**Gap 5: a real agent in a test reaches the wrong server and binary.** Measured in source:

- The CLI finds its server from `LOOP_INSTANCE`, or else from `~/.local/state/loop`
  (`internal/cli/instance.go:72-78`). The token file in a checkout holds `token`, `run_id`, `kind`,
  `project` and `item`, and no instance.
- `orca agent launch` has no flag that sets the agent's environment (`orca agent launch --help`,
  orca 1.4.237). A test cannot set `LOOP_INSTANCE` or `PATH` for a real agent.
- I infer from these two facts, and did not run it: a real agent in a test world calls the
  production server with the installed release binary. The production server refuses its token,
  and the agent cannot run the binary under test.

**Gap 6: the loop already has one place for a timed host routine.** Measured in source:

- Plan KTD30 gives every stage a throughput entry in host config, and "Native server jobs have an
  entry too". `internal/config/throughput.go:43` lists four native jobs. Config load refuses a
  missing entry and a cap below 1 (`validateThroughput`). ADR 0009 schedules `overwatch` the same
  way, and `loop upgrade` writes its default entry.
- The scheduler parses each schedule as a UTC cron (`internal/sched/clock.go:20`).
- Every push to main that passes the gates and acceptance jobs becomes a release
  (`.github/workflows/ci.yml:72-78`). `make release-dist` builds only `loop` for each platform
  (`Makefile:105-110`).
- `fault.Service.Report` takes a principal and the report text (`internal/fault/report.go:41`), so
  server code can add a fault with no CLI call.
- ADR 0006 decision 3 rejected a job that a run hands to the server, because no lease, run budget
  or transcript would cover that job.
- `orca repo add` registers a repository by its path. `orca repo` has no command that removes one
  (`orca repo --help`, orca 1.4.237).

**Tickertown has no real-orca acceptance.** Measured in source:

- Tickertown's tests stub orca. Each test writes a shell script named `orca` onto `PATH`
  (`scripts/sdlc/land-command.test.ts:56`, `scripts/sdlc/timers.test.ts:53`), or uses
  `scripts/sdlc/orca-fake.ts`. Its CI jobs run on `depot-ubuntu-24.04` runners with no orca.
- Tickertown proved its orca behaviour in production, on the devbox. So this epic adds a
  requirement that the operator stated. It ports nothing. Plan R15 and the U7 scenario "behind
  build tag `orca`, against real orca on the devbox" are its starting point.

**What I assumed, and did not measure:**

- A real implementer and a real code-reviewer can finish a one-line fixture ticket in 30 minutes.
- The lane can register its own fixture repository with `orca repo add`, and reuse it on each
  later run. I read `orca repo --help`, and ran no `orca repo add`.
- A real session in the contract spends a small share of the daily quota. I read no usage figure,
  because the proxy hides it.
- The darwin host behaves as the Linux host does. Nobody ran orca on the Mac for the loop.

## Decision

1. **Run two layers against real orca: the whole agent-host contract, and one real-agent flow.**
   The flow is F1 with real agents: a real implementer and a real code-reviewer take a fixture
   ticket from claim to merge. The real server, real beads and synthetic GitHub run under it, on
   the real clock, with no coordinator. I rejected the whole acceptance suite on real orca. It
   runs on the fake clock and on injected faults, and each real session waits a real quiet period.
   I rejected the contract alone. The contract proves the host calls, but `60a94ea` shows that a
   real agent can fail where the contract passes.

2. **Keep the 6 fault-injection scenarios on the fake, and allow one skip only: a spent quota
   window (decision 4).** The lane prints each fake-only scenario by name. Any other skip fails the
   lane. A missing `LOOP_ORCA_*` value fails it, and the output names each missing value in the
   line `LOOP_ORCA_<NAME> is not set`. I rejected fault injection into real orca. An orca-serve
   restart killed every native chat session on the host (`docs/evidence/orca-restart.md`), and
   each restart needs the operator's approval.

3. **Run the lane as a native server job, `orca-acceptance`, from its own KTD30 throughput entry,
   and on demand with `make orca-acceptance`.** The default entry is cap 1 and schedule
   `30 17 * * *`, which is 01:30 +08, after the host's nightly orca update. The job runs the lane
   binaries that the installed release ships. So it runs only code that review, CI and the merge
   mechanism passed, which is the code the server itself runs. The job makes its own fixture
   repository, registers it with `orca repo add` when `orca repo show` does not find it, and sets
   each `LOOP_ORCA_*` value itself. It stops the lane after 120 minutes, and it writes the lane's
   output to a log under the state directory. For each failure and each skip, it adds a fault with
   `fault.Service.Report` as the `host` principal, so the fault reaches the coordinator's inbox.
   The operator takes no step: a host config with no entry reads the default at load time, and
   nothing writes the entry, as for `overwatch`
   (`docs/plans/factorio-9ac-real-orca-acceptance-spec.md`, Decisions).
   - I rejected a host timer, such as a systemd user timer or a cron line. KTD30 puts every
     schedule in host config, and the operator would enable the timer by hand on each host.
   - ADR 0006 decision 3 does not reach this job. It rejected a job that a run hands the server: a
     command that an agent picks, from a branch that no review passed. This job runs a fixed binary
     of the installed release, and no run chooses it. Its cap, the session cap of decision 4, the
     120-minute stop and its log cover it.
   - I rejected GitHub-hosted CI, because a vendor runner would need the operator's harness login.
   - I rejected a self-hosted runner on the loop host. It would run each pull request head, which
     an unattended agent wrote, outside any lease, with access to the host's orca and harness login.
   - I rejected `loop coordinator send` for the report, because it types the text as the
     operator's own words.

4. **Bound spend by a session count, not by a quota reading.** Each real-orca scenario declares the
   number of sessions it starts. The lane fails a scenario that starts more sessions than it
   declares, and it starts no session when the declared total passes 20. When the host has a quota
   reading and a window is spent, the lane starts nothing and exits 3. Its output names the
   provider and the time the window resets, and the job adds a fault that says the lane skipped. I
   rejected a skip that exits 0. A lane that skipped every day would then read as green, as an
   empty environment does today (Gap 2). I rejected a bound on the quota reading alone, because
   this host has no Claude reading (Gap 4).

5. **Let a run's CLI take its instance and its binary from the checkout the server made.** The
   server writes both into the checkout, and the installed `loop` defers to them. So a lane agent
   reaches only the lane's server, and it runs the binary under test. Three bounds hold. The server
   writes the file again at each run start. The CLI reads the two values only for a call that
   sends the run token of that checkout, and a call with the host-admin credential ignores them.
   The CLI runs the named binary only from a directory under the instance's state directory, and it
   refuses any other path. I rejected the agent's environment, because `orca agent launch` cannot
   set it (Gap 5). I rejected a second OS user, because it needs a second orca install and a second
   harness login on each host. I rejected an absolute binary path in the prompt, because every
   runbook names the command `loop`.

6. **Write each parity epic's criteria for the fake first, and for real orca only at the host
   boundary.** A criterion about a server rule runs in `make acceptance` on the fake host. A
   criterion about what the host does adds a contract scenario, which runs on the fake and on real
   orca. A criterion that needs a real agent adds a real-orca scenario only when the fake cannot
   prove it. Its spec names the sessions it declares. I rejected a real-orca run of every parity
   criterion. The fake passes the same contract, so a server rule proven on the fake holds on real
   orca, and the session cap would not hold eight epics.

## Consequences

Files, commands and stages that change, for the spec of epic factorio-9ac:

- `Makefile`: a new target `orca-acceptance` runs every test with the build tag `orca`, then the
  scenarios with the build tags `acceptance orca`. `release-dist` also builds the lane's test
  binaries for each platform, next to `loop`. `GATES`, the CI jobs and `make orca-contract` stay as
  they are.
- `internal/agenthost/orca/contract_orca_test.go`: under the lane, a missing `LOOP_ORCA_*` value
  fails with `LOOP_ORCA_<NAME> is not set`. The lane counts each session that it starts.
- `internal/agenthost/agenthosttest/contract.go`: each scenario declares its session count, and it
  names the capability that makes it fake-only.
- `acceptance/world.go`: an option picks the agent host. A real host runs on the real clock and
  starts no coordinator.
- `acceptance/scenarios/`: one file with the build tags `acceptance orca` holds
  `TestF1RealAgentsTakeATicketFromClaimToMerge`.
- `internal/cli/instance.go`, `internal/cli/client.go` and `internal/brief/`: the server writes the
  instance and the binary into each checkout, and the CLI reads them before `LOOP_INSTANCE`, within
  the three bounds of decision 5.
- `internal/config/throughput.go`: `orca-acceptance` joins `nativeJobs` and `DefaultThroughput`,
  with cap 1 and schedule `30 17 * * *`, and joins the optional set. A host config that has no entry
  reads the default, and nothing writes the entry
  (`docs/plans/factorio-9ac-real-orca-acceptance-spec.md`, Decisions).
- A new package under `internal/server/` holds the job. It makes and registers the fixture
  repository, runs the release's lane binaries with the 120-minute stop, writes the log, and adds
  one fault for each failure and each skip.
- `docs/evidence/orca-acceptance.md`: the record of the first green lane run.
- `docs/operating.md`: the job, its throughput entry, and where its log lives. It names no
  operator step.
- Stages: no run stage changes. The server gains one native job.

What this makes easy: an orca update or a loop release that breaks a real agent reaches the
coordinator as a fault within one day, and the operator takes no step to set it up.

What this makes hard:

- A pull request can still merge an orca fault, because the lane runs after the release.
- The job tests the installed release. A host that has not upgraded tests an older loop.
- Each release ships the lane's test binaries, so each download grows.
- A flaky model answer can fail the lane. The coordinator then triages a fault that the code did
  not cause.
- A new real-orca scenario costs sessions from one shared cap.

Postponed on purpose:

- A gate on `loop upgrade` that waits for a green lane. A host that upgrades by cron forces it
  (`docs/operating.md`, "Upgrade by cron"). This host has no upgrade cron today.
- A real-orca restart or daemon-death scenario. A host where restarts do not kill the
  operator's chat sessions forces it.
- The live run in `docs/evidence/v1-e2e.md`. The lane does not replace it, because the lane uses
  synthetic GitHub and no real App.

A later reader must not:

- Add the lane to `GATES`, or to a CI job.
- Register a self-hosted CI runner on the loop host.
- Schedule the lane outside its throughput entry, or add an operator step to start it.
- Make the job run a binary that the installed release did not ship.
- Make a missing `LOOP_ORCA_*` value a pass, or make a quota skip exit 0.
- Start a real coordinator in the lane.
- Raise the session cap without a spec that names the new scenarios.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `grep -n '^GATES :=' Makefile | grep orca` prints nothing.
- `grep -rn -E 'orca|self-hosted' .github/workflows/` prints nothing.
- `head -1 internal/agenthost/orca/contract_orca_test.go` prints `//go:build orca`.
- `grep -n 'agentfake' acceptance/world.go` prints at least one line.
- `loop gates --docs` passes.

These criteria wait on later work, and epic factorio-9ac owns each one:

- `make -n orca-acceptance` prints the contract run and a `go test` run with the tags
  `acceptance orca`.
- `out=$(mktemp); env -u LOOP_ORCA_ROOT make orca-acceptance >"$out" 2>&1; echo $?` prints a
  non-zero exit, and `grep -F 'LOOP_ORCA_ROOT is not set' "$out"` then prints a line.
- `grep -ln '^func TestF1RealAgentsTakeATicketFromClaimToMerge(' acceptance/scenarios/*.go` prints
  one file, and `head -1` of that file prints `//go:build acceptance && orca`.
- `grep -c '"orca-acceptance"' internal/config/throughput.go` prints 2 or more, and
  `grep -n '30 17 \* \* \*' internal/config/throughput.go` prints a line.
- `make -n release-dist VERSION=v9.9.9` prints a `go test -c` command with the tags
  `acceptance orca`.
- `go test ./internal/agenthost/...` proves that a scenario that starts more sessions than it
  declares fails, and that a declared total above 20 starts nothing.
- `go test ./internal/server/...` proves that the job adds one fault for a failed lane, one fault
  for a lane that exits 3, and no fault for a green lane.
- `go test ./internal/cli/...` proves that a call from a checkout reaches the instance that the
  checkout names, with `LOOP_INSTANCE` unset, and that the CLI refuses a binary path outside the
  state directory.
- `docs/evidence/orca-acceptance.md` records one green lane run: its date, the orca version, the
  loop head and the session count.
