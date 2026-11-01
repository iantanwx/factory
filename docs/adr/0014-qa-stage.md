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
  the loop App on three kinds of head. It posts on the reviewed head of each `ready_to_merge` and
  `queued` ticket, "on the reviewed sha only" (`internal/github/status.go:49-83`, line 58). It
  posts on the head of a settings change (`internal/settings/land.go:154-188`). It posts on a
  merge-group commit of the github-queue mechanism (`internal/merge/ghqueue/group.go:34-90`).
- `base_merged` moves a queued ticket's reviewed head to the server's merge of a moved base, with
  no new review (F-66, `internal/machine/ticket.go:206-210`, `internal/merge/handoff.go:322-333`).
  `loop/eligible` follows that new reviewed head.
- `loop/eligible` never counts as CI. `RequiredChecks` takes every required context of the ruleset
  but `loop/eligible` (`internal/github/merge_api.go:209-216`). A review pass waits until each of
  those checks is green at the head, or gets `CI_NOT_GREEN` (`internal/github/verbs.go:470-506`).
  `Classify` skips a status named `loop/eligible` (`internal/github/reads.go:153-156`).
  `loop register` drops it from the CI checks it finds (`cmd/loop/register.go:370`). `loop doctor`
  counts any other required context as the CI check (`internal/github/rules.go:113-114`).
- The default-branch ruleset requires the CI check and `loop/eligible`, with the loop App as its
  source (`docs/operating.md:433-436`). The operator is on its bypass list, so the sign-off merge
  of a PRD or a spec needs no `loop/eligible` (`docs/operating.md:439-440`).
- The loop App holds checks at read only. Its write scopes are push, pulls, statuses, actions and
  merge (`internal/github/token.go:24-39`, `docs/operating.md:80-83`). No scope can create a check
  run. A commit status holds a state, a description of at most 140 characters and one link.
- `loop work create --kind bug` refuses any caller but the coordinator with `CAPABILITY_DENIED`
  and the reason `coordinator_only` (`internal/pipeline/adhoc.go:52-62`).
- The run stages are the eleven names of `runStages` in `internal/config/throughput.go:46-51`,
  with their constants at lines 30-44. None is a QA stage.
  Each stage's cap lives in the `stages` section of `.loop/config.yaml`.
- The simplify spec, `docs/plans/factorio-1r94-simplify-stage-spec.md`, sets a `simplify_due` fact
  on `review_reject`. The next implementer hand-off then enters `in_simplify`, not `in_review`
  (lines 76-87). That epic is not built yet.

Assumed, not measured: GitHub counts a check run with the conclusion `neutral` as a pass of a
required check. GitHub's documentation states this, and this run did not test it on a ruleset.

## Decision

**This ADR supersedes ADR 0002 gap G2 where its words "the QA states" would reject the loop's own
QA fields, and ADR 0005 decision 9 where it rejects a bug source and says that only the coordinator
files a bug. Where they differ from this ADR on those parts, this ADR wins.** The loop's QA fields
are the ticket state `in_qa`, the settings `stages.qa.enabled` and `stages.qa.cap`, the check run
`loop/qa` and the bug source `qa`. Each one is a field of the loop, and none names a product path.
G2 still rejects each QA, design, frame or art rule on a ticket body, and no decision here adds
one. The rest of both ADRs stands. ADR 0005 still rejects `--needs` and screenshot attachments.
This ADR puts the stage cap and an evidence file in their place.

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

