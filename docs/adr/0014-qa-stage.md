# ADR 0014: A QA stage runs between code review and merge, reports in a required check run, and files bugs

**Status:** Proposed
**Date:** 2026-11-01
**Ticket:** factorio-nhb5
**Binds:** all

## Context

Epic factorio-nhb5 adds a QA stage to the build phase, with fuzzy UAT and visual QA. Its PRD is
`docs/prd/qa-stage.md`, merged at `c42f690`. The epic body records decision D2. The operator chose
a kernel stage with a new ADR, after code review and before merge. That ADR supersedes named parts
of two earlier ADRs, and nobody edits either one.

Two earlier ADRs reject parts that this epic adds:

- `docs/adr/0002-document-and-ticket-gates.md`, gap G2, rejects tickertown's QA states and design
  rules on ticket bodies, because each names tickertown's product paths.
- `docs/adr/0005-work-item-fidelity.md`, decision 9, rejects a bug's `--source`, the `needs`
  lock and screenshot attachments. Its reason for `--source` is that only the coordinator files a
  bug in the loop, so the source is always the operator. Decision 10 gives the same reason when it
  rejects tickertown's production-bug marker: no loop bug carries a source.

The operator answered four questions at PRD sign-off on 2026-11-01. The epic body records each
answer, and this ADR records the four:

- `On; no skill passes (Recommended)`. Every project gets the stage, and a project can turn it
  off. A run in a project with no skill at `.claude/skills/verify-<app>/` passes the ticket with a
  note, and overwatch reports the missing skill.
- "Rework, then review (Recommended)". A QA fail is a rework reason, as a review reject is.
- "Check run (Recommended)". The QA report is a required check run that can block the merge, and
  it links its evidence files.
- "QA stage files it (Recommended)". The QA stage is a second bug filer next to the coordinator,
  with `--source qa`.

This run read the loop at the base `c42f690` on 2026-11-01. It measured each fact below.

- The ticket table moves a ticket from `in_review` to `ready_to_merge` on `review_pass`, and to
  `todo` on `review_reject` (`internal/machine/ticket.go:199-202`). `reviewReject` counts the
  reject, sets the entry `rework` and clears `reviewed_head` (`internal/machine/ticket.go:45-54`).
- The merge hand-off takes a `ready_to_merge` ticket when its pull request's head holds the
  `loop/eligible` status (`internal/merge/handoff.go:61-65`). The server posts `loop/eligible` as
  the loop App on the reviewed head only (`internal/github/status.go:46-56`).
- The default-branch ruleset requires the CI check and `loop/eligible`, with the loop App as its
  source (`docs/operating.md:433-436`).
- The loop App holds checks at read only. Its write scopes are push, pulls, statuses, actions and
  merge (`internal/github/token.go:24-39`, `docs/operating.md:80-83`). No scope can create a check
  run.
- `loop work create --kind bug` refuses any caller but the coordinator with `CAPABILITY_DENIED`
  and the reason `coordinator_only` (`internal/pipeline/adhoc.go:52-62`).
- The run stages are the ten names in `internal/config/throughput.go:31-50`. None is a QA stage.
  Each stage's cap lives in the `stages` section of `.loop/config.yaml`.
- The simplify spec, `docs/plans/factorio-1r94-simplify-stage-spec.md`, sets a `simplify_due` fact
  on `review_reject`. The next implementer hand-off then enters `in_simplify`, not `in_review`
  (lines 76-87). That epic is not built yet.

Assumed, not measured: GitHub counts a check run with the conclusion `neutral` as a pass of a
required check. GitHub's documentation states this, and this run did not test it on a ruleset.

## Decision

**This ADR supersedes ADR 0002 gap G2 where it rejects QA fields, and ADR 0005 decision 9 where
it rejects a bug source and says that only the coordinator files a bug. Where they differ from
this ADR on those parts, this ADR wins.** The rest of both ADRs stands. ADR 0005 still rejects
`--needs` and screenshot attachments. This ADR puts the stage cap and an evidence file in their
place.

