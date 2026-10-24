# ADR 0004: Implement the run and stage verbs, add three reads, and leave stalls and CI time to overwatch

**Status:** Proposed
**Date:** 2026-10-24
**Ticket:** factorio-wd0

## Context

The tickertown parity audit of 2026-10-23 compared tickertown's read commands with `loop schema`.
It found gaps in what the coordinator and the operator can see. The operator asked to "identify
all gaps, draft requirements for them, then push them through the entire SDLC process", and asked
for an ADR.

Tickertown is the repository at `/home/operator/projects/alpha`. This run read it on 2026-10-24, at the head of its
main branch.
This run read the loop at the base `6bfba44`, and the CLI and server at `v0.0.64`.

`loop work show factorio-wd0` refuses this run with `CAPABILITY_DENIED`, rule `item_not_held`.
The ticket factorio-onr has no `epic` fact, so the F-16 fix (`internal/server/server.go`,
`readsEpic`) does not apply. The coordinator created the ticket on 2026-10-23 at 14:49Z, before
that fix. So this run read the epic body from the `loop epic create` command in the coordinator's
transcript.

The epic's note moves the timed coordinator wake to epic factorio-a7r and ADR 0009, at the
operator's request on 2026-10-23. This ADR decides no wake.

The plan is `docs/plans/2026-10-21-1328-feat-standalone-sdlc-loop-plan.md`. R22 drops each command
that exists only to work around orca, Linear or the devbox. Its smell triage (Sources / Research)
marks "unassigned items lost in Drafting" as gone by design.

**Gap 1: six verbs are in the schema and have no handler.** Measured on `6bfba44`:

- `loop schema` lists `run show`, `run list`, `run events`, `stage pause`, `stage resume` and
  `work block` as server verbs. No file under `internal` or `cmd`, tests excluded, names any of
  the six as a string literal. Every other server verb has one. `internal/rpc/server.go:478`
  answers `NOT_IMPLEMENTED` for a verb with no handler.
- The epic measured the same six on `v0.0.21`. So the gap stands after 43 releases.
- `pack/runbooks/coordinator.md` section 3 step 1 reads a run with `loop run show <run>` and
  `loop run events <run>`. Section 3 pauses a failing stage with `loop stage pause <stage>`.
- `stage pause` and `stage resume` take a stage and no `--project` flag. No table in
  `internal/store/migrations` holds a paused stage. `internal/sched/quota.go:62-72` pauses a
  harness after a usage limit, and pauses no stage.
- The store already holds what the run verbs read: `runs`, `leases`, `failures` and `alerts`
  (`0001_init.sql`), `holds` (`0004_inbox.sql`), and `transitions` with a `run_id` column
  (`0003_transitions.sql`).
- ADR 0005 decision 2 decides `work block`.

**Gap 2: holds, leases and the in-flight count.** Measured in source:

- Tickertown `pnpm admission` prints each retry hold and each lease, and names an orphan child
  lease (`scripts/sdlc/admission-report.ts:3-35`). `queue inflight` lists every claimed issue in
  a started state (`scripts/sdlc/queue-cap.ts:20-24`).
- In the loop a retry hold is a `retry_limit` alert (`internal/sched/scheduler.go:57`), and a
  blocked run is a `run_blocked` alert. Both are inbox entries that await the coordinator. A room
  hold shows in `loop room list`. An escalation hold shows as a `panel/<id>` inbox entry
  (`internal/inbox/entries.go:261`).
- ADR 0009 ports the `claims` part, which reports a lease whose run ended.
- No loop verb returns the lease, the holds and the counted failures of one item. `loop work
  show` returns the item's status and facts.

**Gap 3: stalls.** Measured in source:

- Tickertown `pnpm stalls` reports time in state, starved epics and idle polls.
- ADR 0009 ports `stalls` as a part of the overwatch sweep, with `sweep` inside it. The spec of
  factorio-a7r (C9 to C11) states its rules. ADR 0009 says the sweep is the one producer of the
  stall list.

