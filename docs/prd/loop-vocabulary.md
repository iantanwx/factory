# Rename the loop's roles, phases and paths to the new vocabulary

## Stories

The operator asked for this epic in the coordinator's chat. The coordinator wrote the operator's
choices into the epic body of factorio-vlle: the units of work, the nine phases, the rename table,
what the rename covers, and the one-release grace for an old stage name. Each story below quotes
the operator's words from the `request` fact. The text under each story states the part of the
epic body that the story covers.

This epic goes first, so that the later epics use the new names. The epic body names the model:
the core loop of compound-engineering, with the steps ideate, brainstorm, plan, work and compound.

In this PRD, a **name** is the word that a runbook, a skill, the glossary, CLI help, an inbox entry,
a stage key or a document path uses for one role, phase, kind or place. A rename changes the name
and nothing that the name stands for.

### US-01 Read one smaller vocabulary for the loop's roles and phases

As the operator, I can read one smaller vocabulary for the loop's roles and phases in every
runbook, skill, CLI help text and inbox entry, so that the loop is easier to understand.

> "i think we need a cleaner vocabulary/ontology. what i have in mind is compound-engineering's
> core loop:"

The epic body states the phases, in this order: ideate (an optional room), brainstorm, doc review,
ADR (optional), plan, doc review, decompose, build, retro.

The epic body states the role renames:

| Today | New name |
| --- | --- |
| intake room | brainstorm room |
| prd-author | unattended brainstorm |
| amender | reviser |
| spec-author | planner |
| fixer | triage |

The epic body says that the unattended brainstorm shares `loop-brainstorm` with the brainstorm
room, so that both write the same PRD. The body cites this as decision D5.

The epic body says that the rename covers runbooks, skill names, stage names in
`.loop/config.yaml`, CLI verbs and help, inbox kinds and the glossary (`loop-sdlc-glossary`). It
also says that a change to the transition tables other than names is out of scope.

### US-02 Count each unit of work as an epic or a bug

As the operator, I can count each unit of work as an epic or a bug, and read a ticket as one slice
of a unit, so that the loop has one answer to what a piece of work is.

> "* a unit of work is either an epic or bug."

The epic body states the units of work. A unit of work is an epic or a bug. A ticket is one
vertical slice of a unit and never a unit by itself. A decision stops being a work kind and becomes
an inbox hold.

### US-03 Read "retro" in place of "AAR"

As the operator, I can read "retro" as the name of the step that learns from finished work, so
that the name says what the step does.

> "i think AAR -> Retro. easier to understand"

The epic body puts retro last in the phases, and it renames AAR to retro. Today no runbook, skill
or Go file of the loop says AAR. Only the retro PRD quotes the request
(`docs/prd/retro.md` § US-01). The retro stage itself belongs to epic factorio-1xq0.

### US-04 Find each spec under `docs/specs/`

As the operator, I can find each spec under `docs/specs/`, so that the path names the document the
planner writes and no other kind of plan.

> "i think we need a cleaner vocabulary/ontology. what i have in mind is compound-engineering's
> core loop:"

The epic body renames the spec path from `docs/plans/<epic id>-<slug>-spec.md` to
`docs/specs/<epic id>-<slug>-spec.md`. Today `docs/plans/` holds twelve specs and one plan that is
not a spec, `2026-10-21-1328-feat-standalone-sdlc-loop-plan.md`.

### US-05 Keep a project config with an old stage name working for one release

As the operator, I can upgrade the loop before I change a project's config, so that a project
that still uses an old stage name keeps its settings for one release and warns me.

> "i think we need a cleaner vocabulary/ontology. what i have in mind is compound-engineering's
> core loop:"

The epic body says that a project config that uses an old stage name keeps working for one
release, with a warning.

## Scope check

**Stories:** 5
**Estimated total:** 3,000 lines
**Lands independently?** Yes, with one pairing. Each role rename of US-01 lands alone, as long as
the old-name grace of US-05 for that stage lands in the same change or before it. US-02, US-03 and
US-04 each land alone. Most of the lines are mechanical text changes across the pack, the CLI
help and the tests.
**Promises provable alone?** Yes. A role rename needs only a search of the pack and of the CLI
help, and one run of the renamed stage. The old-name grace needs one project config fixture. The
spec path needs one spec fixture at each path. The decision as an inbox hold needs one filed
decision and one fire.
**Verdict:** proceed. The decision as an inbox hold (US-02) is the one part that changes a record,
and not only a name. Open questions name it as a possible seam.

## Goal

The operator reads one small vocabulary everywhere the loop speaks: in each runbook, skill, CLI
help text, inbox entry, stage key and document path. The vocabulary follows the steps of the
compound-engineering loop, and the later epics use it. A project that still uses an old stage name
keeps working for one release, and the loop tells the operator what to change.