1. **A QA stage, `qa`, takes each ticket after its code review passes and before it merges.** The
   ticket table gains the state `in_qa`. `review_pass` moves a ticket to `in_qa` when the project's
   QA stage is on, and to `ready_to_merge` when it is off. A QA pass moves the ticket from `in_qa`
   to `ready_to_merge`. So the merge hand-off and `loop/eligible` stay as they are, and they read a
   ticket that QA passed. A bug's shaped ticket takes the same path. Each project has the stage on
   by default. A project turns it off with `stages.qa.enabled: false`. The stage cap,
   `stages.qa.cap`, limits how many QA runs use a browser at one time, and no `needs` lock exists.
   A QA run in a project with no `verify-<app>` skill passes the ticket with a note in its report,
   and the overwatch sweep reports that project's missing skill.

   I rejected a QA step inside the code-reviewer's runbook, which decision D2 rejected. A review
   would then hold two jobs at one cap, and the browser would hold a reviewer slot. I rejected a
   stage that a project turns on, because the operator chose "On". I rejected a fail or an
   escalation for a project with no `verify-<app>` skill. Each project without one would then stop
   every ticket at QA on the day this stage ships. I rejected a fact on `ready_to_merge` in place
   of a state, because the merge hand-off would then need a guard that reads it, and the
   transition table could not refuse a merge before QA.

2. **A QA fail is a rework reason, as a review reject is.** A QA fail moves the ticket from
   `in_qa` to `todo` with the entry `rework`, clears `reviewed_head`, and sets `simplify_due`. The
   implementer reads the QA report as the rejection. Then the simplify run takes the new head, then
   code review, then QA again at the new reviewed head. A QA run that ends with no report fails
   nothing. Its release returns the ticket to `in_qa` for the next QA run.

   I rejected the `repair` state, which the epic body first named. `repair` means a red build, and
   the implementer reads failed CI jobs there, not a report. A `fix_pushed` from `repair` also
   skips code review, so a QA fix would merge with no review of its code. I rejected a fail that
   returns to `in_review` and skips the implementer, because nobody writes the fix there.

3. **The QA report is a check run named `loop/qa`, which the server posts as the loop App on the
   head that the QA run read.** Its conclusion is `success` on a pass, `failure` on a fail, and
   `neutral` when the project has no `verify-<app>` skill. When the stage is off, the server posts
   `loop/qa` with `neutral` on the reviewed head at the review pass, so the required check passes
   with no QA run. Its text names each check that ran, fuzzy UAT and visual QA when it ran, with
   pass or fail for each. It links each
   evidence file it cites. A screenshot is one such file, never an attachment in beads, and no
   body, fact or note holds image data. The ruleset that `loop register` makes requires `loop/qa`
   with the loop App as its source, next to `loop/eligible`. The App gains checks at write. A QA
   run never writes to GitHub itself. It hands its report to the server with a loop verb, and the
   server posts it.

   I rejected a section in the pull request body, because the operator chose a check run, and a
   section cannot block a merge. I rejected a commit status for the report, because a status holds
   one line of 140 characters and cannot list the checks or link the evidence. I rejected the
   report folded into `loop/eligible`, because a person who reads the pull request could then not
   tell a QA fail from a review that has not passed. I rejected a check run that the QA run posts
   with a token of its own, because a run holds no GitHub token, and the server is the one writer
   of GitHub.

4. **The QA stage files a bug with `loop work create --kind bug --source qa`, as a second filer
   next to the coordinator.** A bug that the coordinator files carries the source `operator`. A
   bug that the QA stage files carries `qa`, and it needs no `--request`. Its body quotes the QA
   report and the head it read in place of the operator's words. No other run may file a bug. The
   refusal `coordinator_only` becomes a refusal for any caller but the coordinator and a QA run.
   The `--source` values are `operator` and `qa`, and no value names a product path or a stage of
   one project.

   I rejected a QA run that asks the coordinator to file each bug, because the operator chose "QA
   stage files it". The bug would then wait on the coordinator's next turn, and the report would
   pass through a second writer. I rejected tickertown's source values, such as `qa-stagehand`,
   which ADR 0005 rejected because they name one project's stages. I rejected a bug filer open to
   every run, because a run then files a fault of its own work as a bug of the project.

The PRD's other open questions stay with the planner: when QA files a bug and when it only fails
the ticket, how a run picks `<app>` in a project with two apps, where an evidence file lives after
the checkout goes, what bounds a fuzzy UAT run, and whether a visual QA fail alone fails the
report.

## Why an ADR

- **More than one epic:** every ticket of every project passes through `in_qa`, and the simplify
  epic factorio-1r94 must set `simplify_due` on a QA fail as it does on a reject.
- **Rejected alternative:** a QA step in the code-reviewer's runbook, a `repair` return, a pull
  request section, and a coordinator that files each QA bug.
