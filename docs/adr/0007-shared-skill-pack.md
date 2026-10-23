# ADR 0007: Ship the tickertown skills through the release pack as loop-* skills

**Status:** Proposed
**Date:** 2026-10-23
**Ticket:** factorio-cd4

## Context

On 2026-10-23 the operator asked for every tickertown skill in every loop-managed project. Their
words: `all tickertown skills are supposed to be available in every loop managed project`. Bug
factorio-o2w holds the report. Tickertown is the repository at
`/home/operator/projects/alpha`, read on 2026-10-23 at the head of its main branch.

**What tickertown ships.** Measured in source:

- `skills/` holds 21 skills that tickertown wrote or forked. `skills/README.md` lists each one, and
  names the upstream of each fork.
- `.agents/skills/` holds 4 third-party skills as real directories: `asd-ste100`, `impeccable`,
  `neon` and `neon-postgres`. `skills-lock.json` pins the source repository and a hash of each.
- Git tracks `.agents/skills/` and `.claude/skills/`. Each entry in `.claude/skills/` is a symlink
  to `.agents/skills/`. So a checkout of tickertown holds all 25 skills.
- Each of 9 skills holds `disable-model-invocation: true`: `bro`, `boundaries`, `commit`,
  `least-code`, `unslop`, `reader-load`, `verifiable-units`, `test-behavior-not-implementation` and
  `domain-modeling`. A model reads one of them only when a runbook or a person names it.

**What the loop ships.** Measured in this repository:

- `pack/manifest.json` lists 10 skills, each a directory `pack/skills/loop-*`. Plan unit U15 ported
  7 of the 25 tickertown skills: `sdlc-turn`, `sdlc-glossary`, `sdlc-review`, `sdlc-split`,
  `sdlc-triage`, `reflect` and `encode-lessons`.
- The release embeds the pack (`pack/embed.go`). At each run start, `brief.Pack.Write` writes it
  into the checkout under the harness's skill root: `.claude/skills` or `.agents/skills`
  (`internal/brief/pack.go`). `loop register` writes it into the clone too
  (`cmd/loop/register.go`, `installSkills`).
- KTD19 in the plan reserves the `loop-` prefix. `brief.Pack.Skills` refuses a pack entry outside
  it. `brief.CheckTracked` refuses a run with `CONFIG_CONFLICT` when the project tracks a file at an
  injected path. The push scan treats `.claude/skills/loop-` and `.agents/skills/loop-` as injected
  paths (`internal/github/scan.go:26`).
- `make pack-check` runs the vocabulary check on the pack (`internal/pack/gate.go:414`). It fails
  on an owner's name, `pnpm`, `Linear`, `orca` and `TickerTown`.

**The 14 tickertown skills that the pack does not hold.** Measured in source:

- Generic engineering and writing rules: `bro`, `boundaries`, `commit`, `least-code`, `unslop`,
  `reader-load`, `verifiable-units`, `test-behavior-not-implementation` and `domain-modeling`.
  Tickertown's runbooks and `CODING_STANDARDS.md` name each one. A grep for the vocabulary terms
  hits `commit` 3 times, `bro` 2 times, `boundaries` once and `unslop` once. It hits the other 5
  0 times.
- `file-bug`: a person reports a bug in a conversation, and it becomes a Linear ticket with
  screenshots and an assignee. The loop part already exists: `pack/runbooks/coordinator.md:189`
  files a bug with `loop work create --kind bug`. The loop has no attachment verb and no assignee.
- `sdlc-intake`: the rule that orders epics, and how intake cuts tickertown's locked sources into
  epics.
- `sdlc-decisions`, `willis-decisions` and `design-ticket`: who decides what in tickertown, how a
  product decision reaches one named person in Linear, and design requests to one named designer.

**The 4 third-party skills.** Measured in source:

- `asd-ste100` holds an MIT `LICENSE`, and is 88 KB. The loop already holds a fork of its linter
  at `tools/prose/ste-lint.py`.
- `impeccable` is 2.3 MB in 59 files. Its launcher downloads a platform binary on first use. Its
  directory holds no `LICENSE` file.
- `neon` (72 KB) and `neon-postgres` (56 KB) describe one database vendor. Neither directory holds a
  `LICENSE` file.
- Tickertown tracks `.claude/skills/impeccable` and `.claude/skills/unslop` under the bare names.

**What I assumed, and did not measure:**

- The upstream repositories of `impeccable`, `neon` and `neon-postgres` permit a host to fetch and
  use their skills. Nobody here read their licences.
- A skill description costs context in each session that lists it. The descriptions of the 13
  skills this ADR adds hold about 4,400 characters in total. I did not measure the token cost.

## Decision

1. **Port the 9 generic skills into the pack as `loop-<name>`.** Each one becomes
   `pack/skills/loop-<name>/SKILL.md`, in the loop's vocabulary, and passes `make pack-check`. I
   rejected a copy committed in each project, and harness plugins. The plan rejected both for the
   pack, because one version for each install ends copy drift (plan, Key Decisions).

2. **Name every injected skill with the `loop-` prefix, a third-party skill too.** I rejected the
   upstream names. Tickertown tracks `.claude/skills/impeccable` and `.claude/skills/unslop`. So
   `CONFIG_CONFLICT` would refuse every tickertown run, and the push scan would not see the paths
   as injected.

