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

The epic body first moved `impeccable` to userspace. The operator then changed that decision. The
coordinator recorded the operator's words in the epic's note fact, not in the request fact or an
amendment fact: "ok, let us move it to kernel, conditionally loaded." US-04 quotes those words. The
operator confirms them at sign-off, by the first open question.

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

### US-04 The design skill loads only where it serves

As the operator, I can keep `loop-impeccable` in the kernel and have it reach a run only when the
run's work touches a user interface, so that a project with no frontend never loads it.

> "ok, let us move it to kernel, conditionally loaded."

## Scope check

US-01 sets the naming rule and the fault rule. US-03 builds the project pins and the listing. US-02
removes the two Neon pins from the kernel. US-04 builds the conditional load of a kernel skill and
its first user, `loop-impeccable`.

**Stories:** 4
**Estimated total:** 2,300 lines
**Lands independently?** Yes. US-01 merges alone, because the name refusal and the two skill texts need no pin. US-03 merges alone. US-02 merges alone, and it lands after US-03 so that a project that uses Neon can pin both skills before they leave the kernel. US-04 merges alone, because it reads only the pack manifest, the project's `ui` block and the brief.
**Promises provable alone?** Yes. The name refusal needs one config fixture and one checkout fixture. A project pin needs a fetched skill fixture and two project fixtures. The Neon removal needs only the pin file. The conditional load needs a manifest entry, one project with a `ui` block, one without, and one ticket body.
**Verdict:** proceed. The split skill names a seam, under Open questions. US-04 touches the brief and the scheduler, and US-01 to US-03 touch config, install and the pack. The two halves share no promise.

## Goal

Each project's runs read the loop's own skills and the skills that the project declares, and no
other skill. A project declares its own stack's skills in its repository, and the operator reads
them for each project. A kernel skill that serves only some work, such as the design skill, reaches
only the runs whose work needs it.

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
- `loop-impeccable` stays in the kernel.
  → § US-04
- The pack manifest can mark a kernel skill as conditional, and names the project config key that
  the condition reads. A second conditional skill needs a manifest entry and no new code.
  → § US-04
- `loop-impeccable` is conditional on a `ui` block in `.loop/config.yaml`. The block names the
  kind of interface, web or native, and the repository paths that hold it as globs.
  → § US-04
- A project with no `ui` block gets `loop-impeccable` in no run.
  → § US-04
- A run whose item is an epic gets `loop-impeccable` when its project declares a `ui` block.
  → § US-04
- A run whose item is a ticket gets `loop-impeccable` only when a path in the ticket's `## Files`
  section matches a glob of the `ui` block.
  → pack/runbooks/decomposer.md
- `brief.json` lists each skill that the run holds, the conditional skills among them.
  → § US-04
- A kernel runbook that can use a conditional skill reads it only when the brief lists it. The
  runbook never inspects the repository to decide.
  → § US-04
- `loop-init` proposes a `ui` block to the operator when the repository shows a frontend, and
  writes it only on the operator's yes.
  → § US-04
- The overwatch sweep reports a project whose repository shows a frontend and whose config holds no
  `ui` block. The sweep never writes the block.
  → internal/server/overwatch.go
- A project whose `ui` block names a userspace design skill gets that skill in place of
  `loop-impeccable`, in each run that would get `loop-impeccable`.
  → § US-04

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
- **A12.** A run of a project with no `ui` block holds no `loop-impeccable`.
- **A13.** A run on an epic of a project with a `ui` block holds `loop-impeccable`.
- **A14.** A run on a ticket whose `## Files` section names no path that a `ui` glob matches holds
  no `loop-impeccable`, although its project declares a `ui` block.
- **A15.** A run on a ticket whose `## Files` section names a path that a `ui` glob matches holds
  `loop-impeccable`.
- **A16.** `brief.json` lists each skill the run holds. A conditional skill that the run does not
  hold is not in the list.
- **A17.** No kernel runbook tells a run to inspect the repository to decide whether to read a
  conditional skill.
- **A18.** A project whose `ui` block names a userspace design skill gets that skill and not
  `loop-impeccable`, in a run that would otherwise get `loop-impeccable`.
- **A19.** The overwatch sweep reports a project that shows a frontend and declares no `ui` block,
  and it leaves the project's config unchanged.

## Boundaries with other epics

