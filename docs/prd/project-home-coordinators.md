# Give each project its own home coordinator

## Stories

The operator asked for this epic in the coordinator's chat. The coordinator recorded the operator's
words as the epic's request. It holds two clauses, and the stories quote each one alone:

> "that isn't consistent with the way tickertown works"

> "every project should have its own "home" coordinator."

Today one coordinator serves every project of an instance. Its one checkout, `loop-coordinator`,
comes from a single home project. Every inbox entry, alert, question, decision and overwatch
finding of every project reaches that one session. `docs/operating.md` § Talk to the coordinator
and `internal/sched/coordinator.go` state this. Tickertown runs its coordinator in its own
repository, so the people who build tickertown talk to a coordinator that knows only their project.

The epic body asks this PRD to settle five questions:

1. Who serves the instance-wide work that names no project?
2. How do `loop coordinator send` and the doorbell pick a coordinator?
3. How do tokens and capabilities scope each coordinator to its project?
4. How do an upgrade, a project removal and a re-registration move or close each coordinator?
5. How does the loop project, factorio, keep its own coordinator for loop faults?

The coordinator also recorded the operator's flow for each project as a handoff note on the epic,
on 2026-11-04: "1. i run loop-init in main 2. it merges 3. it opens loop-coordinator worktree 4.
work continues from there". The note adds that the server starts the coordinator once loop-init's
change merges to the default branch, not at `loop register`. After that, the operator works from
that coordinator session. No story quotes the note, because a story quotes only the request.
US-01 names it as the source of its start rule.

### US-01 Each project gets its own coordinator

As the operator, I can open one coordinator for each project, in a checkout of that project, so
that I talk about a project with a coordinator whose home is that project.

> "every project should have its own "home" coordinator."

The epic body places it "in a loop-coordinator checkout of that project, with its own session and
terminal". The handoff note above gives the moment it starts.

### US-02 A project's coordinator brings me only its project's work

As the operator, I can rely on a project's coordinator to bring me each inbox entry, alert,
decision and overwatch finding of that project, and none of another project, so that each
conversation stays about one project.

> "that isn't consistent with the way tickertown works"

The epic body: "Each coordinator reads and answers the inbox entries, alerts, decisions and
overwatch findings of its own project."

### US-03 A project's coordinator acts only on its own project

As the operator, I can trust that a project's coordinator changes nothing in another project, so
that a mistake in one project's conversation stays in that project.

> "every project should have its own "home" coordinator."

The epic body asks "how tokens and capabilities scope each coordinator to its project".

### US-04 The loop project's coordinator serves the loop itself

As the operator, I can bring loop faults and the other work that names no project to the
coordinator of the loop project, factorio, so that this work has one home as each project does.

> "every project should have its own "home" coordinator."

The epic body asks "how the loop project (factorio) keeps its own coordinator for loop faults", and
names the instance-wide work: "loop faults, upgrades, host doctor, instance jobs, the rules part
that compares projects".

### US-05 I send a message to one project's coordinator

As the operator, I can send a message from a shell to the coordinator of one project that I name,
so that the message reaches the conversation it belongs to.

> "every project should have its own "home" coordinator."

The epic body asks "how `loop coordinator send` and the doorbell pick a coordinator".

### US-06 Coordinators follow upgrades, removals and re-registrations

As the operator, I can upgrade the loop, remove a project and register it again, and each project
still has the right coordinator, so that no coordinator works for a project the loop no longer
serves.

> "every project should have its own "home" coordinator."

The epic body asks "how an upgrade, a project removal and a re-registration move or close each
coordinator", and names factorio-29766af, which moves the one coordinator today.

### US-07 I get a project's coordinator back after a close or a move

As the operator, I can get a project's coordinator back after I close its session by accident, or
after I move the loop to a new machine, so that each project keeps its home coordinator and its
work in flight.

> "every project should have its own "home" coordinator."

The operator added this need in a comment on PR #385 on 2026-11-04: "suppose i migrate to a new
machine or i accidentally close it? we would need a way for the coordinator to reregister." No
story quotes the comment, because a story quotes only the request. US-07 names it as its source.

## Scope check

US-01 is the base. US-02, US-03, US-05, US-06 and US-07 each change one behaviour of a coordinator that
US-01 starts. US-04 names the coordinator that keeps the work no project owns.

**Stories:** 7
**Estimated total:** 2,300 lines
**Lands independently?** Yes.

