# Kernel and userspace skills

## Stories

The operator opened this epic with these words, the epic's request fact:

> "loop-neon and loop-neon-postgres are userspace. let's adopt that. we need a way to have
> userspace/kernel division for skills"

Today every skill in the release pack reaches every run of every project. `loop install` fetches
each pin of `pack/third-party.json`, and the server writes each one into each run as
`loop-<name>` (`internal/pack/embed.go`, `verified`). So `neon`, `neon-postgres` and `impeccable`
load in a project such as factorio, which uses neither Neon nor a frontend. No runbook names any of
the three.

The epic body first moved `impeccable` to userspace. The operator then changed that decision, and
the epic's amendment fact records it: "The operator chose 'Kernel, loaded only with a UI'." The
same answer split the conditional load off into epic factorio-791i. So `loop-impeccable` stays a
kernel pin in this epic, and every run holds it as today.

In this PRD, a **kernel skill** is a skill that the release pack ships under the `loop-` prefix and
that a loop runbook can name. A **userspace skill** is a skill that one project declares for its own
stack or domain.

### US-01 Keep the loop's skills apart from a project's skills

As the operator, I can tell a kernel skill from a userspace skill by its name and by where it is
declared, so that each fault goes to the right owner and no project skill poses as a loop skill.

> "we need a way to have userspace/kernel division for skills"

### US-02 The Neon skills leave the kernel

As the operator, I can run a project that uses no Neon database with no Neon skill in its runs, so
that each run reads only the skills that serve its project.

> "loop-neon and loop-neon-postgres are userspace. let's adopt that."

### US-03 A project pins its own skills

As the operator, I can declare a skill for one project, and read which skills each project
declares, so that a project that uses Neon still gets the Neon skills after they leave the kernel.

> "loop-neon and loop-neon-postgres are userspace. let's adopt that."

> "The host fetches a userspace pin at `loop register`, and again when the server reads a changed
> `skills:` list in the project config. A fetch at run start stays rejected."

## Scope check

US-01 sets the naming rule and the fault rule. US-03 builds the project pins and the listing. US-02
removes the two Neon pins from the kernel.

**Stories:** 3
**Estimated total:** 1,500 lines
**Lands independently?** Yes. US-01 merges alone, because the name refusal and the two skill texts need no pin. US-03 merges alone. US-02 merges alone, and it lands after US-03 so that a project that uses Neon can pin both skills before they leave the kernel.
**Promises provable alone?** Yes. The name refusal needs one config fixture and one checkout fixture. A project pin needs a fetched skill fixture and two project fixtures. The Neon removal needs only the pin file.
**Verdict:** proceed. The operator split the conditional load off at the seam that the first review named, into epic factorio-791i.

## Goal

Each project's runs read the loop's own skills and the skills that the project declares, and no
other skill. A project declares its own stack's skills in its repository, and the operator reads
them for each project.

## Requirements

- A kernel skill name starts with `loop-`. A userspace skill name never starts with `loop-`.
  → § US-01
- The server refuses a project config that declares a userspace pin whose name starts with
  `loop-`. The refusal comes when the server loads the config, and the last good config stays in
  force, as for any config that does not load.
  → § US-01
- The server refuses to start a run in a project that commits a skill directory whose name starts
  with `loop-` and that the release pack does not hold. The refusal names the directory.
  → § US-01
- The `loop-fault` skill says that a fault in a userspace skill is a fault of the project, not a
  loop fault.
  → pack/skills/loop-fault/SKILL.md § 1. What counts as a loop fault
- The `loop-init` policy templates say how a project's policy file names a userspace skill that the
  role reads. No kernel runbook names a userspace skill.
  → pack/skills/loop-init/SKILL.md § 6. Policy and ledger
- `pack/third-party.json` holds no pin for `neon` or `neon-postgres`.
  → § US-02
- A run of a project that declares no Neon skill holds no Neon skill.
  → § US-02
- A project declares a userspace pin in `.loop/config.yaml` with a name, a source repository, a
  commit, a path and a sha256, the fields of a kernel pin today.
  → pack/third-party.json
- A project may also commit a userspace skill as a directory under its skill root. That skill
  keeps its name.
  → § US-03
- The host fetches each userspace pin, and checks its hash as it checks a kernel pin today.
  → internal/pack/embed.go
- The host fetches a project's userspace pins at `loop register`, and again when the server reads a
  changed `skills:` list in the project config. No run start fetches a pin.
  → § US-03
- Each run of a project holds each userspace pin of that project that passed its check, under the
  pin's own name.
  → § US-03
- No run of another project holds that pin.
  → § US-03
- `loop register` writes a project's userspace pins into that project's clone only, as it writes
  the kernel skills into every clone.
  → cmd/loop/register.go
- A userspace pin that is missing or fails its check raises one alert for its project, which names
  the pin. Runs of the project start without it, as runs start without a kernel pin that fails
  today.
  → cmd/loop/serve.go
