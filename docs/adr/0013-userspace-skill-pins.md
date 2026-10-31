# ADR 0013: A userspace skill pin keeps its upstream name, and the host fetches it when a project declares it

**Status:** Proposed
**Date:** 2026-10-31
**Ticket:** factorio-6bba

## Context

Epic factorio-yzrt splits the skills into kernel skills and userspace skills. Its PRD is
`docs/prd/kernel-and-userspace-skills.md` in pull request #233, which is still open.
A kernel skill ships in the release pack under the `loop-` prefix. A userspace skill belongs to one
project. A project declares a userspace pin in `.loop/config.yaml` under a `skills:` list, with the
fields of a kernel pin: name, source repository, commit, path and sha256.

Two decisions of `docs/adr/0007-shared-skill-pack.md` bind every pin today, and both block that
PRD:

- **Decision 2** names every injected skill with the `loop-` prefix, a third-party skill too. It
  gives two reasons. First, tickertown tracks `.claude/skills/impeccable` and
  `.claude/skills/unslop`, so `CONFIG_CONFLICT` would refuse every tickertown run. Second, the push
  scan would not see an unprefixed path as injected.
- **Decision 7** fetches each pin at `loop install` and `loop upgrade`, and nowhere else. It
  rejects a fetch at run start, because each run would then need the network, and a fetch failure
  would count against the item.

The operator chose an ADR before the spec of factorio-yzrt, on 2026-10-31. Their choice, recorded
on epic factorio-6bba: "The host fetches a userspace pin at `loop register`, and again when the
server reads a changed `skills:` list in the project config. A fetch at run start stays rejected,
as decision 7 states."

This run read the loop at the base `680b32e` on 2026-10-31. It measured each fact below.

- `brief.SkillPrefix` is `loop-` (`internal/brief/pack.go:19`). `brief.Pack.Skills` refuses a pack
  entry outside the prefix (`internal/brief/pack.go:72`).
- `brief.CheckTracked` refuses a run with `CONFIG_CONFLICT` when git tracks a file under an
  injected skill directory (`internal/brief/exclude.go:30`). Each run start and each pack refresh
  calls it (`internal/brief/brief.go:335`, `internal/brief/brief.go:379`).
- The push scan knows the injected skill paths by two fixed prefixes, `.claude/skills/loop-` and
  `.agents/skills/loop-` (`internal/github/scan.go:34`). It reads no project config.
- `pack.BriefOf` lays each kernel pin out as `loop-<name>/` when its hash matches, and returns each
  pin that fails (`internal/pack/embed.go:30`). The server serves without a failed kernel pin and
  raises one alert, `third_party_skills_missing`, that names no project (`cmd/loop/serve.go:266`,
  `internal/config/lkg.go:22`). The comment on that alert says "The server never fetches a skill".
- `loop install` and `loop upgrade` place the kernel pins in `versions/<v>/skills` of the
  instance (`internal/release/layout.go:57`), through `release.SkillFetcher`
  (`internal/release/skills.go:25`). An upgrade to a new version starts an empty directory.
- `loop register` writes the pack into the project's clone (`cmd/loop/register.go:199`). It reads
  no project config for skills.
- The server reads each project's `.loop/config.yaml` from its default branch through
  `config.Loader` (`internal/server/sched.go:119`). The scheduler caches a read for 30 seconds
  (`internal/server/sched.go:85`). A config that fails `Loader.Check` does not load. The last good
  config stays in force, and the alert `project_config_invalid` names the project
  (`internal/config/lkg.go:16`).
- The server already reaches the network: the GitHub actor, the reactor and the mirror fetch run
  in it.

Assumed, not measured: the upstream text of a userspace skill names its own siblings by their
upstream names, as `neon` names `neon-postgres` (ADR 0007, Context). This holds for the two Neon
skills, which are the first userspace pins. A later pin can differ.

## Decision

