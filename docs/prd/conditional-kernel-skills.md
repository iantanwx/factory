# Load a kernel skill only when its condition holds, starting with loop-impeccable

## Stories

The operator opened this epic with these words, the epic's request fact:

> "actually, i'm beginning to reconsider that decision. perhaps impeccable should in fact be
> kernel"
>
> "it's our default UI layer."
>
> "ok, let's move it to kernel, conditionally loaded. how does the loop decide that, though?"

The coordinator answered the question with a condition in seven parts, a to g. The operator
accepted all seven parts, and the epic body records each one. The text under each story names the
parts that it holds. The operator split this epic from factorio-yzrt, which holds the userspace
skills.

Today every run of every project holds `loop-impeccable`. `pack/third-party.json` pins it, and the
server writes each pin that passes its hash into each run (`internal/pack/embed.go`). No kernel
runbook names it. `brief.json` lists the userspace skills that a run holds, and no kernel skill
(`internal/brief/brief.go`).

### US-01 A project declares its UI

As the operator, I can declare in a project's config that the project has a user interface, and
where its files are, so that the loop has one fact to decide from.

> "how does the loop decide that, though?"

Part a: a project declares a UI in `.loop/config.yaml` as `ui: {kind: web|native, paths:
[<globs>]}`. With no `ui:` block, no run gets `loop-impeccable`.

### US-02 The design skill reaches only the runs whose work touches the UI

As the operator, I can keep `loop-impeccable` in the kernel and have it reach only the runs whose
work touches the declared UI, so that a project with no UI never loads it.

> "ok, let's move it to kernel, conditionally loaded."

Part b: the pack manifest marks a kernel skill as conditional and names its config key. A second
conditional skill then needs no new code. Part c: the server decides at run start. A run on an
epic gets the skill when the project declares `ui:`. A run on a ticket gets it only when a path in
the ticket's `## Files` section matches a `ui.paths` glob. Part d: `brief.json` lists each loaded
skill. A kernel runbook reads `loop-impeccable` only when the brief lists it, and never tests the
repository itself.

### US-03 The loop proposes the declaration, and a person makes it

As the operator, I can rely on the loop to tell me when a project shows a frontend and declares no
UI, so that no UI project runs without its design skill, and no agent declares a UI for me.

> "how does the loop decide that, though?"

Part e: `loop-init` detects a frontend and proposes the `ui:` block to the operator. A frontend is
react, vue, svelte or next in `package.json`, a `.tsx` or `.vue` file, or an `ios/` or `android/`
directory. Part f: overwatch reports a project with frontend files and no `ui:` block. It never
writes the block.

### US-04 A project names its own design skill in place of the default

As the operator, I can name a userspace design skill for one project, so that the project's runs
read it where they would read `loop-impeccable`.

> "perhaps impeccable should in fact be kernel"
>
> "it's our default UI layer."

Part g: `ui.design_skill: <userspace skill>` replaces `loop-impeccable` in the brief.

## Scope check

US-01 adds the `ui` block to the project config. US-02 adds the conditional mark, the decision at
run start and the skill list of the brief. US-03 adds the proposal of `loop-init` and one overwatch
report. US-04 adds the design skill override.

**Stories:** 4
**Estimated total:** 1,500 lines
**Lands independently?** Yes. US-01 merges alone, because a `ui` block that no reader reads changes no run. US-02 merges after US-01, and US-03 merges after US-01, each alone. US-04 merges after US-02, and after factorio-yzrt, which owns userspace skills.
**Promises provable alone?** Yes. The block's load needs config fixtures only. The conditional load needs one manifest entry, a project with a `ui` block, a project with none, and two ticket bodies. The proposal and the report each need a fixture repository with a frontend and one with none. The override needs one userspace skill fixture.
**Verdict:** proceed. The work reaches the config, the run start, `loop-init` and overwatch. Open questions name the seam.

## Goal

A project states once that it has a user interface, and where it lives. From that fact alone, the
loop's design skill reaches each run whose work touches that interface, and no other run. The loop
tells the operator about a project that looks like a UI project and declares no UI, and the
operator decides.

## Requirements

- A project declares a UI with a `ui` block in `.loop/config.yaml`. The block holds `kind`, which is
  `web` or `native`, and `paths`, a list of globs over the repository.
  → § US-01
- A `ui` block with a `kind` other than `web` or `native` does not load. The last good config stays
  in force, as for any project config that does not load.
  → internal/config/lkg.go
- The `ui` block sits outside the `stages`, `pools` and `merge` sections, so a ticket's pull request
  can add it.
  → docs/prd/project-scoped-settings.md § Requirements
