# ADR 0012: The base audit passes an ADR merge on a new `adr_merged` record

**Status:** Proposed
**Date:** 2026-10-27
**Ticket:** factorio-fgza

## Context

ADR 0003 § Decision, G5, decided a detective audit of the base branch: on each tick, the reactor
walks the first-parent commits of the base from its last audited commit, and it raises one audit
alert for each commit that is no item's recorded merge at a head that a review passed. The
operator split that audit out of factorio-2u6 into this epic on 2026-10-27 (question/7: "Split
(Recommended)"). Work entries 7 and 8 and contracts C19 to C27 and C31 of
`docs/plans/factorio-2u6-quality-ratchets-spec.md` at `c3e0aff79b` hold the design that the split
removed.

This room read the loop at the base `6f3bbd6` on 2026-10-27. This room measured each fact below,
except where it says assumed.

- `gh api repos/slothware-labs/factorio/branches/main/protection` still answers HTTP 403,
  "Upgrade to GitHub Pro or make this repository public". So GitHub refuses no merge, and the audit
  stays the only check after a merge. The project's merge mechanism is `direct`
  (`.loop/config.yaml`).
- Since `307ae81` (pull request #4), main holds 201 first-parent commits. All 201 are merge
  commits with the subject "Merge pull request". None is a squash and none is a direct push. The
  loop's App made 196. The operator made 5: two spec merges (#6, #13) and three pull requests of
  no item (#40, #51, #87).
- The c3e0aff design passes a commit only when an item's history records it as `merged_sha`,
  `prd_merged` or `spec_merged` (C20, C22).
- An ADR merge records no commit. `adr_landed` sets only the ADR's path, as the fact `adr`
  (`internal/machine/epic.go:354`, `mark(FactADR, "path")`). The reactor passes the base head of
  the tick as its `sha` parameter (`internal/github/reactor.go:388-389`), and that head is not
  always the commit that added the ADR.
- An ADR pull request has no review stage. The operator merges it on GitHub, and that merge is the
  sign-off (`pack/skills/loop-architect-room/SKILL.md` section 4). Main holds ten ADRs.
- `tickProject` runs `pollPulls`, `adrs`, `pollCI`, `amendments` and `eligibility`, in that order
  (`internal/github/reactor.go:202-223`). No `auditBase`, `MergeOf` or `spec_merged` exists yet.

So under the c3e0aff design, each ADR merge raises a false `unreviewed_merge` alert. That holds
for every architecture epic, this one too. ADR 0003 G5 does not name ADRs.

## Decision

**ADR 0003 G5 stands, and an ADR merge passes the base audit on a new fact, `adr_merged`, that
`adr_landed` records with the commit that added the ADR.** The operator chose this on 2026-10-27:
"Record adr_merged (Recommended)".

1. **The record.** When the reactor applies `adr_landed`, it also sets `adr_merged` to the
   first-parent commit of the base that added the ADR file. The reactor finds that commit in the
   mirror, and it makes no GitHub request.
2. **The rule.** `MergeOf` returns a record for an `adr_merged` commit, as it does for
   `merged_sha`, `prd_merged` and `spec_merged`. The audit passes that commit with no reviewed-head
   rule, because the operator's merge of an ADR is its sign-off.
3. **The order.** `auditBase` runs after `adrs` in `tickProject`, so an ADR merge has its record
   on the tick that first sees it.

The operator also chose the scope of this ADR: "Amend G5 by D1 only (Recommended)". This ADR
decides only the ADR merge. The spec of this epic rebuilds the rest of the audit from c3e0aff
work entries 7 and 8 and contracts C19 to C27 and C31, and it decides again the rules that
c3e0aff added: an outran document merge, a walk that stops for 15 minutes, and a rewritten base.

Rejected:

- **Pass a commit whose diff touches only `docs/adr/*.md`.** It needs no machine change. But an
  edit to an ADR that landed, or an ADR whose Ticket line reads `none`, would pass with no record.
  The audit would then miss a change to the architecture that skipped the loop.
- **Raise an alert on every ADR merge.** It keeps the c3e0aff design unchanged. But it raises one
  false alert for each architecture epic, ten so far, and a person dismisses each one.
- **Lift the c3e0aff rules into this ADR.** The outran, stall and rewritten-base rules would get
  the operator's sign-off by merge. But each one is a choice inside G5, which the spec records,
  and a change to the 15-minute figure would then need a new ADR.

## Consequences

What changes:

- **The machine.** `internal/machine/rows.go` gains `FactADRMerged = "adr_merged"`. The
  `adr_landed` row of `internal/machine/epic.go` sets it from a new parameter.
  `adr_misjoined` clears it with `adr`.
- **The reactor.** `internal/github/reactor.go` `adrs` finds the adding commit in the mirror and
  passes it to `adr_landed`. `tickProject` calls `auditBase` after `adrs`.
- **The audit.** `MergeOf` and the C22 rule of the spec treat `adr_merged` as a recorded merge
  with no reviewed-head check.

What this makes easy: the operator merges an ADR, and the inbox gets no alert for it.

What this makes hard: a tick on which `adrs` fails has no record for an ADR that landed. The spec
must give an error from `adrs` the same partial-read rule that c3e0aff C25 gives an error from
`pollPulls`. The audit then stops at that commit, and it never raises a false alert for it.

What still raises `unreviewed_merge`:

- An edit to an ADR that landed, or a second ADR for an epic past `architecture`. Neither sets
  `adr_merged`.
- An ADR whose Ticket line reads `none`, or names no epic in `architecture`.
- A pull request of no item, such as #40, #51 and #87. ADR 0003 G5 decided that such a merge
  reaches the inbox.

A later reader must not:

- pass a commit by the paths its diff touches.
- apply a reviewed-head rule to an `adr_merged` commit. No stage reviews an ADR.
- set `adr_merged` to the base head of the tick, unless that head added the ADR.

Assumed, not measured: the operator merges an ADR pull request as one commit on the first-parent
line, as a merge or a squash. A rebase merge puts each commit on that line, and only the one that
added the file gets the record. This is wrong if one ADR merge raises an `unreviewed_merge` alert.

## Acceptance criteria

- `loop gates --docs` passes.
- `grep -c '^\*\*Ticket:\*\* factorio-fgza$' docs/adr/0012-base-audit-adr-merges.md` prints `1`.
- `ls docs/adr | grep -c '^0012-'` prints `1`.
- `grep -c 'adr_merged' docs/adr/0012-base-audit-adr-merges.md` prints at least `1`.

Epic factorio-fgza owns these checks, through its spec and tickets:

- A test proves that a first-parent commit with no merge record raises one `unreviewed_merge`
  alert, and that the next tick raises no second alert. This is A5 of the c3e0aff spec.
- A test proves that `adr_landed` sets `adr_merged` to the commit that added the ADR, when the
  base head of that tick is a later commit.
- A test proves that the merge of an ADR whose Ticket line names an epic in `architecture` raises
  no alert on the tick that first sees it.
- A test proves that a commit that edits an ADR that landed raises one `unreviewed_merge` alert.
- A test proves that on a tick whose `adrs` step fails, the walk stops before an unrecorded ADR
  merge, and a later tick passes it.
