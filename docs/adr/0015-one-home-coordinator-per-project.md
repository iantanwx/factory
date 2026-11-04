# ADR 0015: Each project gets one home coordinator, and the loop project's coordinator serves the instance-wide work

**Status:** Proposed
**Date:** 2026-11-04
**Ticket:** factorio-1edo
**Binds:** all

## Context

Epic factorio-1edo gives each project its own home coordinator. Its PRD is
`docs/prd/project-home-coordinators.md`, merged at `27b95a8`. The operator asked for it in these
words: "every project should have its own "home" coordinator." The operator decided three things in
the brainstorm of this epic, and the epic body records each one:

1. Every registered project gets one long-running home coordinator session. All projects share one
   loop server.
2. A project's coordinator owns that project's inbox, and acts only on its own project.
3. The loop project's coordinator, factorio on this host, also serves the instance-wide work: loop
   faults, upgrades, the host doctor and the `rules` part of overwatch.

This run read the loop at `27b95a8` on 2026-11-04. It measured each fact below.

### One coordinator serves the instance today

- `internal/sched/coordinator.go` keeps one coordinator. `CoordinatorConfig.Project` names "the
  project whose repository the coordinator's checkout comes from". Its comment says: "The
  coordinator itself serves every project."
- `cmd/loop/serve.go:214-220` sets that project to the first project of the host config, or
  leaves it empty. When it is empty, `ensureCoordinator` takes the first registered project.
- The store keeps one row of kind `coordinator` in the `schedule` table, with an empty project
  (`saveCoordinator`). The server keeps one session, one checkout named `loop-coordinator` and one
  terminal titled `loop coordinator`.
- `startCoordinator` issues the coordinator token with `rpc.IssueRequest` and no `Project`. So the
  token names no project, and the server admits a coordinator verb on any project's record.
- `coordinatorPack` gives the coordinator no conditional skill. Its comment says: "A condition
  reads one project's config, and the coordinator serves every project."
- When the home project is no longer registered, `ensureCoordinator` closes the session, revokes
  its token, removes its checkout and starts a new coordinator in another project. The operator's
  conversation ends with it.
- The doorbell (`internal/inbox/notify.go`, type `Doorbell`) rings one session with one text. Each
  inbox entry of every project rings that session.
- `pack/runbooks/coordinator.md` opens with "You are the one resident agent of this loop
  instance." Its § 3 says that a `loop stage pause` with no `--project` holds the stage in every
  project.
- `internal/cli/surface.go` admits only the coordinator on `stage pause`, `stage resume`,
  `fault ack` and `inbox list`. It admits the host and the coordinator on the host verbs `upgrade`,
  `rollback` and `doctor`.

### What this run did not measure

- The number of entries that each project adds to the one inbox. A run gets `CAPABILITY_DENIED` on
  `loop inbox list`.
- The memory and the model cost of one more session for each project. This ADR assumes that a
  host runs few projects, as this host does.

## Decision

Each registered project with a project config gets one home coordinator session, scoped to that
project. The coordinator of the loop project also serves the work that names no project.

1. **One coordinator for each project.** Each coordinator has its own session, terminal, checkout
   and token. Its checkout is `loop-coordinator`, made from its project's repository at its
   project's default branch. All coordinators share one loop server.
2. **A coordinator acts on its own project.** Its token names its project. The server refuses a
   coordinator verb that names a record of another project, and the refusal names the owner. A
   record is an item, a room, an inbox entry, a decision, an alert or a setting. Each inbox entry
   of a project rings that project's coordinator only.
3. **The loop project's coordinator serves the instance-wide work.** Instance-wide work is the six
   kinds that the PRD names under `## Requirements`: loop faults, upgrades and rollbacks, the host
   doctor, the alerts and findings of instance jobs that name no project, the `rules` part of the
   sweep, and a stage pause or resume in every project. The server admits each one only from the
   loop project's coordinator, or from the operator's shell when the verb is a host verb.
4. **Each later coordinator verb names its scope.** An epic that adds a coordinator verb states in
   its spec whether the verb is project-scoped or instance-wide. A project-scoped verb acts on the
   caller's project only. The server admits an instance-wide verb only from the loop project's
   coordinator and, for a host verb, from the operator's shell.
5. **The spec names the loop project.** This ADR does not choose how the server names the loop
   project. The PRD § Open questions names two answers: the host config, or the repository that the
   loop releases from. The spec of factorio-1edo chooses one. Either answer must name exactly one
   registered project, and the server names it, never a coordinator. The spec also decides what
   serves the instance-wide work when the loop project is not registered.

What I rejected:

- **One coordinator for the whole instance, as today.** It mixes the work of every project in one
  conversation. The operator's request names this as the fault: "that isn't consistent with the
  way tickertown works". Its token names no project, so a mistake in one project's conversation
  can change another project. It cannot get a project's conditional skills, because it serves
  every project. And the removal of its home project ends the operator's conversation and moves
  the coordinator to another project.
- **One coordinator for each project, plus one more for the instance.** The extra session has no
  project, so it has no repository and no default branch for its checkout. It also costs one more
  long-running session on each host. The operator chose the loop project's coordinator, because
  the loop's own work belongs to the loop's own repository.
- **Each project's coordinator also gets the instance-wide work.** Then a loop fault rings every
  coordinator, and two of them can ack, triage or file one fault at once. Each upgrade would need
  a lock across coordinators.

