# ADR 0003: Ratchet any stack gate on a keyed baseline, and audit the base branch in the reactor

**Status:** Proposed
**Date:** 2026-10-24
**Ticket:** factorio-2u6

## Context

The tickertown parity audit of 2026-10-23 found eight gaps in how the loop ratchets lint and
dependency findings, and in how it audits what lands on the base branch. This ADR splits the first
gap into G1 and G2, so it decides nine questions. The operator asked to
"identify all gaps, draft requirements for them, then push them through the entire SDLC process",
and asked for an ADR. Tickertown is the repository at `/home/operator/projects/alpha`, read on
2026-10-24 at the head of its main branch. This run read the loop at the base `0d6a85f`.

The epic body names the gaps. `loop work show factorio-2u6` refuses this run with
`CAPABILITY_DENIED` (`item_not_held`), because the ticket carries no `epic` fact (loop fault F-16).
So this run read the body from the `loop epic create` command in the coordinator's transcript, as
ADR 0006's run did under F-8.

This run measured each fact below in source. The last list names what it assumed.

**G1: no lint baseline ratchet.** Measured:

- The loop's `lint` stack gate runs the command that `.loop/config.yaml` names, and it reads only
  the exit code (`internal/gates/stack.go`, `stackGate`). A project with old lint debt must hold
  it at zero, or switch rules off.
- The loop ratchets inline suppressions only: `.loop/suppressions.json`, keyed
  `<file>::<rule>`, in both directions (`internal/gates/standards.go:189`, `ratchet`).
- Tickertown's `pnpm lint:gate` (`scripts/sdlc/lint-gate.ts`) counts oxlint's JSON diagnostics by
  `<file>::<rule>`, and it applies the same two-way ratchet to `lint-baseline.json`. That file
  holds 36 keys and 65 findings, in two legacy apps.

**G2: no baseline writer.** Measured:

- Tickertown's `pnpm lint:baseline` writes every found count (`lint-gate.ts:112`). A careless
  run once blessed unfixed code, so the gate now refuses a baseline key outside the two legacy
  apps (`lint-gate.ts:152`). That path list is tickertown policy.
- The loop has no writer. An agent edits `.loop/suppressions.json` by hand.

**G3: no accepted-advisory baseline.** Measured:

- The loop's `audit` gate runs only when a change touches a lock file
  (`internal/gates/stack.go:38`). It has no baseline. An advisory with no fixed version fails
  every change to that lock file.
- Tickertown's `audit-baseline.json` accepts one esbuild advisory, with its package, severity,
  fixed range, date and a reason of one paragraph. The gate fails on an accepted id that the
  audit no longer reports.

**G4: no scheduled full audit.** Measured:

- Tickertown runs `audit-deps.ts --all` in `.github/workflows/audit.yml` at `37 3 * * *`. The
  overwatch `audit` part reads the newest scheduled run (`scripts/sdlc/overwatch-audit.ts`). That
  part exists because the run failed on 2026-10-21 and 2026-10-22, and nobody read it.
- The loop's CI has one workflow, `.github/workflows/ci.yml`, on pull request and push.
- ADR 0009 ports the overwatch `audit` part, and states that project config names its workflow.
  No config key names one today (`internal/config/project.go`).

**G5: no detective audit of the base branch.** Measured:

- `gh api repos/slothware-labs/factorio/branches/main/protection` answers HTTP 403, "Upgrade to
  GitHub Pro or make this repository public". The repository is private. So GitHub refuses no
  merge, and the `direct` mechanism is the only preventive check
  (`internal/github/rules.go:91`: "the loop checks CI itself before a direct merge").