- US-01 merges alone, together with the four transition requirements under it. They start "On the
  first start of this release", "Until that session ends", "Each other project with a project
  config" and "The project whose repository holds the old". Each project gets a session. Until
  US-02 merges, the doorbell rings one coordinator with every entry, as it does today. That
  coordinator serves the project whose repository holds the old `loop-coordinator` checkout.
  First, that coordinator is the session that ran before this change. After that session ends, it
  is the new session that starts in the same checkout. The alert of a failed start rings the loop
  project's coordinator. When the old checkout's project has no project config, every entry rings
  the loop project's coordinator until US-02 merges.
- US-02 needs US-01 and merges alone after it.
- US-03 needs US-01 and merges alone.
- US-04 merges together with US-02 or after it. The instance-wide work needs a ring that a project
  filter does not drop.
- US-05 needs US-01 and merges alone.
- US-06 needs US-01 and merges alone.
- US-07 needs US-01 and merges alone.

**Promises provable alone?** Yes.

- "Each project with a project config gets one coordinator" needs the scheduler, a fake agent host
  and two fixture projects.
- "A ring reaches only its project" needs the inbox, two fake sessions and one entry.
- "A token refuses another project's item" needs the capability check and two fixture items.
- "A loop fault reaches the loop project's coordinator" needs one fault and two fake sessions.
- "Send reaches the named coordinator" needs the send verb and two fake sessions.
- "Removal closes one coordinator" needs the scheduler and two fixture projects.
- "Restart creates a missing checkout again" needs the scheduler, a fake agent host and one fixture
  project.

**Verdict:** proceed.

## Goal

Each project that the loop serves has a coordinator of its own, in a checkout of that project. The
server starts that coordinator once the project's default branch holds the project config that
loop-init lands. The operator talks about a project with that coordinator. That coordinator brings
only that project's work. It changes only that project, except the work that US-03 grants the
loop project's coordinator. The loop's own work, such as loop faults and upgrades, has its home in
the loop project's coordinator. After the operator closes a coordinator by accident, or moves the
loop to a new machine, the project gets its coordinator back.

## Requirements

In this section, the **loop project** is the registered project that the server names as the
loop's own, factorio on this host. **Instance-wide work** is the work that names no project. It
holds six kinds:

1. loop faults
2. upgrades and rollbacks
3. the host doctor
4. the alerts and overwatch findings of instance jobs that name no project
5. the `rules` part of the sweep that compares projects
6. a pause or a resume of a stage in every project

- Each registered project has at most one coordinator. Each coordinator has its own session, its
  own terminal and its own token.
  → § US-01
- A project's coordinator works in a checkout named `loop-coordinator`, made from that project's
  repository at that project's default branch.
  → § US-01
- The server starts a project's coordinator once the project's default branch holds the project
  config that loop-init lands, `.loop/config.yaml`.
  → § US-01
  → pack/skills/loop-init/SKILL.md § 7. Land it
- `loop register` alone starts no coordinator. A registered project whose default branch holds no
  project config has no coordinator.
  → § US-01
- Each coordinator's terminal title names its project, so the operator can tell two coordinators
  apart.
  → § US-01
- When a project's coordinator session ends, the server starts the next one for that project in the
  same checkout. No other project's coordinator changes.
  → § US-01
- A coordinator that fails to start raises an alert that names its project. The alert rings the
  loop project's coordinator. The failure stops no other project's coordinator from starting.
  → § US-01
- On the first start of this release, the one coordinator that ran before this change gets the
  release message. The server does not end its session. When the session ends, the server revokes
  its token. No
  unscoped session starts after it.
  → § US-01
- Until that session ends, it is the coordinator of the project whose repository holds the old
  `loop-coordinator` checkout. The scope requirements of US-03 bind each session that starts
  under this release.
  → § US-01
- Each other project with a project config gets its coordinator at the first start of this
  release. The start rule above applies.
  → § US-01
- The project whose repository holds the old `loop-coordinator` checkout gets its new coordinator
  when the old session ends. When its default branch holds a project config, the new coordinator
  starts in the old checkout. Otherwise, the server removes the checkout.
  → § US-01
- A new inbox entry of a project rings only that project's coordinator. This rule has three
  exceptions, and each one rings the loop project's coordinator:
  1. the alert of a coordinator that failed to start
  2. an entry of a registered project that has no coordinator, because its default branch holds no
     project config
  3. a loop fault, whatever project it names
  → § US-02