## Why an ADR

- **More than one epic:** it binds every later epic that adds a coordinator verb, by decision 4.
  It also binds the epics of ADR 0004, ADR 0007, ADR 0009 and ADR 0011. So `Binds:` reads `all`.
- **Rejected alternative:** one coordinator for the whole instance, which
  `internal/sched/coordinator.go` runs today.
- **Costly to undo:** it changes the process topology, from one coordinator session for the
  instance to one for each project. It changes a trust boundary, because a coordinator token now
  names one project. It also changes stored state, the one `coordinator` row of `schedule`.
- **How, not what:** US-01 to US-07 of the PRD say what the operator gets. This ADR decides how
  the server splits the work and the trust between sessions.

## Consequences

### The process topology changes

- One coordinator session, terminal and `loop-coordinator` checkout run for each project whose
  default branch holds `.loop/config.yaml`. A host with three such projects runs three
  coordinators. Each terminal title names its project.
- `CoordinatorConfig.Project` and the one `coordinator` row of `schedule` give way to one state for
  each project. The spec names the stored shape and its migration.
- The doorbell rings one session for each project. A ring names only that project's entries,
  except on the loop project's coordinator.
- A project removal ends that project's coordinator only. No coordinator moves to another project.

### The trust boundary changes

- `startCoordinator` issues each coordinator token with its project. Today the token names none.
- The capability check reads that project on every coordinator verb, and refuses a record of
  another project. A coordinator that names `--project` of another project gets the same refusal.
- The loop project's token also admits the instance-wide work. The spec states how the server
  tells the loop project's token from the others.

### How three earlier ADRs read after this decision

- **ADR 0004, `docs/adr/0004-operator-visibility.md`, decision 3.** It says that `stage pause`
  with no `--project` holds the stage in every project. After this ADR, that holds only when the
  loop project's coordinator runs the verb. A project's coordinator that runs `stage pause` or
  `stage resume` with no `--project` acts on its own project only. This ADR wins that conflict by
  `loop-sdlc-documents` § 5. Each other decision of ADR 0004 still holds.
- **ADR 0007, `docs/adr/0007-shared-skill-pack.md`.** The pack still ships one coordinator
  runbook, `pack/runbooks/coordinator.md`, and every project's coordinator reads that one copy. No
  project gets a copy of its own, because two copies drift. The runbook stops saying "the one
  resident agent of this loop instance". It names which sections only the loop project's
  coordinator follows, such as § 9 Loop faults. The step of § 9 that files a loop fault as a bug
  in another project stays, and only the loop project's coordinator takes it.
- **ADR 0011, `docs/adr/0011-coordinator-recovers-twice-failed-item.md`.** "The coordinator" now
  reads as the coordinator of the item's project. The `retry_limit` alert names the item's
  project, so it rings that coordinator. The bound of one recovery for each item still holds. The
  record of the recovery stays on the item, so a new session of the same coordinator does not
  reset it.

ADR 0009 keeps its sweep and its parts. A finding that names a project rings that project's
coordinator. A finding of the `rules` part rings the loop project's coordinator.

### Files and commands that change, for the spec of epic factorio-1edo

- `internal/sched/coordinator.go` and `cmd/loop/serve.go`: one coordinator for each project.
- `internal/rpc/tokens.go` and the capability check of `internal/cli/capabilities.go`: a
  coordinator token names its project.
- `internal/inbox/notify.go`, `internal/server/inbox.go` and `internal/overwatch/ring.go`: each
  ring reaches one project's coordinator.
- `loop coordinator send` names the project of the coordinator that gets the message.
- `pack/runbooks/coordinator.md` and `docs/operating.md` § Talk to the coordinator.

What this makes easy: the operator talks about one project with one coordinator. A mistake in that
conversation stays in that project. A project's coordinator can later get that project's
conditional skills. The conditional-kernel-skills epic decides that.

What this makes hard:

- Each project costs one more long-running session.
- A question that spans two projects has no one coordinator. The operator brings it to each
  coordinator, or to the loop project's coordinator when it is instance-wide work.
- The loop project's coordinator carries the loop's own work and its project's work in one
  conversation.

A later reader must not:

- Add a coordinator verb whose spec does not name its scope as project-scoped or instance-wide.
- Issue a coordinator token that names no project.
- Ring two coordinators for one entry, or let a coordinator other than the loop project's admit an
  instance-wide verb.
- Move a coordinator to another project when the operator removes its own project.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `grep -c '^\*\*Ticket:\*\* factorio-1edo$' docs/adr/0015-one-home-coordinator-per-project.md`
  prints `1`.
- `ls docs/adr | grep -c '^0015-'` prints `1`.
- `grep -c 'docs/adr/000[47]-\|docs/adr/0011-' docs/adr/0015-one-home-coordinator-per-project.md`
  prints a count of at least `3`.
- `loop gates --docs` passes.

These criteria wait on later work, and epic factorio-1edo owns each one:

- `grep -n 'serves every project' internal/sched/coordinator.go` prints nothing.
- `grep -n 'one resident agent of this loop instance' pack/runbooks/coordinator.md` prints nothing.
- A test proves that a coordinator token of project A gets a refusal on an item of project B, and
  that the item does not change.
- A test proves that a loop fault that a run of project A reports rings the loop project's
  coordinator only.
- `make pack-check` passes.