**Gap 4: the schedule, the tick history and the stage models.** Measured in source:

- Tickertown `pnpm timers` converges orca automations from `timer-table.ts`.
  `timer-runtime.ts` reads the scheduled execution of each timer. `stage-models.ts` prints the
  pinned model of each role as JSON (`stage-models.ts:201`).
- The loop holds each stage's cap and cron in host config (KTD30). Project config may override
  the schedule (R35). `ThroughputFor` fills a native job's entry with its default at read time
  (`internal/config/throughput.go:111`). The model comes from project config, or from
  `DefaultModels` (`internal/sched/quota.go:24`).
- The `schedule` table holds `last_fired_at` for each cron row.
- No loop verb prints an effective cap, schedule or model. `loop status` prints the instance, the
  pid, the version and the uptime.
- ADR 0009 ports `timer-health`, which reports a timer that stopped firing.

**Gap 5: CI time, merge statistics and the GitHub quota.** Measured in source:

- `ci-minutes.ts` (alpha) counts runner minutes. Its header quotes the operator on 2026-10-21:
  track the minutes "to judge whether hosted Ubicloud runners would cost a reasonable amount".
  CI for tickertown moved from Ubicloud to the devbox on 2026-10-20.
- `ci-timing.ts` (alpha) measures the `gates` job each night and files one decision when it is
  slow.
- `merge-stats.ts` (alpha) prints "the five measures of the Mergify plan over one window". It
  compares the merge queue with tickertown's `land` lanes for one cutover.
- `gh-quota.ts` (alpha) reads the App installation's rate limit. On 2026-10-21 two merger runs read
  the operator's own token pool and blamed a hidden limit, while the installation's pool was the
  one that ran out. One read is not enough: twelve reads at 18:16Z met two counters.
- ADR 0009 ports the `ci` part (CI time on the base branch and in the merge queue) and `flaky`.
- The loop has no `land` lane. The server hands an eligible pull request to the project's merge
  mechanism (KTD29), and `loop merge status` lists each pull request that a mechanism holds.
- The loop backs off on `github.RateLimitError` in the merge hand-off
  (`internal/merge/handoff.go:146-166`). No loop verb reads the installation's remaining quota.

**Gap 6: the children of an epic.** Measured in source:

- Tickertown `epic children` and `epic done` read the children of one epic. `epic done` answers
  the merger's question before it closes an epic (`scripts/sdlc/epic.ts:264`).
- The loop's `work list` takes `--project`, `--kind` and `--status`. `machine.ListFilter` already
  selects by facts (`internal/machine/seams.go:46-52`), and a child names its epic in the `epic`
  fact.
- The server closes an epic itself. When a child ends, the machine applies `complete` to its epic
  (`internal/machine/apply.go:67`). The `complete` row refuses while a child is open
  (`internal/machine/epic.go:175-189`).

**Gap 7: owed feedback and permalinks.** Measured in source and on GitHub:

- Tickertown `pr pending` lists every open pull request that owes a reply, with its ticket,
  oldest first (`scripts/sdlc/pr-queues.ts:139-149`).
- The loop's reactor reads a person's unanswered comment only on the document pull request of an
  epic in an amendable state (`internal/github/reactor.go:439-470`, `awaitsAnswer`). No verb lists
  unanswered comments on a ticket's pull request.
- Tickertown `pr link` prints one permalink. It exists because a stale branch checkout ignored
  `--append`, and pnpm wrote its own line into the links file
  (`docs/agents/code-reviewer.md:122-127`, `scripts/sdlc/pr-review-files.ts:24-30`).
- `pack/skills/loop-sdlc-review/SKILL.md:74` requires a permalink that names a full commit.
  This run read 164 issue comments on slothware-labs/factorio. 144 are review comments of 300
  characters or more. 113 of them hold a `/blob/<40-hex commit>/` link. None holds a branch link
  or a short commit link. 31 hold no link.