- An overwatch finding that names a project rings only that project's coordinator. A finding of the
  `rules` part is the exception. It is instance-wide work.
  → § US-02
- The inbox that a project's coordinator lists holds the entries of that project only. The
  inbox of the loop project's coordinator also holds each entry that rings it by an exception of
  US-02 or by US-04.
  → § US-02
- When a project has a coordinator but no running session, its new entries wait. When the next
  session starts, its first ring names each entry that arrived while no session ran.
  → § US-02
- A project's coordinator may run a coordinator verb only on a record of its own project. A record
  is an item, a room, an inbox entry, a decision, an alert or a setting. The rule below on the
  entries that ring the loop project's coordinator is an exception.
  → § US-03
- The server refuses a coordinator verb that names another project's record. The refusal names the
  project that owns the record.
  → § US-03
- A project's coordinator creates work and opens rooms only in its own project. This rule has one
  exception. The loop project's coordinator may file a loop fault as a bug in the project that the
  fault names. A policy of that project must authorize it. The coordinator runbook § 9 step 3
  states this step.
  → § US-03
- A project's coordinator changes the settings of its own project only.
  → § US-03
- The loop project's coordinator may list, and run a coordinator verb on, each entry that rings it
  by an exception of US-02 or by US-04.
  → § US-03
- When a project's coordinator runs `loop stage pause` or `loop stage resume` with no `--project`,
  the verb acts on that coordinator's project only. The loop project's coordinator is the
  exception. With no `--project`, its pause or resume acts in every project, as ADR 0004 states.
  → § US-03
  → docs/adr/0004-operator-visibility.md
- The loop project's coordinator acknowledges, triages and resolves each loop fault.
  → § US-04
- A loop fault that any session reports rings the loop project's coordinator, and no other
  coordinator.
  → § US-04
- The server admits the host verbs `upgrade`, `rollback` and `doctor` only from the loop project's
  coordinator, in its own checkout. It refuses them from every other coordinator. The operator can
  still run them from a shell that is no coordinator's session.
  → § US-04
- An inbox entry, an alert or an overwatch finding that names no project rings the loop project's
  coordinator.
  → § US-04
- The `rules` part of the overwatch sweep reaches the loop project's coordinator, which may read
  the default branch of each registered project to compare their rules.
  → § US-04
- The loop project's coordinator also serves the loop project's own inbox entries, alerts,
  decisions and overwatch findings, as any project's coordinator does.
  → § US-04
- `loop coordinator send` delivers its message to the coordinator of one project, and never to more
  than one coordinator.
  → § US-05
- The operator can name the project whose coordinator receives the message.
  → § US-05
- `loop coordinator send` refuses a project that is not registered, or that has no running
  coordinator. The refusal names the project.
  → § US-05
- After an upgrade, each running coordinator gets one message that names the new release. The next
  session of each coordinator starts under the new release, in the same checkout.
  → docs/operating.md § Talk to the coordinator
- When the operator removes a project, the server ends its coordinator's session, revokes its token and
  removes its checkout. No other project's coordinator changes, and no coordinator moves to another
  project.
  → § US-06
- When the operator registers a removed project again, its coordinator starts by the same rule as a new
  project's, in a new `loop-coordinator` checkout.
  → § US-06
- When a project's coordinator session ends, the next session resumes the conversation of the last
  session when the harness can resume it. The next session gets a new token and the runbook of the
  release that the server runs.
  → § US-07
- When the harness cannot resume that conversation, the first message of the next session names
  the work that the last session left open. That work is each question it prepared, each room it
  opened that is still open, and each entry that arrived while no session ran.
  → § US-07
- When a project's `loop-coordinator` checkout is missing, the server starts no session for that
  project. The alert of the failed start names the verb `loop coordinator restart`.
  → § US-07
- The host verb `loop coordinator restart --project <project>` ends the session of that project's
  coordinator when one runs, and revokes its token. It creates the `loop-coordinator` checkout
  again when the checkout is missing. Then it writes a new token into the checkout and starts a
  session.
  → § US-07
- The session that `loop coordinator restart` starts does not resume the last conversation. Its
  first message names the work that the last session left open, as the requirement above states.
  → § US-07
- The server admits `loop coordinator restart` only from a shell that is no coordinator's session.
  It refuses the verb from every coordinator, and from the loop project's coordinator too.
  → § US-07