- The alert closes when every userspace pin of the project passes its check.
  → cmd/loop/serve.go
- `loop project show` lists each userspace skill of the project: each pin with its source, commit
  and hash, and each committed directory.
  → § US-03
- `pack/third-party.json` keeps the `impeccable` pin, and every run holds `loop-impeccable` as
  today.
  → pack/third-party.json

## Acceptance criteria

- **A1.** A project config with a userspace pin named `loop-x` does not load. The alert names the
  pin, and the last good config stays in force.
- **A2.** A run of a project that commits `.claude/skills/loop-x/` does not start, when the pack
  holds no `loop-x`. The refusal names the directory.
- **A3.** The `loop-fault` skill says that a fault in a userspace skill is a fault of the project.
- **A4.** Each `loop-init` policy template shows how the policy names a userspace skill.
- **A5.** A run of a project that declares no skills holds no `loop-neon` and no
  `loop-neon-postgres`.
- **A6.** A project that pins `neon` gets a skill named `neon` in each of its runs, and the skill's
  bytes match the pin's hash.
- **A7.** A run of a second project, which pins nothing, holds no `neon`.
- **A8.** A project pin whose bytes do not match its hash reaches no run. One alert for that
  project names the pin.
- **A9.** That alert closes after the pin passes its check.
- **A10.** `loop register` writes a project's pins into its clone, and writes no other project's
  pins.
- **A11.** `loop project show` lists each pin of the project with its source, commit and hash, and
  each committed skill directory.
- **A12.** A changed `skills:` list in a project config leads the host to fetch the new pin, and
  no run start fetches a pin.

## Boundaries with other epics

- **ADR 0007 and epic factorio-cd4.** ADR 0007 put the four third-party skills in every run under
  the `loop-` prefix. Its decision 2 names every injected skill with the prefix, and it tells a
  later reader never to inject a skill outside it. Its decision 7 fetches each pin at `loop install`
  and `loop upgrade`, and the server never fetches one. A userspace pin keeps its own name, and the
  install does not know a project's pins. This epic changes both rules. The operator chose an ADR
  before the spec. Ticket factorio-g70s of architecture epic factorio-6bba writes the ADR that
  supersedes decisions 2 and 7. The spec of this epic cites that ADR once it lands.
  → docs/adr/0007-shared-skill-pack.md § Decision
- **Conditional kernel skills, factorio-791i.** That epic loads a kernel skill only when its
  condition holds, starting with `loop-impeccable`. It holds the condition that the first review
  found with no source. This epic blocks it. Until it lands, `loop-impeccable` reaches every
  run.
- **Project-scoped settings.** That epic refuses a stage run's push that changes the `stages`,
  `pools` or `merge` section of `.loop/config.yaml`. The new `skills` list is outside those
  sections. So a ticket's pull request can add a pin, and the
  code-reviewer and the merge read it like any other change. This epic does not change that rule.
  → docs/prd/project-scoped-settings.md § Requirements
- **The vocabulary epic, factorio-vlle.** It owns every rename of a kernel skill. This epic renames
  none. The retro's scope rubric sends a finding about the loop to the kernel and a finding about
  the project's stack to userspace. The vocabulary epic owns the rubric's text.
- **The QA stage, factorio-nhb5, and the simplify stage, factorio-1r94.** Neither stage exists.
  This epic adds no stage.
- **Doc review with clean readers, factorio-4mb8.** That epic uses `asd-ste100`, which stays a
  kernel pin. This epic does not move it.
- **The optional ADR stage, factorio-ku99.** That epic decides when a PRD epic takes an ADR. It is
  in spec drafting. The operator chose the ADR for this epic directly, so this epic does not lean on
  its rule.
- **Checked against:** ADR 0007, the factorio-cd4 spec, the project-scoped settings PRD, the
  optional ADR stage PRD, and the open epics factorio-1r94, factorio-4mb8, factorio-6bba,
  factorio-791i, factorio-ku99, factorio-nhb5 and factorio-vlle. One conflict found, with ADR 0007,
  and epic factorio-6bba settles it.

## Out of scope

- A rename of any kernel skill. The vocabulary epic, factorio-vlle, owns it.
- A registry or a catalogue of userspace skills that several projects share. Never, by the epic
  body.
- A rewrite of the text of a third-party skill. ADR 0007 keeps fetched text unchanged.
- A licence review of a userspace pin's source. The project that pins a skill owns that choice.
- A conditional load of any kernel skill, `loop-impeccable` among them. Epic factorio-791i owns it.
- A new stage. The QA and simplify epics own their stages.

## Open questions

- What happens when a userspace pin and a committed skill directory of one project share a name?
  ADR 0007 decision 2 notes that today a tracked directory at an injected path refuses every run of
  its project with `CONFIG_CONFLICT`. Blocks: A6 and A2.