## Requirements

- Each runbook, skill, CLI help text and inbox entry names a renamed role by its new name:
  brainstorm room, unattended brainstorm, reviser, planner and triage.
  → § US-01
- No runbook, skill, CLI help text or inbox entry names a role by its old name: intake room,
  prd-author, amender, spec-author or fixer. The warning for an old stage name (US-05) is the one
  exception.
  → § US-01
- The runbook skill of each renamed role carries the role's new name, with the `loop-` prefix.
  → § US-01
- The glossary lists the nine phases in the order that the epic body gives, and marks ideate and
  ADR as optional.
  → § US-01
- The glossary defines each renamed role once, by its new name.
  → § US-01
- The unattended brainstorm and the brainstorm room both follow `loop-brainstorm`, and both write a
  PRD from its one template.
  → § US-01
- `.loop/config.yaml` and `loop settings` name each renamed stage by its new name.
  → § US-01
- `loop room open` takes `brainstorm` as the room kind that `intake` names today.
  → § US-01
- An item takes the same moves, in the same order, that it takes today. Only the names change.
  → § US-01
- The glossary defines a unit of work as an epic or a bug, and a ticket as one vertical slice of a
  unit.
  → § US-02
- `loop work list` lists no decision, and the glossary lists no decision among the work kinds.
  → § US-02
- A decision appears in the inbox as a hold, under the inbox kind that names a hold.
  → § US-02
- A filed decision keeps its trigger, and it fires when its trigger happens, as it does today.
  → § US-02
- The glossary names the last phase "retro", and no runbook, skill, CLI help text or inbox entry
  says "AAR".
  → § US-03
- The glossary names the event of an epic that reaches done and the event of a bug that closes,
  because the retro epic waits on these names.
  → docs/prd/retro.md § Boundaries with other epics
- The planner writes each new spec at `docs/specs/<epic id>-<slug>-spec.md`.
  → § US-04
- The `spec` gate reads each spec under `docs/specs/`.
  → § US-04
- `loop-sdlc-documents` names `docs/specs/` as the place of a spec.
  → § US-04
- A spec that an epic already holds under `docs/plans/` when this epic lands still passes the
  `spec` gate, and its epic still decomposes.
  → § US-04
- A project config that names a stage by its old name loads, and the loop applies each value to the
  stage under its new name.
  → § US-05
- The loop warns the operator about each old stage name that a project config holds. The warning
  names the old name and the new name.
  → § US-05
- A stage name that is neither an old name nor a new name still refuses the config, as it does
  today.
  → internal/config/throughput.go

## Acceptance criteria

- **A1.** A search of the pack, the CLI help and the inbox kinds finds the names brainstorm room,
  reviser, planner and triage for the renamed roles.
- **A2.** A search of the pack, the CLI help and the inbox kinds finds no role named intake room,
  prd-author, amender, spec-author or fixer, outside the warning for an old stage name.
- **A3.** The glossary lists ideate, brainstorm, doc review, ADR, plan, doc review, decompose, build
  and retro, in that order, and marks ideate and ADR as optional.
- **A4.** The unattended brainstorm and the brainstorm room write PRDs that one template and one set
  of gates accept. A PRD that one of them writes and the gates refuse is also refused when the
  other writes it.
- **A5.** `loop room open --kind brainstorm` opens the room that `--kind intake` opens today.
- **A6.** After this epic, the transition tables allow each move that they allow today, under its
  new names. They allow no other move.
- **A7.** The glossary defines a unit of work as an epic or a bug, and defines a ticket as one slice
  of a unit.
- **A8.** `loop work list` lists no decision.
- **A9.** A decision whose trigger happens appears in the inbox as a hold.
- **A10.** A decision that waits for its trigger does not fire before the trigger happens.
- **A11.** No runbook, skill, CLI help text or inbox entry says "AAR", and the glossary defines
  "retro".
- **A12.** The glossary names the event of an epic that reaches done and the event of a bug that
  closes.
- **A13.** A spec that the planner writes lands under `docs/specs/`, and the `spec` gate passes it.
- **A14.** A spec that an epic held under `docs/plans/` before this epic lands passes the `spec` gate,
  and its epic decomposes.
- **A15.** A project config that sets `stages.amender.cap` to 2 loads, and the reviser's cap is 2.
- **A16.** That config produces a warning that names `amender` and `reviser`.
- **A17.** A project config that names a stage that the loop never had does not load, and the
  refusal names the key.

## Boundaries with other epics

