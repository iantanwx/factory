# Project-scoped settings

## Stories

The operator opened this epic on 2026-10-24:

> "file a PRD for project scoped settings. we will want that degree of control."

The trigger was slow throughput. The coordinator could tune it only for the whole host, by editing
host config on the box. Two reasons drive the epic, chosen in the intake room on 2026-10-24: more
projects will share this host, and a change to a project's pace must be a recorded change in that
project's repository. The stories quote the operator's choices in that room.

### US-01 Set a stage's cap for one project

As the operator, I can set the cap of each run stage for one project, so that the project's pace
is its own and no other project's setting changes it.

> Chosen: "Stage caps". Chosen for how a project value meets the host value: "Project overrides
> host". The option read: "A project cap replaces the host value for that project, and the host
> value is only a default. Each project is counted alone."
>
> Chosen for a cap lowered below the live run count: "They finish". The option read: "Live runs
> carry on to their own end, and no new run starts until the count is under the new cap."

### US-02 Set the pool and intake caps for one project

As the operator, I can set the reviewers pool cap and the intake cap for one project, so that
every throughput limit of the project is the project's own.

> Chosen: "Pool + intake caps".

### US-03 Set the server jobs' schedules for one project

As the operator, I can set the schedule of each server job that runs for a project, so that one
project can merge or react faster than another.

> Chosen: "Native job settings". The option read: "Schedules of merge-handoff,
> dependency-advance, decision-timers and github-reactor for this project."

The option text named github-reactor, but the reactor is an instance job. It runs once for the
host and polls every project in one tick, so it has no schedule for one project. This story covers
the other three jobs.

What holds today: merge-handoff, dependency-advance and decision-timers run for each project, and
the scheduler already uses a project's `stages.<job>.schedule` in place of the host schedule, for
that project only. So this story's requirements already hold at the merge base, and A6 is a
regression criterion. One gap the code shows: the server accepts a misspelled job name under
`stages`, and the name silently does nothing. Whether the story keeps that gap as its new promise,
or drops out, is an open question.

### US-04 Set how many runs one tick starts for one project

As the operator, I can set how many runs of a stage one tick may start for one project, so that a
raised cap fills quickly instead of one run per tick.

> Chosen: "Runs per tick". Chosen for the overlap with factorio-lqo: "lqo builds, epic sets". The
> option read: "factorio-lqo builds the ability to start several runs in one tick. This epic adds
> only the project setting for how many, and it depends on lqo landing first."

### US-05 The coordinator changes a project's settings

As the coordinator, I can change a project's settings with one command, on my own judgement, so
that I fix slow throughput without waiting for the operator.

> "well the agent should also be able to change the config"
>
> Chosen for which agent: "Coordinator". Chosen for the write path: "Verb lands a repo commit".
> The option read: "the server commit[s] the change to .loop/config.yaml on the default branch. It
> takes effect next tick. The repo file stays the one source, and git history is the change log
> with author and reason. You approve nothing." Chosen for when: "Own judgement, tell you". The
> option read: "Each change records a reason and shows in your inbox as a notice that needs no
> answer."

### US-06 See the settings in force for a project

As the operator, I can read every setting in force for a project, with where each value came
from, so that I know why the project runs at the pace it does.

> Chosen: "Effective-settings verb". The option read: "One read verb lists every setting with its
> effective value, its source (project file or host default) and the commit that last changed it.
> It also says when the server is running on a last-known-good copy because main is invalid."

### US-07 A run cannot change its project's settings

As the operator, I can trust that no stage run changes its project's settings through its pull
request, so that the coordinator's command is the one agent path to a change.

> Chosen: "Refuse settings edits". The option read: "A run's push that changes the settings in
> .loop/config.yaml (stages, caps, schedules, merge) is refused, with a code that points to the
> coordinator's verb. Gates and setup edits stay allowed, because tickets legitimately add gates."

## Scope check

US-01 builds the per-project cap that US-02 extends to the pool and intake caps. US-04 depends on
factorio-lqo, which builds the multi-start. US-05, US-06 and US-07 each work on today's settings
before any new setting exists.

**Stories:** 7
**Estimated total:** 2,100 lines
**Lands independently?** Yes. US-01 merges alone. US-02 needs US-01's per-project counting and still merges alone after it. US-03 already holds at the merge base, so it needs no change unless the operator gives it a new promise. US-04 merges alone after factorio-lqo lands. US-05, US-06 and US-07 each merge alone, because each one works on the settings that exist today.
**Promises provable alone?** Yes. A per-project cap needs two projects and a fake run count. A server job schedule needs the scheduler and one project. The command needs a project repository fixture and the inbox. The settings view needs only config files. The push refusal needs one push fixture. Runs per tick needs factorio-lqo's multi-start, and it is the last promise to prove.
**Verdict:** proceed.