**Gap 8: an architecture epic that waits for its ADR.** Measured in source:

- Tickertown `sweep unowned` reports an issue in a person-owned state with no assignee
  (`scripts/sdlc/sweep.ts:85-108`).
- The loop has no assignee. The inbox derives entries from escalated items, decisions in
  `needs_human`, and epics in `prd_signoff` and `spec_signoff` (`internal/inbox/entries.go:55-60`).
  No entry names an epic in `architecture`.
- No stage takes an epic in `architecture`. Only the coordinator creates the ADR ticket (KTD12).
  The `stalls` part of ADR 0009 reads only states that a stage takes (spec of factorio-a7r, C10).

**Gap 9: the judge check.** Measured in source:

- Tickertown `decide --check` asks whether every judge model id and Jev answer, and judges
  nothing (`scripts/sdlc/decide.ts:117-140`).
- The loop's judge ids are constants in `internal/escalation/judge.go:76-78`. The Jev key is the
  project secret `TYPESAFE_API_KEY`, which the server writes into the fixer's `.env`
  (`judge.go:23-33`). The server calls Jev never (KTD22).
- `loop doctor` checks the install, the harnesses, the repositories and the remote GitHub lines
  (`internal/release/doctor.go:41-69`). It checks no judge and no Jev key.

**Gap 10: the newest release.** Measured in source:

- Bug factorio-obc reported that the coordinator told the operator to cut a release. Releases are
  automatic: each push to `main` that passes CI becomes the next release
  (`pack/runbooks/coordinator.md:276-279`).
- `loop version` prints the installed version only. `release.Source.LatestVersion` reads the
  newest published release (`internal/release/upgrade.go:164-190`), and only `loop upgrade` with
  no version calls it.

**What I assumed, and did not measure:**

- That the coordinator reads a run's history more often than it reads SQLite by hand. No
  transcript count backs this.
- That a person's comment on a ticket's pull request goes unanswered in practice. I found the
  missing read in source, and I counted no such comment.
- That the 31 review comments with no link cite nothing. I did not read each one.
- That GitHub's two counters for one installation, which tickertown measured, apply to the loop's
  App too.

## Decision

1. **Implement `run show`, `run list` and `run events`.** `run show` returns the run row, its
   lease, its failure record and the alerts that name it. `run list` takes `--project`, `--stage`
   and `--live`. `run events` returns the transitions with the run's `run_id`, and the audit rows
   of the run's actor, oldest first. I rejected removing the verbs from the schema, because
   section 3 of the coordinator runbook reads a run with them. I rejected reading SQLite by hand.
   Only the server writes its store (R11), and a migration changes the tables under a hand read.

2. **Count in-flight runs with `run list --live`.** I rejected a port of `queue inflight` as its
   own verb, because the live runs answer the same question.

3. **Implement `stage pause` and `stage resume` with a reason and a project.**
   `stage pause <stage> [--project <id>] --reason "<why>"` writes one row to SQLite. With no
   `--project`, the pause holds in every project. Admission starts no new run of a paused stage,
   and a live run continues. Each pause is an inbox entry that awaits the coordinator until
   `stage resume`. I rejected a cap of 0 in host config instead. That is an operator's config
   edit, and it records no reason. I rejected a pause with no inbox entry, because a forgotten
   pause stops a stage with no signal.

4. **Add `loop stage list` for the coordinator and the host.** For each project and stage, it
   prints the effective cap, the effective schedule, the harness and model, the pause and its
   reason, the last and the next fire, and the count of live runs. I rejected a port of `timers`,
   `timer-runtime` and `stage-models` as three verbs, because one table answers all three. I
   rejected a read of the host config file. Defaults fill at read time (ADR 0009) and project
   config overrides the schedule (R35), so the file is not the effective value. The verb prints
   values. ADR 0009's `timer-health` part judges a timer that stopped.