- **Kernel and userspace skills, factorio-yzrt.** That epic owns the `loop-` prefix rule and the
  split between kernel and userspace skills. Its PRD says that this epic owns every rename of a
  kernel skill. This epic renames kernel skills only, and each new name keeps the `loop-` prefix.
  Its PRD also says that this epic owns the text of the retro's scope rubric. This PRD finds no
  source for that claim. Open questions hold it.
- **Retro, factorio-1xq0.** That epic owns the retro stage, its rubric and its trigger. Its PRD
  waits on this epic for the name "retro", for the event names of an epic that reaches done and a
  bug that closes, and for which bug states count as closed. This epic names the events. Open
  questions hold the closed states.
  → docs/prd/retro.md § Boundaries with other epics
- **The optional ADR stage, factorio-ku99.** That epic owns the ADR phase and its rule. This epic
  names the phase only. Its spec is in sign-off at `docs/plans/factorio-ku99-optional-adr-stage-spec.md`, so A14
  covers it.
- **Doc review with clean readers, factorio-4mb8.** That epic owns the doc review subloop. This
  epic names the doc review phase only.
- **The QA stage, factorio-nhb5, and the simplify stage, factorio-1r94.** Each epic owns its new
  step. This epic adds no step.
- **Project-scoped settings, factorio-j4x.** That epic owns the `stages` and `pools` sections of
  `.loop/config.yaml` and `loop settings`. This epic renames the stage keys in them, and it changes
  no rule of a setting.
  → docs/prd/project-scoped-settings.md § Requirements
- **The unattended PRD author, ADR 0001.** Decision 6 keeps the `prd-author` run stage apart from
  the `intake` cap on epics in flight. This epic renames both. Open questions hold the new key of
  each.
  → docs/adr/0001-unattended-prd-author.md § Decision
- **Checked against:** the PRDs of factorio-j4x, factorio-ku99 and factorio-1xq0, the PRD of
  factorio-yzrt on its item branch, ADRs 0001 to 0013, the glossary, `loop-sdlc-documents`, and
  the open epics factorio-1r94, factorio-4mb8, factorio-6bba, factorio-791i and factorio-nhb5. Two
  conflicts found: the rubric text that factorio-yzrt assigns to this epic, and the two keys of
  ADR 0001 decision 6. Open questions hold both.

## Out of scope

- The doc review subloop. Epic factorio-4mb8 owns it.
- The simplify run. Epic factorio-1r94 owns it.
- The QA stage. Epic factorio-nhb5 owns it.
- The retro stage. Epic factorio-1xq0 owns it.
- A change to the transition tables other than names. Never in this epic, by the epic body.
- The kernel and userspace split. Epic factorio-yzrt owns it.
- The rule for when a decision needs an ADR. Epic factorio-ku99 owns it.

## Open questions

- The unattended brainstorm shares `loop-brainstorm` with the room, by "decision D5". In ADR 0001,
  decision 5 makes `loop prd submit` one act for the room and the run. Does the `prd-author`
  runbook stay as its own skill with a new name, or does `loop-brainstorm` replace it? Blocks A2
  and A4.
- Which stage keys replace `prd-author` and `intake`? ADR 0001 decision 6 keeps the run stage apart
  from the cap on epics in flight. Both cannot take the key `brainstorm`. Blocks A15 for those two
  stages.
- "Hold" already names a room or an escalation that holds an item from every stage
  (`internal/admission/holds.go`). Does an inbox hold of a decision share that word? Blocks A9.
- What does a decision become in the record? Today it is an item with its own states. Does it stay
  an item that `loop work list` hides, or does it leave the item store? The second changes a
  record, and not only a name, and it can be a second epic by `loop-sdlc-split`. Blocks A8.
- Which CLI verbs change? The epic body names CLI verbs. Do `loop decision ...`,
  `loop escalation judge`, `loop escalation route` and `loop prd submit` take new names? Does an old
  verb keep working for one release, as an old stage name does? Blocks A2.
- Does the decision room keep its name, after a decision leaves the work kinds? Blocks A2.
- Do item states that carry an old name, such as `prd_amending` and `spec_amending`, take new
  names? The epic body allows names to change in the transition tables. Blocks A6.
- Do the twelve specs under `docs/plans/` move to `docs/specs/`? ADRs and code comments cite their
  paths today. Does the `spec` gate refuse a new spec under `docs/plans/`? Blocks A13 and A14.
- What happens to an old stage name in the release after the grace? Does the config then refuse
  it? Does the grace also cover the host config and `loop settings set`? Blocks A15 and A16.
- Which bug states count as closed? The retro epic waits on this answer. Blocks A12.
- Does this epic add the ideate room, or only name the phase? No room of that kind exists today.
  Blocks A3.
- The PRD of factorio-yzrt says that this epic owns the text of the retro's scope rubric. Is that
  so, or does factorio-1xq0 own it?