- `loop coordinator restart` refuses a project that is not registered, or whose default branch
  holds no project config. The refusal names the project.
  → § US-07
- `loop coordinator restart` for one project changes no other project's coordinator.
  → § US-07

## Acceptance criteria

- **A1.** A host has two registered projects, each with a project config on its default branch. Two
  coordinator sessions run, one in a `loop-coordinator` checkout of each project.
- **A2.** A registered project with no project config on its default branch has no
  coordinator.
- **A3.** A project's coordinator starts within one scheduler tick after its project config reaches
  the default branch.
- **A4.** The two coordinators' terminal titles differ, and each names its project.
- **A5.** A new inbox entry of project A rings A's coordinator, and B's coordinator receives no
  ring for it.
- **A6.** The inbox that A's coordinator lists holds no entry of project B.
- **A7.** When A's coordinator tries to answer, move or comment on an item of project B, the server
  refuses, and B's item does not change.
- **A8.** When A's coordinator tries to change B's settings, the server refuses, and B's settings do
  not change.
- **A9.** A loop fault that a run of project A reports rings the loop project's coordinator, and A's
  coordinator receives no ring for it.
- **A10.** When a coordinator other than the loop project's runs `loop upgrade`, the server refuses,
  and the host does not upgrade.
- **A11.** `loop coordinator send` that names project A delivers the message to A's coordinator only.
- **A12.** `loop coordinator send` that names a project with no running coordinator refuses, and
  delivers the message to no coordinator.
- **A13.** After an upgrade, each running coordinator receives exactly one message that names the
  new release.
- **A14.** When the operator removes project A, A's coordinator session ends and its token no
  longer works. B's coordinator session keeps running with the same session id.
- **A15.** After the operator removes project A and registers it again, A gets a new coordinator, and no session
  of the removed coordinator runs.
- **A16.** When A's coordinator session ends, the next session starts in A's existing
  `loop-coordinator` checkout, and B's coordinator does not restart.
- **A17.** On the first start of this release, the coordinator that ran before this change receives
  one message that names the new release. Its session keeps running with the same session id.
  Each other project with a project config gets a coordinator of its own at once, and each terminal
  title names its project.
- **A18.** When the session of the coordinator that ran before this change ends, its token no
  longer works. When the project that holds the old checkout has a project config, it then gets its
  own coordinator in that checkout. Otherwise the checkout is gone.
- **A19.** An entry of project A that arrives while A's coordinator has no running session rings no
  coordinator. When A's next session starts, its first ring names that entry.
- **A20.** When A's coordinator fails to start, an alert that names A rings the loop project's
  coordinator, and B's coordinator still starts.
- **A21.** A new entry of a registered project with no project config on its default branch rings
  the loop project's coordinator.
- **A22.** When A's coordinator pauses a stage with `loop stage pause` and no `--project`, the
  stage pauses in A and does not pause in B.
- **A23.** A loop fault that a run of A reports appears in the loop project's coordinator's inbox
  list, and its `loop fault ack` succeeds.
- **A24.** When A's coordinator session ends and the harness can resume its conversation, A's next
  session continues that conversation with a new token. The old token no longer works.
- **A25.** When A's coordinator session ends and the harness cannot resume its conversation, the
  first message of A's next session names each question that the last session prepared, each room
  it opened that is still open, and each entry that arrived while no session ran.
- **A26.** When A's `loop-coordinator` checkout is missing, the server starts no session for A, and
  an alert names `loop coordinator restart`. Then `loop coordinator restart --project A` from the
  operator's shell creates the checkout again and starts A's coordinator in it. B's coordinator
  session keeps the same session id.
- **A27.** When A's coordinator session runs, `loop coordinator restart --project A` ends it, and
  its token no longer works. The new session does not continue the last conversation, and its
  first message names the work that the last session left open.
- **A28.** When a coordinator session runs `loop coordinator restart`, the server refuses, and no
  coordinator changes.
- **A29.** `loop coordinator restart` that names a project with no project config on its default
  branch refuses, and starts no session.

## Boundaries with other epics

- **Project settings.** The project-scoped-settings epic owns `loop settings set` and its commit.
  Its PRD is `docs/prd/project-scoped-settings.md`. This epic only decides which coordinator may run
  it for a project. It changes no setting and no settings file.