- `loop-impeccable` stays a kernel skill, pinned in the pack.
  → § US-02
- The pack's manifest can mark a kernel skill as conditional, and names the project config key that
  the condition reads. `loop-impeccable` is conditional on the `ui` key.
  → § US-02
- A second conditional kernel skill needs a manifest entry and no change to the server.
  → § US-02
- The server decides at the start of each run which conditional skills the run holds.
  → § US-02
- A run of a project with no `ui` block holds no `loop-impeccable`.
  → § US-01
- A run whose item is an epic holds `loop-impeccable` when its project declares a `ui` block.
  → § US-02
- A run whose item is a ticket holds `loop-impeccable` only when a path in the ticket's `## Files`
  section matches a glob of `ui.paths`.
  → pack/runbooks/decomposer.md § 3. Cut vertical slices
- `brief.json` lists each skill that the run holds, the kernel skills and the conditional skills
  among them. A conditional skill that the run does not hold is not in the list.
  → § US-02
- A kernel runbook that names a conditional skill reads it only when the brief lists it. No kernel
  runbook tells a run to inspect the repository to decide.
  → § US-02
- `loop-init` proposes a `ui` block to the operator when the repository shows a frontend by the
  signals of part e. It writes the block only on the operator's yes.
  → § US-03
- `loop-init` proposes no `ui` block when the repository shows none of those signals.
  → § US-03
- The overwatch sweep reports a project whose default branch shows a frontend by the signals of
  part e and whose config holds no `ui` block.
  → § US-03
- The overwatch sweep never writes a `ui` block, as overwatch writes nothing.
  → docs/adr/0009-overwatch.md
- A `ui` block can name a userspace skill of the project in `design_skill`. Each run that would hold
  `loop-impeccable` then holds no `loop-impeccable`, and its brief names the design skill in its
  place.
  → § US-04
- A `design_skill` whose name starts with `loop-` does not load, because a userspace skill name
  never starts with `loop-`.
  → docs/prd/kernel-and-userspace-skills.md § Requirements

## Acceptance criteria

- **A1.** A project config whose `ui` block holds kind `web` and one glob loads.
- **A2.** A project config whose `ui` block holds kind `desktop` does not load, and the last good
  config stays in force.
- **A3.** A run of a project with no `ui` block holds no `loop-impeccable`.
- **A4.** A run on an epic of a project with a `ui` block holds `loop-impeccable`.
- **A5.** A run on a ticket whose `## Files` section names no path that a `ui.paths` glob matches
  holds no `loop-impeccable`, although its project declares a `ui` block.
- **A6.** A run on a ticket whose `## Files` section names a path that a `ui.paths` glob matches
  holds `loop-impeccable`.
- **A7.** A `ui` block that merges while a run works changes no skill that the run holds.
- **A8.** `brief.json` lists each skill that the run holds.
- **A9.** `brief.json` of a run that does not hold `loop-impeccable` does not list it.
- **A10.** A fixture pack that marks a second skill as conditional on a second config key gives that
  skill to a run whose project sets the key. The server's code is the same as for A4.
- **A11.** That fixture pack gives the second skill to no run whose project lacks the key.
- **A12.** A search of the kernel runbooks finds no step that tells a run to inspect the repository
  to decide whether to read a conditional skill.
- **A13.** `loop-init` in a fixture repository with `react` in `package.json` proposes a `ui` block
  to the operator.
- **A14.** `loop-init` writes no `ui` block when the operator answers no.
- **A15.** `loop-init` in a fixture repository with no frontend signal proposes no `ui` block.
- **A16.** The overwatch sweep reports a project whose default branch holds a `.tsx` file and whose
  config holds no `ui` block.
- **A17.** The overwatch sweep reports nothing about the UI of a project that declares a `ui` block.
- **A18.** After a sweep that reports a project with no `ui` block, the project's config is
  unchanged.
- **A19.** A run of a project whose `ui` block names a userspace design skill holds no
  `loop-impeccable`, in a case where A4 or A6 would give it `loop-impeccable`.
- **A20.** The brief of that run names the userspace design skill.
- **A21.** A project config whose `ui.design_skill` is `loop-x` does not load.

## Boundaries with other epics

- **Kernel and userspace skills, factorio-yzrt.** It owns userspace skills, project pins and the
  `user_skills` list of the brief. It blocks US-04 of this epic. Its PRD keeps `loop-impeccable` in
  every run until this epic lands. After this epic, a run holds `loop-impeccable` only by the rules
  of US-02. Its PRD also puts each userspace pin of a project in each run of that project. This
  epic does not change that rule. US-04 changes only what the brief names as the design skill.
  → docs/prd/kernel-and-userspace-skills.md § Boundaries with other epics