## Goal

The operator sets each throughput limit and schedule for one project, and that project alone runs
at that pace. The coordinator changes those settings on its own judgement, and each change is a
recorded commit in the project's repository that tells the operator why. The operator reads, for
each project, every setting in force and where it came from.

## Requirements

In this section, a project's **settings** are its stage entries (enabled, model, harness, run
budget, schedule, cap and runs per tick), its pool caps, its intake cap, its server job schedules,
and its merge mechanism. Gates, setup, signoff readers, the secret manifest, the tracker, policy
hooks and the `health` section that factorio-a7r adds are not settings. US-07's option text names
the settings as "stages, caps, schedules, merge", and none of these is one of them.

- A project may set a cap for each run stage.
  → § US-01
- A project's stage cap replaces the host value of that cap for that project.
  → § US-01
- A host cap applies to each project that sets no cap for that stage.
  → § US-01
- A stage cap counts only the live runs of its own project. Live runs of another project never
  fill it.
  → § US-01
- When a cap drops below the project's live run count, every live run continues to its own end,
  and no new run of that stage starts until the count is under the cap.
  → § US-01
- A project may set the reviewers pool cap. That cap counts only the project's own reviewer runs.
  → § US-02
- A project may set the intake cap. That cap counts only the project's own epics.
  → § US-02
- A project may set the schedule of each server job that runs for a project: merge-handoff,
  dependency-advance and decision-timers.
  → § US-03
- A host server job schedule applies to each project that sets none.
  → § US-03
- A project may set, for each run stage, the most runs that one tick starts.
  → § US-04
- The coordinator changes a project's settings with one command.
  → § US-05
- The command refuses any caller that is a stage run.
  → § US-07
- The command lands each change as one commit to the project's settings file on the project's
  default branch.
  → § US-05
- Each commit names who made the change and the reason the caller gave.
  → § US-05
- The command refuses a change that gives no reason.
  → § US-05
- A landed change is in force from the next scheduler tick, with no restart.
  → § US-05
- The command refuses a change that the settings file would not validate, and lands nothing.
  → § US-05
- Each change by the coordinator puts one notice in the operator's inbox, with the setting, the
  old value, the new value and the reason. The notice needs no answer.
  → § US-05
- The coordinator needs no approval from the operator to make a change.
  → § US-05
- One read command lists, for a project, every setting with its value in force.
  → § US-06
- For each setting, the read names its source: the project file or the host default.
  → § US-06
- For each setting from the project file, the read names the commit that last changed it.
  → § US-06
- When the project's default branch holds a settings file that does not load, the read says that
  the last good copy is in force, and why the file does not load.
  → § US-06
- The server refuses a stage run's push that changes a setting in the project's settings file. The
  refusal code names the coordinator's command.
  → § US-07
- The server does not refuse a stage run's push that changes no setting in the settings file, such
  as a change to gates or setup.
  → § US-07

## Acceptance criteria

- **A1.** A project with an implementer cap of 3 never holds more than 3 live implementer runs.
- **A2.** A project's live runs never count against another project's cap.
- **A3.** A project that sets no cap for a stage gets the host cap for that stage.
- **A4.** A project that sets its reviewers pool cap to 2 never holds more than 2 live
  code-reviewer and doc-reviewer runs together, and another project's reviewer runs never count
  against it.
- **A5.** A project that sets its intake cap to 1 opens no second intake while it holds 1 epic in a
  counted document state, though the host intake cap is higher. Per-project counting of epics holds
  today. The new promise is that the project's own value replaces the host value.
- **A6.** A project's merge-handoff schedule fires on that schedule, while another project keeps
  the host schedule. This holds today, and it is a regression criterion.
- **A7.** With runs per tick set to 3 and room under the cap, one tick starts 3 runs of the stage.
- **A8.** A settings change by the command is in force at the next tick, and the server does not
  restart.
- **A9.** Each change by the command is one commit on the default branch that names the caller and
  the reason.
- **A10.** The command refuses a change with no reason, and lands nothing.
- **A11.** The command refuses a value that does not validate, such as a cap of 0, and lands
  nothing. The refusal names the setting.
- **A12.** The command refuses a stage run as caller.
- **A13.** Each change by the coordinator puts one inbox notice that names the setting, the old
  value, the new value and the reason.
- **A14.** The settings read names, for each setting, its value, its source and the commit that
  last changed it.