5. **Explain one item's wait in `work show`.** `work show` adds `lease`, `holds`, `failures` with
   the limit, and the open alerts that name the item. I rejected a port of `pnpm admission`. A
   retry hold already reaches the coordinator as a `retry_limit` alert, a room hold shows in
   `room list`, an escalation hold shows as a `panel/` entry, and ADR 0009's `claims` part reports
   an orphan lease. What is missing is one read for one item.

6. **Leave stalls and CI time to the overwatch sweep.** ADR 0009 ports `stalls`, `sweep`, `ci`
   and `flaky`, and names the sweep as the one producer of the stall list. I rejected a second
   stall verb, because two producers would disagree. I rejected a time field on each inbox entry
   for the same reason.

7. **Port neither `ci-minutes` nor `merge-stats`.** `ci-minutes` is tickertown policy: the
   operator asked it to price hosted runners for one repository. The plan holds no requirement for
   CI cost: a case-insensitive search of the plan for `runner minute`, `ubicloud`, `ci cost` and
   `ci-minutes` finds nothing. A project that wants it keeps it in its own tooling. `merge-stats`
   is obsolete under the loop's design. It compared Mergify with tickertown's `land` lanes for one
   cutover, and the loop has no `land` lane (KTD29). I rejected both as loop verbs, because no
   stage acts on either number.

8. **Read the GitHub quota in `loop doctor-github`.** The server reads `GET /rate_limit` with the
   installation's token several times, and prints the lowest `core` and `graphql` values and the
   count of counters it met. I rejected tickertown's `--min` floor and its exit code, because no
   loop act waits on that guard. The server already backs off on a rate limit. I rejected a read
   of the operator's own token, because tickertown measured that it reads the wrong pool.

9. **Filter `work list` by epic, and port no `epic done`.** `work list --epic <id>` lists the
   items whose `epic` fact names the epic. I rejected an `epic children` verb, because the filter
   already exists in `machine.ListFilter`. `epic done` is obsolete under the loop's design: the
   server closes an epic when its last child ends. The plan closes an epic only from `execution`,
   with every child terminal (KTD24). The smell triage behind R14 marks two `Done` writers as must
   fix, and a hand-run `epic done` would be the second writer.

10. **Add `loop pr pending` for the coordinator, and port no `pr link`.** `pr pending [--project]`
    lists each open pull request that holds a person's unanswered comment, with its item and the
    age of the oldest comment. It uses the reactor's `awaitsAnswer` rule. I rejected a route that
    sends a ticket's comment to a stage, because that is a new transition, and ADR 0005 owns the
    work-item transitions. I rejected a port of `pr link`. The loop ships one pinned binary (R40),
    so no branch holds an older CLI. The measured comments hold no branch link.

11. **Put each architecture epic with no ADR ticket in the inbox.** The inbox gains an
    `architecture/<project>/<epic>` entry that awaits the coordinator. It holds while the epic is
    in `architecture` and no open item names the epic in its `epic` fact. I rejected tickertown's
    assignee rule, because the loop has no assignee, and the plan marks that smell as gone by
    design. I rejected a new overwatch part, because the coordinator is the actor that must create
    the ticket, and the inbox is the view of who must act (KTD21).

12. **Check the judges in `loop doctor`, and probe no model.** For each project, `loop doctor`
    reports whether `TYPESAFE_API_KEY` is present, as `loop secret check` answers. It also reports
    whether the `claude` and `codex` binaries of the judges are on the path. I rejected a live
    probe of each model id. The ids are constants of the release, so a wrong id is a loop defect.
    The fixer reports it as a loop fault at its first use. I rejected a call to Jev from the host,
    because KTD22 keeps every Jev call inside the fixer run.