- **Overwatch.** ADR 0009 owns the sweep and its parts. Its file is `docs/adr/0009-overwatch.md`.
  This epic changes which coordinator a part's ring reaches. It changes no part and no finding key.
- **The retro's rule comparison.** The retro epic owns the comparison of rules across projects. Its
  PRD is `docs/prd/retro.md` § US-06. This epic names the coordinator that runs it, the loop
  project's. It changes no step of the comparison.
- **Conditional kernel skills.** The conditional-kernel-skills epic did not decide which rule picks
  the skills of a session that holds no item, such as the coordinator. Its PRD is
  `docs/prd/conditional-kernel-skills.md`. Today the coordinator gets no conditional skill, because
  it serves every project. `internal/sched/coordinator.go` holds this rule. This epic gives each
  coordinator one project. It does not decide that open question.
- **The coordinator runbook.** The shared skill pack owns `pack/runbooks/coordinator.md`. ADR 0007,
  `docs/adr/0007-shared-skill-pack.md`, states this. The runbook's text says one coordinator serves
  the instance. The spec names each section that must change.
- **The operating guide.** `docs/operating.md` § Talk to the coordinator says that one coordinator
  serves every project. It also says that a removal moves the coordinator to another project. Both
  conflict with US-01 and US-06. The spec names each sentence that must change.
- **Checked against:** every PRD in `docs/prd/` and ADRs 0004, 0007, 0009 and 0011. ADR 0004,
  `docs/adr/0004-operator-visibility.md`, lets `stage pause` with no `--project` act in every
  project. US-03 changes that: it keeps the every-project pause only for the loop project's
  coordinator. The spec names the ADR 0004 text that changes. ADR 0011,
  `docs/adr/0011-coordinator-recovers-twice-failed-item.md`, names "the coordinator", and each
  project's coordinator now does that work for its own items.

## Out of scope

- A coordinator for a project on another host. Each host serves the projects registered on it.
  Never in this epic.
- A change to what any coordinator does with an entry, a fault or a finding. The coordinator
  runbook keeps its steps. This epic changes only which coordinator receives the work.
- A change to how the loop project's coordinator files a loop fault as a bug in another project.
  Runbook § 9 step 3 keeps that step, and US-03 names it as one of its exceptions.
- A change to `loop register` beyond starting no coordinator. Register keeps its other steps.
- A change to loop-init. It still lands the project config on the default branch, and that landing
  is the start signal.
- A coordinator session for a room or a run. Rooms and runs keep their own sessions.
- A move of a conversation's history to a new machine. On a new machine the harness holds no
  history, so the first message of the new session names the work left open.

## Open questions

- Which coordinator serves the instance-wide work when the loop project is not registered on the
  host, or after the operator removes it? The epic body names factorio as the loop project. It does
  not say what a host without it does. Blocks: A9, A10, A20 and A21.
- Which coordinator receives `loop coordinator send` when the operator names no project? One answer
  picks the project of the clone that the shell runs in. Another refuses and lists the running
  coordinators. Both fit the request. Blocks: A11.
- May a project's coordinator still run the read-only host verbs `project list` and `project show`?
  The coordinator runbook admits every coordinator on them today. Runbook § 9, "Loop faults",
  says so.
  They read every project, and US-03 is about writes. Blocks: the US-04 requirement on the host
  verbs `upgrade`, `rollback` and `doctor`.
- How does the server name the loop project? The epic body names factorio. Today the host config
  names the coordinator's project in `CoordinatorConfig.Project`, in
  `internal/sched/coordinator.go`. The spec chooses between the host config and the repository the
  loop releases from. Blocks: A9, A20 and A21.
- Does the decision of who serves instance-wide work need an ADR? Four facts bear on it:
  1. It binds every later epic that adds a coordinator verb.
  2. It rejects one coordinator for the instance.
  3. It changes the process topology.
  4. It changes a trust boundary.

  By `loop-sdlc-documents` § 6, these four facts meet all four parts of the ADR test. On a review
  pass, the doc-reviewer names each requirement that passes the ADR test, by the doc-reviewer
  runbook § 2.
- The work reaches four parts of the server:
  1. the scheduler that starts sessions
  2. the doorbell and the send verb
  3. the capability check
  4. the coordinator runbook in the pack

  They ship in one binary and one release, so the scope check finds one epic. When the operator
  wants a seam, it falls between the runbook text and the server code. This run does not split it.