- **ADR 0007 and ADR 0013.** They own how the pack pins and fetches `impeccable`, and the `loop-`
  prefix. This epic changes neither. It changes only which runs hold a pin that passed its check.
  → docs/adr/0007-shared-skill-pack.md § Decision
- **The design stage, factorio-a4yq, `docs/prd/design-stage.md`.** It reads "a project declares a
  UI" as "the project config holds a `ui` block", and it owns the capture command and the canvas
  skill of US-07 in that PRD. This epic owns the `ui` block and its keys `kind`, `paths` and
  `design_skill`. a4yq may add keys to the block. Its open question asks whether its canvas skill
  is this epic's `design_skill` or a second key. This epic does not decide it.
  → docs/prd/design-stage.md § Boundaries with other epics
- **Check shipped screens, `docs/prd/shipped-screen-check.md`.** It adds no config, and reads no
  key of the `ui` block other than a4yq's capture command. No conflict.
- **The QA stage, factorio-nhb5, and the simplify stage, factorio-1r94.** Neither stage exists. A
  run of either stage on a ticket falls under the ticket rule of US-02 when the stage lands. This
  epic adds no stage.
- **The vocabulary epic, factorio-vlle, `docs/prd/loop-vocabulary.md`.** It renames the intake room
  to the brainstorm room and the spec-author to the planner. The epic body uses the new names. This
  PRD uses today's names, and its rule reads the item's kind, so a rename changes no rule.
- **Project-scoped settings, `docs/prd/project-scoped-settings.md`.** It owns the `stages`, `pools`
  and `merge` sections. The `ui` block is outside them.
- **Overwatch, ADR 0009.** It owns the sweep and its parts. This epic adds one report, and the
  sweep still writes nothing.
- **Checked against:** `docs/prd/kernel-and-userspace-skills.md`, `docs/prd/design-stage.md`,
  `docs/prd/shipped-screen-check.md`, `docs/prd/loop-vocabulary.md`,
  `docs/prd/project-scoped-settings.md`, ADR 0007, ADR 0009, ADR 0013, and the open epics
  factorio-1r94 and factorio-nhb5. One conflict found. The yzrt PRD keeps `loop-impeccable` in
  every run, and this epic ends that after yzrt lands. The yzrt PRD states the same order.

## Out of scope

- Userspace skills and project pins. factorio-yzrt owns them.
- The QA stage that uses `loop-impeccable`. factorio-nhb5 owns it.
- The design stage, its canvas skill and its capture command. factorio-a4yq owns them.
- A second conditional kernel skill. This epic builds the mechanism and one user,
  `loop-impeccable`.
- A change to the text of `loop-impeccable`. ADR 0007 keeps fetched text unchanged.
- A `ui` block that an agent writes without the operator's yes. Never, by part f.

## Open questions

- Which rule applies to a run whose item is neither an epic nor a ticket? A reproducer run holds a
  bug, a fixer run holds an escalation, and the coordinator holds no item. The epic body names
  epic runs and ticket runs only. Blocks: A3 to A6.
- Does a path that a ticket's `## Files` section lists as one the ticket must not change count as a
  match? The decomposer runbook puts both kinds of path in that section. Blocks: A5 and A6.
- Which kernel runbooks name `loop-impeccable`? None does today, so part d binds no runbook until
  one names it. The operator called it "our default UI layer". Blocks: A12.
- Does `loop register` write `loop-impeccable` into the clone of a project with no `ui` block? It
  writes every kernel skill into every clone today (`cmd/loop/register.go`). Blocks: A3.
- What does a `ui` block with an empty `paths` list mean? By part c, epic runs then hold the skill
  and no ticket run does. Blocks: A1 and A6.
- How does the operator stop the overwatch report for a project that shows a frontend signal and
  has no UI, such as a repository that keeps a `.tsx` fixture? Part f gives the report no off
  switch. Blocks: A16.
- What happens when `design_skill` names a skill that the project neither pins nor commits? The
  config can refuse to load, or the run can hold no design skill and raise an alert. Blocks: A19
  and A20.
- Does the userspace design skill leave the runs that fail the condition? The yzrt PRD puts each
  userspace pin in each run of its project. Part g changes only the brief. Blocks: A19.
- The split skill names a seam. US-03 touches `loop-init` and overwatch, and US-02 touches the run
  start and the brief. The two halves share only the `ui` block. The operator accepted the seven
  parts as one epic, so this PRD keeps one. Only the operator agrees to a split.