3. **Keep `disable-model-invocation: true` on each ported skill, and name each one from at least
   one runbook in `pack/manifest.json`.** I rejected skills that a model loads on its own judgement.
   An unattended run then reads rules that no runbook chose. A skill that no runbook names reaches
   no unattended run, so the manifest must name it. A person can still start one by name.

4. **Do not port `sdlc-decisions`, `willis-decisions` or `design-ticket`.** They are tickertown
   policy, and the plan puts "design issues, and the Willis routing in
   `skills/sdlc-decisions/SKILL.md`" outside the loop (plan, Scope Boundaries, "Deferred to
   Follow-Up Work"). They stay tracked in tickertown, so tickertown's checkouts still hold them. I
   rejected a port with the names taken out. The rules route to one named person, so nothing is
   left once the names go.

5. **Do not port `file-bug` as a skill.** `pack/runbooks/coordinator.md:189` already holds its loop
   part. Screenshots on a bug wait on epic factorio-9wh, which decides the work-item fields. I
   rejected a second copy of the coordinator's steps, because two copies drift.

6. **Do not port `sdlc-intake` here.** The plan defers intake from a configured source, such as
   tickertown's locked sources (plan, Scope Boundaries, "Deferred for later"). The epic order
   rule belongs to epic factorio-4n0, which decides unattended intake. I rejected a port before
   that epic decides the stage that would read it.

7. **Pin each third-party skill in `pack/third-party.json`, and fetch it at `loop install` and
   `loop upgrade`, instead of vendoring it into the release.** Each entry names the source
   repository, the commit, the skill path and a sha256 over its files. I rejected vendoring. Three
   of the four copies hold no licence, so the release cannot redistribute them. `impeccable` alone
   would add 2.3 MB to each binary. I also rejected a fetch at run start. Each run would then need
   the network, and a fetch failure would count against the item.

8. **Check a third-party skill by its hash alone, not by `make pack-check`.** The vocabulary and
   prose checks would fail on upstream text. An edit to upstream text dies at the next update. I
   rejected a fork of each one, because nobody here edits them. Tickertown keeps the same rule in
   `skills/README.md`.

## Consequences

Files and commands that change, for the spec of epic factorio-cd4:

- `pack/skills/loop-<name>/SKILL.md`: 9 new skills. Each one carries a comment that names its
  tickertown source path and commit.
- `pack/manifest.json`: 9 new entries in `skills`. The runbooks that read each one name it in
  their `skills` list, and in their text by `../loop-<name>/SKILL.md`. The spec chooses each
  reader. Tickertown's readers are the starting point: the implementer reads `commit`, the
  decomposer reads `bro` and `unslop`.
- `internal/pack`: the manifest check fails a skill with `disable-model-invocation: true` that no
  runbook lists.
- `pack/third-party.json`: new. It pins `asd-ste100`, `impeccable`, `neon` and `neon-postgres`.
- `loop install` and `loop upgrade`: fetch each pinned skill into the host's state directory, and
  check its hash. A failed fetch or a wrong hash refuses the install or the upgrade, and the host
  keeps the release it runs.
- `brief.Pack`: the pack the server writes holds the fetched skills as `loop-<name>`. The writer
  changes only the `name:` line of each `SKILL.md`. The pack hash in the brief covers their bytes.
- Stages: no stage changes. Each run of every stage receives 13 more skills.

What this makes easy: each loop-managed project receives the same 13 skills at the same version, with
no file in its repository.

What this makes hard:

- A project that needs a different version of a third-party skill must track its own copy under
  another name.
- An install or an upgrade needs the network to reach GitHub.
- `impeccable` downloads a platform binary on first use, inside a run.

Postponed on purpose:

- The tickertown cutover. A tickertown run under the loop would hold tickertown's tracked
  `sdlc-turn` and the pack's `loop-sdlc-turn`. The plan defers the cutover (plan, Scope
  Boundaries, "Deferred for later").

A later reader must not:

- Inject a skill outside the `loop-` prefix.
- Edit a fetched third-party skill in place. Fork it into `pack/skills/` instead, with its upstream
  in the header.
- Put a name, a tracker or a project's tooling into a ported skill to keep tickertown's wording.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `ls pack/skills | grep -v '^loop-'` prints nothing.
- `ls pack/skills | grep -E 'willis|sdlc-decisions|design-ticket'` prints nothing.
- `grep -rlE '^name: (asd-ste100|impeccable|neon|neon-postgres)$' pack/` prints nothing.
- `grep -rlE '^disable-model-invocation: true' pack/skills` lists only skills that some runbook in
  `pack/manifest.json` lists under `skills`.
- `make pack-check` passes.
- `loop gates --docs` passes.

These criteria wait on later work, and epic factorio-cd4 owns each one:

- `pack/manifest.json` lists `loop-<name>` for each of the 9 generic skills.
- `pack/third-party.json` pins the 4 third-party skills, each with a commit and a sha256.
- `loop upgrade` refuses a release whose fetched skill fails its hash, and the acceptance suite
  proves it.
