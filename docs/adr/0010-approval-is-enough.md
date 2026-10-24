# ADR 0010: The operator's approval is the sign-off, and the server merges and upgrades on it

**Status:** Proposed
**Date:** 2026-10-24
**Ticket:** factorio-8qm

## Context

The operator, 2026-10-24: `why do i have to do all these myself?`, then `do everything you can do
by yourself. make sure this is part of the loop skills/runbooks. do not ask me unless you
absolutely have to.`

This section marks each fact. **Measured** means read in source at this commit. **Assumed** means
not measured.

### What happened

- **Measured** (the ticket). The operator approved two spec PRs in the coordinator's chat ("both
  PRs LGTM") and chose to upgrade the host. The coordinator handed back three steps: `gh pr merge`
  twice, the `install.sh` upgrade, and a read of the version.

### Two loop rules made the hand-back the only legal move

- **Measured.** `pack/runbooks/coordinator.md` section 12 said "Never merge a PRD or a spec. The
  operator's merge is the sign-off." `pack/skills/loop-sdlc-turn/SKILL.md` section 8 said "A person
  merges a PRD or a spec, and that merge is the sign-off." The glossary defined a sign-off as "the
  operator's merge".
- **Measured.** `internal/inbox/answer.go` refused `loop inbox answer signoff/...` with rule
  `signoff_by_merge`: "no inbox answer replaces it". So the operator's approval had no record in
  the loop, and the click on GitHub was the only sign-off.
- **Measured.** `upgrade` and `install` are host verbs. `internal/rpc/capabilities.go`
  `checkHostCaller` refuses a host verb when the caller or any ancestor works inside a registered
  checkout, with rule `host_verb_from_checkout`. The coordinator always runs inside its checkout,
  so `cd ~` does not help. The coordinator got past the rule only with a `systemd-run --user`
  unit.
- **Measured.** `pack/runbooks/coordinator.md` section 4 said "Never end a turn with 'shall I do
  it?'". The two rules above made that impossible for a sign-off and an upgrade.

### What the loop already had

- **Measured.** `internal/github/merge_api.go` `MergePR` merges a PR only while its head is the
  named sha. GitHub refuses the merge with 409 when the head moved.
- **Measured.** The doc-reviewer passes with `loop review submit <epic> --head <sha>`
  (`pack/runbooks/doc-reviewer.md` section 3). The epic table recorded no head on a document pass.
- **Measured.** The reactor applies `doc_merged` on any merge of the current document PR, and
  `MergeObserved` answers Nothing for an epic already past sign-off. So a merge the server makes
  and the reactor reads later moves the epic once.
- **Measured.** The release upgrader (`internal/release/upgrade.go`) drains the server, stops its
  service, installs, and starts the service again. A process in the service's cgroup dies on the
  stop. **Assumed:** the unit keeps systemd's default `KillMode=control-group`.

## Decision

The operator's approval at one head is the sign-off. The coordinator records it with the
operator's own words, and the server merges the document PR at that head. No agent merges, so
`loop-sdlc-turn` section 8 still holds. On the operator's decision the coordinator asks the server
to upgrade, and the server starts `loop upgrade` outside its service and outside every checkout.

1. **The pin.** A document's `review_pass` records `reviewed_head` from `--head`. The sign-off
   entry shows it in `head`.
2. **The answer.** `loop inbox answer signoff/<project>/<epic> --body "<the operator's words>"`
   reads the document PR. When its head is the pinned head, the server writes the words to the
   audit log as `signoff_answered`, merges with `MergePR` at that head, and applies the merge to
   the epic at once.
3. **A moved head.** When the head differs, the server merges nothing. It applies
   `signoff_head`, a new server-only row that keeps the epic in sign-off and pins the new head.
   That transition raises the entry again with a new `at`. The answer refuses with
   `STATUS_CHANGED` and rule `head_moved`. A 409 from GitHub between the read and the merge takes
   the same path.
4. **The upgrade.** `loop coordinator upgrade [<version>] --body "<the operator's words>"` is a
   server verb that only the coordinator principal may call. The server starts `loop upgrade` as a
   transient `systemd-run --user` unit on Linux, and in a session of its own elsewhere. The
   process works in the home directory, so the host-verb check admits it. The CLI's host verbs
   and every stage run keep the `host_verb_from_checkout` refusal.
5. **The rule, as a check.** The pack gate refuses a hand-back in the coordinator's runbook and in
   each skill and agent it reads: a person who merges, upgrades or installs, `gh pr merge`, or a
   person who runs a loop verb the coordinator may call (`internal/pack/handback.go`).

Rejected:

- **Let the coordinator run `gh pr merge`.** It breaks section 8 of the turn skill, and it merges
  whatever head the PR holds at the click. The server's merge names the head the operator saw.
- **Merge at the PR's current head, whatever it is.** A push after the review would merge with no
  one reading it. The pin and the refusal keep the approval tied to one head.
- **Treat a moved head as a new review.** The ticket asks for the entry to be raised again at the
  new head. The operator reads the new head and approves it or not. A person who wants a review
  comments on the PR, and the amender takes it.
- **Lift `host_verb_from_checkout` for the coordinator's process.** The rule stops a stage run
  from upgrading the host through its ancestry. A carve-out by process tree is the hole the rule
  closes. A server verb under the capability matrix admits the coordinator's token and no other.
- **Answer "upgrade" to a fired decision.** It needs a decision item for each upgrade, and the
  coordinator files that item itself. A verb with the operator's words in the audit log records the
  same decision with one call.

## Consequences

- The coordinator applies a sign-off and an upgrade in the turn the operator gives them. Its
  report lists only approvals and decisions under "Blocked on you".
- A person can still merge a document PR on GitHub. The reactor reads that merge as before, and it
  is a sign-off too. The pack no longer tells anyone to do it.
- A sign-off merge comes from the loop's App, not from the operator's account. The audit row
  `signoff_answered` holds the operator's words, the PR and the head.
- An epic that passed review before this change has no pinned head. Its first answer pins the
  current head and refuses, so the operator approves a head the entry named.
- `loop coordinator upgrade` returns once the upgrade process started. The server then drains and
  restarts, so the result of the upgrade lands in the unit's journal. The coordinator reads the
  version back with `loop version` after the restart.
- Files: `internal/machine/epic.go`, `internal/inbox/signoff.go`, `internal/inbox/entries.go`,
  `internal/server/upgrade.go`, `cmd/loop/upgradestart.go`, `internal/cli/surface.go`,
  `proto/loop/v1/services.proto`, `internal/pack/handback.go`, `pack/runbooks/coordinator.md`
  and the skills that defined a sign-off as a merge.
- Do not add a pack line that sends the operator to run a step the coordinator can run. The pack
  gate refuses it.

## Acceptance criteria

- `go test -tags acceptance ./acceptance/scenarios -run TestSignoffAnswer` passes: an answer with
  the operator's words merges the document at its pinned head and the epic leaves `prd_signoff`
  and `spec_signoff`, and a moved head merges nothing and raises the entry again at the new head.
- `go test ./internal/server -run TestCoordinatorTokenStartsAServerUpgradeAndARunTokenIsRefused`
  passes.
- `go test ./internal/pack -run 'Hand|RecordsTheApproval'` passes, and `make pack-check` passes.
- `grep -rn "operator's merge is the sign-off" pack/` returns nothing.