**ADR 0007 decisions 2 and 7 still bind each kernel pin in `pack/third-party.json`. For a
userspace pin, this ADR replaces both.** Where the two ADRs differ on a userspace pin, this ADR
wins.

1. **A userspace skill keeps its upstream name, and never takes the `loop-` prefix.** A kernel
   skill keeps the prefix. The writer copies each byte of a userspace pin, the `name:` line too.
   This decision answers the two reasons of decision 2:

   - **A tracked directory with a pin's name.** The server refuses the pin when it loads the
     project config. The load reads `.claude/skills/<name>` and `.agents/skills/<name>` at the
     default branch for each pin name. When git tracks either one, the config does not load. The
     alert `project_config_invalid` names the pin and the tracked path, and the last good config
     stays in force. The same load refuses a pin name that starts with `loop-`, and two pins with
     one name. So no project's runs fail with `CONFIG_CONFLICT` because of a pin it declared.
     A tracked directory is a project's own committed userspace skill. Each project chooses the
     pin or the directory for one name, never both.
   - **The push scan.** The scan of a project's push treats each skill directory of that project's
     userspace pins as injected, under both skill roots, with the two kernel prefixes. It reads the
     pin names from the project's last good config. A pin's directory is an exact match:
     `.claude/skills/neon` covers `.claude/skills/neon/SKILL.md`, and does not cover
     `.claude/skills/neon-postgres/SKILL.md`. The server refuses a push that adds a file under an
     injected pin directory, as it refuses a push that touches the pack today.

   I rejected the `loop-` prefix on a userspace pin, which ADR 0007 decided for every pin. A
   project skill would then pose as a kernel skill, against US-01 of the PRD, and the config
   refusal of a `loop-` pin name could not exist. I rejected a second reserved prefix, such as
   `project-<name>`. It would let the scan keep two fixed prefixes. But the upstream text names its
   siblings by their upstream names. ADR 0007, Consequences, records the cost: a model that does
   not match `neon-postgres` to its prefixed name fetches the skill again or installs it into the
   checkout. The upstream name removes that mismatch. I rejected a refusal at run start alone, by
   `CheckTracked`. That refuses every run of the project, which is the failure decision 2 names. I
   rejected a scan that treats every path under `.claude/skills/` as injected. It would refuse the
   committed userspace skill directory that the PRD allows.

2. **The host fetches a userspace pin at `loop register`, and again when the server reads a
   changed `skills:` list in a project's config.** A list changes when it names a pin that the
   host does not hold at that commit and that sha256. So a pin that failed its fetch stays
   changed, and each later read of the config fetches it again. The fetch runs in the server
   process and in `loop register`, never inside the start of a run, and no run waits for it.

   - A pin that fails its fetch or its hash raises one alert for its project. The alert names each
     such pin. Runs of that project start without the pin, as runs start without a failed kernel
     pin today. Runs of every other project stay as they are. The alert closes when each pin of
     the project passes its check.
   - `loop register` fetches each pin of the project, checks its hash, and writes each pin that
     passes into the clone with the kernel skills. A pin that fails does not fail the register.
     The envelope names it, and the server's next read of the config fetches it again.
   - The host keeps a fetched userspace pin in the instance's state directory, by project, outside
     `versions/<v>/`. An upgrade does not fetch it again.

   I rejected, again, a fetch at run start, for the reason decision 7 gives. Each run would then
   need the network, and a fetch failure would count against the item. I rejected a fetch at
   `loop install` and `loop upgrade` alone, which decision 7 decided for kernel pins. The install
   reads no project config, so a pin merged into a project would wait for the next release. I
   rejected a config load that fails on a failed fetch. A network fault would then freeze the
   project's stage and pool settings at the last good config, and the alert would name a valid
   file as invalid.

## Consequences

Files and commands that change, for the spec of epic factorio-yzrt:

- `internal/config`: the project config gains the `skills:` list. The load refuses a pin name
  that starts with `loop-`, two pins with one name, and a pin whose directory the default branch
  tracks under either skill root. A new alert kind names a project and its pins that fail their
  fetch or hash. `third_party_skills_missing` stays for kernel pins and names no project.
- `internal/github/scan.go`: `injected` takes the project's pin directories with the two fixed
  prefixes.
- `internal/brief`: the pack a run holds adds each userspace pin of its project that passed its
  check, under its own name. `CheckTracked` and `Exclude` take those directories with the kernel
  ones. `brief.Pack.Skills` still refuses a kernel entry outside `loop-`.
- `internal/pack/embed.go`: the rename of the `name:` line applies to kernel pins only.
- `cmd/loop/register.go`: `registerSkills` fetches and writes the project's pins.
- `cmd/loop/serve.go`: the comment on `skillsAlert` that says the server never fetches a skill
  applies to kernel pins only. The server fetches userspace pins.
- `internal/release/skills.go`: `SkillFetcher` serves a userspace pin too, with no change to its
  hash rule.

What this makes easy: a project pins a skill in its own config, and its runs hold that skill under
the name its upstream text uses. A failed pin stops no other project.

What this makes hard:

- The server needs the network to reach the pin's source repository, not only GitHub's API. Today
  each pin source is a GitHub repository, so the reach is the same.
- The push scan reads project config. A scan with no last good config for the project treats only
  the kernel prefixes as injected.
- One pull request cannot both drop a pin and commit a directory with its name. The scan reads the
  pins of the last good config, so it refuses the directory. The project lands the two changes in
  two pull requests.
- An operator's direct commit can still track a pin's directory after the pin loads. The config
  then fails its load and raises an alert, and the last good config keeps the pin. `CheckTracked`
  refuses each run of the project with `CONFIG_CONFLICT`, which does not count, until the operator
  removes the pin or the directory.
- A run that starts while a pin's first fetch runs holds no copy of that pin. The brief lists the
  skills a run holds, so the run can tell.

A later reader must not:

- Prefix a userspace pin with `loop-`, or rewrite any byte of its text.
- Fetch any pin at run start, or make a run wait for a fetch.
- Fail a project's config load because a pin's fetch failed.
- Inject a skill outside the `loop-` prefix that is not a userspace pin of the run's own project.
  The rest of ADR 0007's rule stands.

ADR 0007 keeps its text. Its decisions 2 and 7, and its rule against injecting a skill outside
the `loop-` prefix, now read for kernel pins only.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `loop gates --docs` passes.
- `ls docs/adr | grep -c '^0013-'` prints `1`.
- `grep -c '^\*\*Ticket:\*\* factorio-6bba$' docs/adr/0013-userspace-skill-pins.md` prints `1`.
- `grep -c 'docs/adr/0007' docs/adr/0013-userspace-skill-pins.md` prints at least `1`.
- `git diff --quiet origin/main -- docs/adr/0007-shared-skill-pack.md` exits `0`.

Epic factorio-yzrt owns these checks, through its spec and tickets:

- A test proves that a project config with a pin named `loop-x` does not load, and that the last
  good config stays in force.
- A test proves that a project config with a pin `neon` does not load when the default branch
  tracks `.claude/skills/neon/SKILL.md`, and that the alert names the pin and the path.
- A test proves that the push scan of a project that pins `neon` refuses a push that adds
  `.claude/skills/neon/SKILL.md`, and passes a push that adds
  `.claude/skills/neon-postgres/SKILL.md`.
- A test proves that a run of a project that pins `neon` holds a skill named `neon` whose bytes
  match the pin's hash, its `name:` line too.
- A test proves that a pin that fails its hash raises one alert for its project, that a run of
  that project and a run of a second project both start, and that the alert closes after the pin
  passes.
- A test proves that a run start makes no fetch.
- A test proves that `loop register` writes the project's pins into its clone, and no other
  project's pins.