- **A15.** The settings read says when the last good copy is in force, and why.
- **A16.** The server refuses a stage run's push that changes a setting, and the refusal names the
  coordinator's command.
- **A17.** The server does not refuse a stage run's push that changes no setting.
- **A18.** A cap lowered from 5 to 2 while 5 runs live stops none of the 5, and no new run of that
  stage starts until fewer than 2 runs live.
- **A19.** A change by the coordinator lands with no approval by the operator. The command waits on
  no person, and the inbox notice asks no question.

## Boundaries with other epics

- **Host config and the R35 and KTD30 rule.** The loop plan says "Project config may override
  `schedule` only (R35)". This epic reverses that rule for caps, by the operator's choice "Project
  overrides host" (US-01). The host config file keeps its entries as the defaults. The spec of
  this epic owns the change to the plan's text.
  → docs/plans/2026-10-21-1328-feat-standalone-sdlc-loop-plan.md
- **The default branch's ruleset, U10 and U14.** The loop plan makes a blocked direct push a
  precondition of every project: `project add` and `loop doctor` fail when the default branch
  allows direct pushes (U10), and the ruleset lists the operator as the only bypass actor (U14).
  The plan also commits project config because "it is reviewed like code". US-05's write path,
  "the server commit[s] the change ... on the default branch ... You approve nothing", conflicts
  with all three. On every project that passes `project add`, GitHub refuses the direct commit, so
  A8 and A9 cannot hold as written. The room did not have these facts in front of the operator.
  The open question names the choice, and the spec of this epic owns the change to the plan's
  text once the operator settles it.
  → docs/plans/2026-10-21-1328-feat-standalone-sdlc-loop-plan.md
- **Multi-start in one tick.** factorio-lqo ("parity 8.2: Implementer throughput") builds it. This
  epic adds only the project setting, and US-04 waits on factorio-lqo.
- **The overwatch sweep.** factorio-a7r adds an `overwatch` instance job and a `health` section of
  project config. This epic sets neither. An instance job runs once for the host, not for a
  project, so US-03 does not reach it.
- **Visibility commands.** factorio-wd0 (architecture) owns the operator's and coordinator's
  visibility commands. This epic owns the settings read of US-06. factorio-wd0 may list it.
- **Sign-off and merge.** ADR 0010 makes the operator's approval the sign-off of a PRD or a spec.
  A settings commit is neither, and this epic does not change that rule.
- **Secrets.** R34 and R53 own the secret store and the manifest. This epic changes neither.
- **Checked against:** the loop plan (R35, KTD9, KTD20, KTD30, U10, U14), ADR 0010, the
  factorio-a7r overwatch spec, and the open epics factorio-2u6, factorio-4n0, factorio-9ac,
  factorio-9wh, factorio-a7r, factorio-cd4, factorio-jiv, factorio-wd0 and factorio-wnl. The
  project holds no other PRD. The room found one conflict, with R35 and KTD30, and the operator
  settled it. The doc review found a second, with U10, U14 and "reviewed like code", and it is
  open.

## Out of scope

- A host-wide total that bounds the sum of every project's caps. Never, by the operator's choice
  "Project overrides host".
- Per-project settings for agent jobs that host config defines. They keep today's overrides.
- A per-project schedule for github-reactor. It is an instance job: it runs once for the host, not
  for a project, and one tick polls every project. A schedule for one project would split the
  reactor, and nothing the operator said asks for that (US-03).
- Changes to host config through a command. The operator edits host config as today.
- A drift view between the repository and a server-held copy. Never, because the server holds no
  copy apart from the last good one (US-05).
- A status line of live runs against caps. The operator chose the settings read alone (US-06).
- Secret values and the secret manifest. The secret store owns them (R34).
- Stopping live runs when someone lowers a cap. Never, by the operator's choice "They finish"
  (US-01).

## Open questions

- How does the command land its commit on a default branch that blocks direct pushes? Two answers
  fit the operator's words. The loop App becomes a bypass actor of the default-branch ruleset, and
  the server commits directly. Or the command opens a pull request that the project's merge
  mechanism merges after CI, with no person's approval. The operator chose "Verb lands a repo
  commit" without either fact in front of them, so the operator settles it at sign-off, or the
  spec-author escalates it. Blocks: A8 and A9.
- Does US-03 keep a story? Its requirements hold today. One new promise the code shows is a
  refusal of a job name under `stages` that names no job. Today the server accepts such a name,
  and it does nothing. The operator keeps US-03 with that promise, or drops it, at sign-off. It
  blocks no criterion, because A6 holds either way.