13. **Report the newest release in `loop version`.** `loop version` adds `latest`, read through
    `LatestVersion`, and `behind`. A failed read leaves `latest` empty, adds the error, and still
    exits 0. I rejected a verb that lists the commits on `main` after the newest release. Each green
    push to `main` releases, so `main` holds unreleased work only while a release runs or after it
    failed. ADR 0009's `main` part reports a red run at the head of the base branch.

## Consequences

Files, commands and stages that change, for the spec of epic factorio-wd0:

- `internal/cli/surface.go`: `run list` gains `--project`, `--stage` and `--live`. `stage pause`
  and `stage resume` gain `--project`, and `stage pause` gains `--reason`. `work list` gains
  `--epic`. New verbs: `stage list` and `pr pending`. `gen/` regenerates with `make proto`.
- `internal/server`: handlers for `run show`, `run list`, `run events`, `stage pause`,
  `stage resume`, `stage list` and `pr pending`.
- `internal/store/migrations`: one additive migration with a table of stage pauses.
- `internal/admission`: the work query starts no run of a paused stage.
- `internal/machine`: `work show` returns the lease, the holds, the counted failures and the open
  alerts. `work list` passes `--epic` to `ListFilter.Facts`.
- `internal/inbox/entries.go`: two new sources, a paused stage and an architecture epic with no
  open ADR ticket.
- `internal/github`: `pr pending` shares `awaitsAnswer` with the reactor. The doctor verb adds
  the quota line.
- `internal/release/doctor.go`: the judge lines.
- `cmd/loop`: `loop version` prints `latest` and `behind`.
- `pack/runbooks/coordinator.md`: section 1 reads `loop stage list`, `loop pr pending` and
  `loop version`. Section 3 pauses a stage with `--reason`. Section 8 creates the ticket for an
  architecture epic that the inbox names.
- Stages: none added and none removed.

What this makes easy: the coordinator explains one run, one stage and one item without reading
SQLite. A paused stage and a waiting architecture epic each reach the inbox.

What this makes hard:

- A stage pause now needs a reason, and it stays in the inbox until someone resumes it.
- `pr pending` reads the comments of each open pull request, so it spends GitHub quota on each
  call. The spec bounds that read.
- Every `loop version` call reads GitHub once.

Postponed on purpose:

- A pause that ends at a time. A stage that must resume on its own forces it.
- A route for a person's comment on a ticket's pull request. A measured comment that no one
  answered forces it, through epic factorio-9wh or a later one.

A later reader must not:

- Add a second producer of the stall list beside the overwatch sweep.
- Pause a stage by a config edit, or with no recorded reason.
- Read the store's tables from a runbook.
- Add a verb that reads only tickertown's `land` lanes or one repository's runner bill.
- Probe a judge model or call Jev from the host.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `grep -c '^\*\*Ticket:\*\* factorio-wd0$' docs/adr/0004-operator-visibility.md` prints `1`.
- `ls docs/adr | grep -c '^0004-'` prints `1`.
- `grep -rn -E 'ci-minutes|merge-stats|pr link|epic done|epic children' pack/runbooks pack/skills`
  prints nothing.
- `loop schema | grep -o '"path":"[a-z-]* [a-z-]*"' | grep -c -E '"(epic (children|done)|pr link)"'`
  prints `0`.
- `loop gates --docs` passes.

Epic factorio-wd0 owns these, because each waits on its spec and tickets:

- A test in `internal/server` proves that each server verb of the schema has a handler, so no
  coordinator call answers `NOT_IMPLEMENTED`.
- A test proves that a paused stage starts no run, that a live run continues, and that the pause
  survives a server restart.
- A test proves that an epic in `architecture` with no open item that names it makes one inbox
  entry, and that the entry goes when the coordinator creates such an item.
- `loop schema` lists `stage list` and `pr pending`, and `loop work list --help` lists `epic`.
- `loop version` prints a `latest` field.