- Since the first loop merge, `307ae81` (pull request #4), all 21 first-parent commits on main
  are merges of loop pull requests. The 59 earlier first-parent commits are direct pushes from
  the bootstrap.
- The reactor already syncs the base on each tick (`internal/github/reactor.go:197`,
  `tickProject`). It records the merge commit of each item's pull request as the `merged_sha`
  fact (`internal/machine/rows.go:24`).
- Tickertown's `audit-main.ts` resolves each first-parent commit after a floor commit to its pull
  request, then reads the required checks of that head from `required-checks.json`. Its header
  says that it runs daily. No workflow, timer or runbook in tickertown runs `audit:main`. Only a
  plan names it, as a command a person ran by hand.

**G6: no worktree-size rule.** Measured:

- Tickertown's `stale-anchors.ts` holds two rules. The loop ports `unreachable-commit` as
  `unreachable_commit` (`internal/gates/standards.go:268`). It does not port `worktree-size`.
- The correction ledger of tickertown traces the rule to 29 sites that quoted the disk size of a
  worktree, in prose about `pnpm bootstrap` and `pnpm reclaim`.
- A search of the loop's tracked Markdown and Go for a size figure beside the word worktree,
  checkout or `node_modules` finds no site.

**G7: the runbooks gate checks only the loop's own commands.** Measured:

- The loop's `runbooks` gate checks each `loop` command against `loop schema`, each
  `loop gates --only <name>`, each tracked path, each count and each claim about what an agent
  reads (`internal/gates/runbooks.go`). It never reads `make <target>` or `pnpm <script>`.
- Tickertown's `runbook-check.ts` checks each `pnpm` script against `package.json`, and each verb
  and flag against the `case` arms of its own dispatcher scripts.
- Tickertown keeps three excuse lists for untracked paths (`runbook-excuses.ts`). `NOT_YET` is
  empty today. The loop's `concreteOf` already cuts a path at its first placeholder or glob.
- Tickertown pins each claim about what an agent reads to one row of a fixed table, `READINGS`
  (`runbook-claims.ts:69`), with a quote as its evidence. The loop compares the claims with each
  other, and the agent's own runbook wins (`internal/gates/runbooks_claims.go:188`).

**G8: no writer for the lessons ledger.** Measured:

- Tickertown's `pnpm lessons file` writes one ledger row (`lessons-file.ts`). It runs the ledger
  rules on the new document first, and refuses a row that adds a finding. It also rewrites the
  summary block.
- The loop's `lessons` gate runs the same rules on every row. When the summary block is stale,
  its finding prints the block to paste (`internal/gates/lessons_summary.go`).

**G9: `secret check` reads only the server store.** Measured:

- Tickertown's `env:check` exists because an implementer read its shell, found no key and
  escalated. The key sat in a dotenv file that `pnpm bootstrap` copied into that worktree.
- In the loop, the server writes every project secret into the run's `.env` (plan R34, KTD18).
  The brief names each secret that is present and each one with no value. A run whose manifest
  names a secret with no value does not start (R53). The store refuses an empty value
  (`internal/secrets/store.go:79`). So the store never holds an empty value.

Assumed, not measured:

- The `mergify` and `github-queue` mechanisms land each pull request as its own merge commit on
  the first-parent line. The spec of factorio-2u6 checks this against the parity cases in
  `internal/merge/parity_test.go`.
- Each lint and audit tool that a project names can print one finding per line, through its own
  JSON output and `jq`. This holds for oxlint, golangci-lint, ruff, govulncheck and `pnpm audit`.

## Decision

| Gap | Verdict | Where it lands |
| --- | --- | --- |
| G1 | Port | Any config gate with a `baseline` key |
| G2 | Port, lower-only | `loop gates --only <gate> --update` |
| G3 | Port | The `audit` gate, with a reason for each entry |
| G4 | Port | A scheduled project workflow, read by ADR 0009's `audit` part |
| G5 | Port | A new step of the reactor tick |
| G6 | Obsolete | Not ported |
| G7 | Port the command check | `make` targets and package scripts only |
| G8 | Not ported | The `lessons` gate stays the one encoding |
| G9 | Obsolete | Not ported |

**G1. A config gate with a `baseline` key ratchets the keys its command prints, and a gate with
none keeps its exit-code rule.** Each stdout line is one finding key, such as `<file>::<rule>`.
The gate counts the keys and holds them to the baseline in both directions, with the same
`ratchet` function as the suppressions. We reject a parser in the loop for each lint tool,
because the loop runs any stack and the config already names the command. We reject SARIF as
the contract, because several audit tools do not print it, and `jq` would still be needed.

**G2. `loop gates --only <gate> --update` lowers each count to the found count and deletes each
stale key, and it never raises a count or adds a key.** When no baseline exists, it writes the
whole set once, for a project that adopts the loop with debt. A raise stays a hand edit that a
reviewer reads in the diff. We reject tickertown's writer, which wrote every count, because that
writer blessed unfixed code once. We reject its list of paths that may carry debt, because the
list names two tickertown apps. `loop gates --only standards --update` lowers
`.loop/suppressions.json` by the same rule.

**G3. The `audit` gate ratchets advisory ids on the same baseline, and each accepted id must hold
a reason and a date.** The gate refuses an entry with an empty reason. We reject the ignore lists
of each tool's own config, such as `pnpm.auditConfig.ignoreCves`, because they hold no reason and
nothing reports a stale entry.

**G4. A project's scheduled CI workflow runs `loop gates --only audit --full`, and ADR 0009's
`audit` part reads its newest run.** `--full` skips the lock-file scope. The pull request rule
stays as it is: a change that touches no lock file is not audited. We reject a native server job
that runs the audit, because the server would then run project code on the host outside any run,
and CI already holds the toolchain. The loop's own repository gets the workflow too.

**G5. On each tick, the reactor walks the first-parent commits of the base from its last audited
commit, and it raises one audit alert for each commit that is no item's `merged_sha` at a head
that a review passed.** The cursor starts at the base head on the first tick, so the audit needs no floor
commit and reports no old history. A person's merge of a reviewed PRD or spec passes,
because that merge is the sign-off. We reject a port of `audit-main.ts`, because it reads the
checks of each head through GitHub again, while the server already holds each merge it made. We
also reject a daily schedule, because tickertown's daily audit had no trigger, and the reactor
ticks anyway.

**G6. The loop does not port `worktree-size`.** The figures that it caught were disk sizes in
prose about `pnpm bootstrap` and `pnpm reclaim`. Plan R22 drops both commands, and the server
deletes checkouts by their records. The loop's tracked files hold no such figure. We reject a port
of a rule with no site to guard, because each standards rule costs a scan of every file. A
project that needs it adds the rule to its own `lint` command.

**G7. The `runbooks` gate checks that each `make <target>` names a target of a tracked
`Makefile`, and that each `pnpm`, `npm run`, `yarn` or `bun run` script names a script of a
tracked `package.json`.** It checks no flag of a project command. We reject a flag check, because
tickertown reads flags from the `case` arms of its own scripts, and no other project writes its
commands that way. We reject excuse lists, because `NOT_YET` is empty, and `concreteOf` already
lets a worked example name a path through a placeholder. We reject the `READINGS` table, because
the loop's claim rule needs no row for each pair, and the agent's own runbook already decides.

**G8. The loop adds no command that writes a ledger row or the summary block.** The `lessons`
gate refuses every malformed row before a push, and its finding prints the summary block. We
reject a writer, because it would encode the nine columns a second time, beside the gate.

**G9. The loop does not port `env:check`.** The server writes the `.env`, the brief names each
secret, and `loop secret check` answers for the store that the `.env` comes from. We reject a
check of the process environment and of dotenv files, because the false report that caused
`env:check` came from a copy that `pnpm bootstrap` made, and R22 drops `bootstrap`.

## Consequences

What changes:

- **Project config.** `internal/config/project.go`: a `Command` gains an optional `baseline`, a
  repository path. Config load refuses a `baseline` on `format`, `typecheck`, `test` and `build`.
- **The stack gates.** `internal/gates/stack.go`: a gate with a `baseline` reads stdout keys and
  calls `ratchet`. The audit gate's scope check reads `--full`.
- **The ratchet.** `ratchet` moves out of `internal/gates/standards.go` into a file that the
  standards gate and the stack gates share. An audit baseline entry holds a reason and a date.
- **The verb.** `loop gates` gains `--update` and `--full`. `--update` needs `--only`. The CLI
  surface regenerates `gen/` with `make proto`.
- **The runbooks gate.** `internal/gates/runbooks.go` reads `make` targets and package scripts.
- **The reactor.** `internal/github/reactor.go` adds a base audit step to `tickProject`. A cursor
  for each project holds the last audited commit. A new alert kind names the commit and its
  subject, and the inbox shows it.
- **CI.** `.github/workflows/audit.yml` runs `loop gates --only audit --full` once a day. The spec
  of epic factorio-a7r names the config key through which overwatch finds the workflow.
- **The pack.** `pack/skills/loop-init/SKILL.md` scaffolds the audit workflow and, for a project
  with debt, one `--update` run. The implementer and code-reviewer runbooks state that a raised
  baseline count needs a reason in the pull request body.

What this makes easy: a project with old lint debt joins the loop, and new debt still fails. A
merge outside the loop reaches the inbox on the next tick.

What this makes hard: a project must write a `jq` line for each ratcheted tool. A tool that prints
nothing when it fails looks like zero findings. The spec decides how a ratcheted gate tells a
silent tool from a clean one, and the ratchet must never read silence as zero.

A later reader must not:

- add a parser for one lint or audit tool to the loop's code.
- let `--update` raise a count or add a key to a baseline that exists.
- audit every dependency on a pull request that touches no lock file.
- audit the base on a timer of its own, apart from the reactor tick.
- port `worktree-size`, `env:check`, the excuse lists, `READINGS` or a ledger writer without a new
  ADR that names the fault behind it.

Postponed on purpose: the severity floor of the audit. Tickertown fails on every advisory. The
spec of factorio-2u6 decides whether a project may name a floor.

## Acceptance criteria

- `loop gates --docs` passes.
- `grep -c '^\*\*Ticket:\*\* factorio-2u6$' docs/adr/0003-quality-ratchets-and-audits.md` prints
  `1`.
- `ls docs/adr | grep -c '^0003-'` prints `1`.
- `grep -c '^| G[0-9] |' docs/adr/0003-quality-ratchets-and-audits.md` prints `9`. Every gap has
  one verdict.
- `grep -rn 'worktree.size' internal/gates` prints nothing.
- `loop schema | grep -c '"noun":"lessons"'` prints `0`.

Epic factorio-2u6 owns these checks, through its spec and tickets:

- `go test ./internal/gates/...` proves that a gate with a `baseline` fails on a count above the
  baseline and on a count below it.
- A test proves that `--update` lowers a count and never raises one.
- A test proves that an audit baseline entry with an empty reason fails the gate.
- `grep -c 'loop gates --only audit --full' .github/workflows/audit.yml` prints `1`.
- A test proves that a first-parent commit with no `merged_sha` raises one audit alert, and that
  the next tick raises no second alert.
- A test proves that a runbook naming a `make` target that no `Makefile` holds fails the
  `runbooks` gate.