- **ADR 0007 and epic factorio-cd4.** ADR 0007 put the four third-party skills in every run under
  the `loop-` prefix. Its decision 2 names every injected skill with the prefix, and it tells a
  later reader never to inject a skill outside it. Its decision 7 fetches each pin at `loop install`
  and `loop upgrade`, and the server never fetches one. A userspace pin keeps its own name, and the
  install does not know a project's pins. This epic changes both rules. Only a later ADR amends an
  ADR, so the second open question asks whether this epic needs one.
  → docs/adr/0007-shared-skill-pack.md § Decision
- **Project-scoped settings.** That epic refuses a stage run's push that changes the `stages`,
  `pools` or `merge` section of `.loop/config.yaml`. The new `skills` list and the `ui` block are
  outside those sections. So a ticket's pull request can add a pin or a `ui` block, and the
  code-reviewer and the merge read it like any other change. This epic does not change that rule.
  → docs/prd/project-scoped-settings.md § Requirements
- **The vocabulary epic, factorio-vlle.** It owns every rename of a kernel skill. This epic renames
  none. The retro's scope rubric sends a finding about the loop to the kernel and a finding about
  the project's stack to userspace. The vocabulary epic owns the rubric's text.
- **The QA stage, factorio-nhb5, and the simplify stage, factorio-1r94.** Neither stage exists. A
  run of either stage on a ticket falls under the ticket rule of US-04 when the stage lands. This
  epic adds no stage.
- **Doc review with clean readers, factorio-4mb8.** That epic uses `asd-ste100`, which stays a
  kernel pin. This epic does not move it.
- **The optional ADR stage, factorio-ku99.** That epic decides when a PRD epic takes an ADR. It is
  in spec drafting, so the second open question cannot lean on its rule yet.
- **Checked against:** ADR 0007, the factorio-cd4 spec, the project-scoped settings PRD, the
  optional ADR stage PRD, and the open epics factorio-1r94, factorio-4mb8, factorio-ku99,
  factorio-nhb5 and factorio-vlle. One conflict found, with ADR 0007, and it is open.

## Out of scope

- A rename of any kernel skill. The vocabulary epic, factorio-vlle, owns it.
- A registry or a catalogue of userspace skills that several projects share. Never, by the epic
  body.
- A rewrite of the text of a third-party skill. ADR 0007 keeps fetched text unchanged.
- A licence review of a userspace pin's source. The project that pins a skill owns that choice.
- A second conditional kernel skill. This epic builds the mechanism and one user,
  `loop-impeccable`.
- A new stage. The QA and simplify epics own their stages.

## Open questions

- The words "ok, let us move it to kernel, conditionally loaded" come from the epic's note fact,
  not from the request fact or an amendment fact. The operator confirms them at sign-off. The
  condition itself, the `ui` block, the epic and ticket rules, the `loop-init` proposal, the
  overwatch report and the design skill override, came from the coordinator's proposal in the same
  note, and the operator's words do not fix them. The operator confirms or changes each one at
  sign-off. Blocks: A12 to A19.
- Does this epic need an ADR that amends ADR 0007 decisions 2 and 7, before its spec? The PRD
  cannot amend an ADR, and both decisions bind the injection that US-03 changes. Blocks: A6 and A8.
- When does the host fetch a userspace pin? ADR 0007 rejected a fetch at each run start, because a
  fetch failure would count against the item. `loop install` does not read project config. A fetch
  when the server loads a changed project config fits both facts, and it needs the network in the
  server. Blocks: A6, A8 and A9.
- What happens when a userspace pin and a committed skill directory of one project share a name?
  ADR 0007 decision 2 notes that today a tracked directory at an injected path refuses every run of
  its project with `CONFIG_CONFLICT`. Blocks: A6 and A2.
- Which runs follow the epic rule, and which follow the ticket rule? The proposal named a planner,
  a simplify run and a QA run, and no such stage exists today. A coordinator run and a decision room
  hold neither an epic nor a ticket. A bug holds no `## Files` section until the reproducer shapes
  it. Blocks: A13 to A15.
- How do `loop-init` and the overwatch sweep tell that a repository shows a frontend? The
  proposal named a react, vue, svelte or next dependency in `package.json`, `.tsx` or `.vue` files,
  and `ios/` or `android/` directories. Blocks: A19.
- The split seam. US-04 changes the brief and the run start, and US-01 to US-03 change config,
  install and the pack. Each half lands and proves alone, so this PRD proceeds as one epic. The
  operator agrees to keep it whole or to split it in an intake room. Blocks: none.