3. **The QA report is a check run named `loop/qa`, which the server posts as the loop App. Like
   `loop/eligible`, `loop/qa` is a loop context, and it never counts as CI.** On the head that a
   QA run read, its conclusion is `success` on a pass, `failure` on a fail, and `neutral` when the
   project has no `verify-<app>` skill. Its text names each check that ran, fuzzy UAT and visual QA
   when it ran, with pass or fail for each. It links each evidence file it cites. A screenshot is
   one such file, never an attachment in beads, and no body, fact or note holds image data. A QA
   run never writes to GitHub itself. It hands its report to the server with a loop verb, and the
   server posts it.

   The server posts `loop/qa` on each head that it posts `loop/eligible` on, and it posts
   `loop/eligible` only on a head that already holds `loop/qa`. So the direct mechanism and the
   Mergify queue condition, which read `loop/eligible`, need no second read. On each head that no
   QA run read, the conclusion is `neutral`, and the text names the reason:

   - The reviewed head of a ticket in a project with the stage off. The text says that the stage
     is off.
   - A queued ticket's head after `base_merged`, the server's merge of a moved base (F-66). The
     merge commit carries the QA pass as it carries the review pass. The text links the QA report
     on the reviewed head that the merge built on.
   - The head of a settings change. It changes `.loop/config.yaml` only, and no QA run reads it.
   - A merge-group commit of the github-queue mechanism. The sweep posts it on each group commit
     that gets `loop/eligible`. Each PR of the group is eligible at its queued head only after its
     QA pass.

   `RequiredChecks` and the CI discovery of `loop register` leave `loop/qa` out, as they leave
   `loop/eligible` out, so a review pass never waits on it. `Classify` skips a check run named
   `loop/qa`, so a QA fail never reads as a red build. The ruleset that `loop register` makes
   requires `loop/qa` with the loop App as its source, next to `loop/eligible`, and `loop doctor`
   checks both. The operator's bypass covers a document's sign-off merge for both contexts. The App
   gains checks at write, because only a GitHub App with that permission can create a check run.

   I rejected a `loop/qa` that counts as CI, because a review pass would then wait on a check that
   only the pass can start. I rejected a return to `in_qa` on `base_merged`. Each moved base would
   then cost a QA run and hold the ticket out of the queue. F-66 lets the merge commit carry the
   review pass, because the merge has no conflict and no shared file, and the QA pass takes the
   same risk under the same guard. I rejected heads with no `loop/qa` from the server's own
   merges, because each settings change, merge group and moved base would then wait on a required
   check that never posts. I rejected a section in the pull request body, because the operator
   chose a check run, and a section cannot block a merge. I rejected a commit status for the
   report. A status holds one line of 140 characters and one link. A check run holds a result for
   each check and a link for each evidence file, and the operator chose it. I rejected the report
   folded into `loop/eligible`, because a person who reads the pull request could then not tell a
   QA fail from a review that has not passed. I rejected a check run that the QA run posts with a
   token of its own, because a run holds no GitHub token, and the server is the one writer of
   GitHub.

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
- `internal/github/merge_api.go`: `RequiredChecks` leaves out `loop/qa`.
- `internal/github/reads.go`: `Classify` skips a check run or a status named `loop/qa`.
- `internal/github/status.go`: `EnqueueEligibility` posts `loop/qa` with `neutral` on a reviewed
  head with the stage off and on a head after `base_merged`. It posts `loop/eligible` only on a head
  that already holds `loop/qa`.
- `internal/settings/land.go`: `await` posts `loop/qa` with `neutral` before `loop/eligible`.
- `internal/merge/ghqueue/group.go`: the sweep posts `loop/qa` with `neutral` on a group commit
  before `loop/eligible`.
- `internal/github/rules.go`: `loop doctor` checks that the ruleset requires `loop/qa` from the
  loop App, and its CI line counts neither loop context.
- `internal/github/token.go`: a checks write scope. `loop install` asks for it in the App
  manifest, and `loop doctor` allows it. A host whose App lacks it fails `loop doctor`, and the
  operator accepts the new permission on GitHub.
- `cmd/loop/register.go`: the ruleset requires `loop/qa`, and the CI discovery drops it.
  `docs/operating.md` names it in the ruleset steps.
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

- Merge a ticket at a head that no QA run read while the project's QA stage is on. The one
  exception is the server's merge of a moved base into a queued ticket's reviewed head (F-66). It
  carries the QA pass as it carries the review pass.
- Count `loop/qa` as CI, or make a review pass wait on it.
- Post `loop/eligible` on a head that holds no `loop/qa`.
- Return a QA fail to `repair`, or past code review.
- Add a `needs` lock, or any other lock, on the browser.
- Store a screenshot or other image data in beads.
- Add a bug source that names a product path or one project's stage.
- Let any run other than a QA run file a bug. The coordinator still files each bug that the
  operator reports.
- Edit ADR 0002 or ADR 0005.

ADR 0002 and ADR 0005 keep their text. ADR 0002 gap G2 still rejects tickertown's QA, design,
frame and art rules on ticket bodies, because each names tickertown's product paths. No decision
here adds a rule on a ticket body. ADR 0005 decision 9 still rejects `--from`, `--needs`,
screenshot attachments and a duplicate check at filing. Decision 10 still rejects a production-bug
marker, and priority 1 still covers that case.

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
- A test proves that a review pass at a head with green CI and no `loop/qa` is not refused, in a
  project whose ruleset requires `loop/qa`.
- A test proves that a `loop/qa` check run with the conclusion `failure` does not make `Classify`
  read the head as red.
- A test proves that the server posts `loop/qa` with `neutral` before `loop/eligible` on a
  settings change's head, on a merge-group commit and on a queued ticket's head after
  `base_merged`.
- A test proves that a QA fail moves the ticket to `todo` with the entry `rework` and
  `simplify_due` set.
- A test proves that a QA report posts one `loop/qa` check run on the head the run read, with the
  conclusion `neutral` in a project with no `verify-<app>` skill.
- A test proves that a QA run can file a bug with `--source qa`, and that an implementer run gets
  `CAPABILITY_DENIED`.
- A test proves that no more QA runs run at one time than `stages.qa.cap` allows.