- **Costly to undo:** it adds a ticket state, a required check in each project's ruleset, a
  GitHub App permission and a bug field, so it changes stored state, a trust boundary and a public
  interface.
- **How, not what:** it decides how the QA stage of US-01, US-05 and US-06 of
  `docs/prd/qa-stage.md` fits the flow, reports and files.

## Consequences

Files and commands that change, for the spec of epic factorio-nhb5:

- `internal/machine/ticket.go`: the state `in_qa`, in `prOpenStates`. `review_pass` lands on
  `in_qa` or `ready_to_merge` by the stage setting. A QA claim row, a pass row to `ready_to_merge`
  and a fail row to `todo` with the reject's facts and `simplify_due`. `head_changed` also starts
  from `in_qa`. Release and reclaim rows for `in_qa`.
- `internal/config/throughput.go`: the run stage `qa`, with its cap and `enabled`, in the optional
  keys so an older host config still loads.
- `internal/github/token.go`: a checks write scope. `loop install` asks for it in the App
  manifest, and `loop doctor` allows it. A host whose App lacks it fails `loop doctor`, and the
  operator accepts the new permission on GitHub.
- `cmd/loop/register.go`: the ruleset requires `loop/qa`. `docs/operating.md` names it in the
  ruleset steps.
- `internal/pipeline/adhoc.go`: `work create --kind bug` takes `--source`, and admits a QA run.
- `internal/overwatch`: a part that reports each project with the QA stage on and no
  `verify-<app>` skill.
- The implementer runbook: a QA fail is a rejection, so a rework after one is no loop fault.
- The glossary: the QA stage in the stage table and the build phase.

What this makes easy: a person reads one check on the pull request to see what QA did and its
evidence. A QA fix goes through the same review as any fix.

What this makes hard:

- Each QA fail costs an implementer run, a simplify run, a review and a QA run.
- Each existing host must accept the App's new permission before any project's ruleset requires
  `loop/qa`. A ruleset that requires it first blocks every merge of the project, also with the
  stage off, because no `loop/qa` can post. The spec names that order, and how `loop doctor` and
  the overwatch sweep report a host that breaks it.
- A ticket in flight in `ready_to_merge` when the stage ships carries no QA pass. The spec states
  whether it merges or returns to `in_qa`.

A later reader must not:

- Merge a ticket at a head that no QA run read while the project's QA stage is on.
- Return a QA fail to `repair`, or past code review.
- Add a `needs` lock, or any other lock, on the browser.
- Store a screenshot or other image data in beads.
- Add a bug source that names a product path or one project's stage.
- Let any run other than a QA run file a bug. The coordinator still files each bug that the
  operator reports.
- Edit ADR 0002 or ADR 0005.

ADR 0002 and ADR 0005 keep their text. ADR 0002 gap G2 still rejects tickertown's QA states and
design rules on ticket bodies, because the loop's QA fields name no product path. ADR 0005
decision 9 still rejects `--from`, `--needs`, screenshot attachments and a duplicate check at
filing. Decision 10 still rejects a production-bug marker, and priority 1 still covers that case.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `loop gates --docs` passes.
- `ls docs/adr | grep -c '^0014-'` prints `1`.
- `grep -c '^\*\*Ticket:\*\* factorio-nhb5$' docs/adr/0014-qa-stage.md` prints `1`.
- `grep -c -e 'docs/adr/0002' -e 'docs/adr/0005' docs/adr/0014-qa-stage.md` prints at least `2`.
- `git diff --quiet origin/main -- docs/adr/0002-document-and-ticket-gates.md docs/adr/0005-work-item-fidelity.md`
  exits `0`.

Epic factorio-nhb5 owns these checks, through its spec and tickets:

- A test proves that `review_pass` moves a ticket to `in_qa` in a project with the QA stage on,
  and to `ready_to_merge` in a project with it off.
- A test proves that the merge hand-off takes no ticket in `in_qa`.
- A test proves that a QA fail moves the ticket to `todo` with the entry `rework` and
  `simplify_due` set.
- A test proves that a QA report posts one `loop/qa` check run on the head the run read, with the
  conclusion `neutral` in a project with no `verify-<app>` skill.
- A test proves that a QA run can file a bug with `--source qa`, and that an implementer run gets
  `CAPABILITY_DENIED`.
- A test proves that no more QA runs run at one time than `stages.qa.cap` allows.
