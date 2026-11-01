# Add a QA stage to build, with visual QA and fuzzy UAT

## Stories

The operator asked for a QA step in the build phase, as tickertown has: visual QA and fuzzy user
acceptance tests. The coordinator created this epic from that request, and the epic body records
decision D2. The operator chose a kernel stage with a new ADR. The stage runs after code review
and before merge. Each story below quotes a part of the operator's request, the epic's `request`
fact. The request has two parts, "QA --> same as tickertown" and "visual QA, fuzzy user acceptance
tests". The text under each story states what the epic body adds to it.

Today the build phase has two runs for each ticket. The implementer builds the ticket, and the
code-reviewer reviews it (`pack/skills/loop-sdlc-glossary/SKILL.md` § Phases). No stage starts the
app that a ticket changes, and no stage looks at a screen. Tickertown is not in this repository, so
this PRD takes tickertown's QA step only as the epic body describes it.

### US-01 A QA run takes each ticket between code review and merge

As the operator, I can rely on the loop to start a QA run on each ticket after its code review
passes and before it merges, so that no ticket merges before a run uses the change.

> "QA --> same as tickertown"

The epic body states that the QA stage is a kernel stage, and that it runs after code review and
before merge. The stage cap holds the browser, in place of a `needs` lock.

### US-02 Fuzzy UAT drives each change toward its ticket's goal

As the operator, I can rely on the QA run to use each ticket's change toward the ticket's goal, the
way a user would, so that a change that passes its tests and fails its user is caught before merge.

> "fuzzy user acceptance tests"

The epic body states that fuzzy UAT runs on every ticket.

### US-03 Visual QA looks at each ticket that changes the UI

As the operator, I can rely on the QA run to look at the screens of each ticket that changes the
project's declared UI, with the design skill loaded, so that a change that looks wrong is caught
before merge.

> "visual QA"

The epic body states that visual QA runs only on a ticket whose Files section matches the
project's `ui.paths`, with `loop-impeccable` loaded. It names factorio-yzrt for that skill.

### US-04 Each project keeps a verify skill that says how to start and drive its app

As the operator, I can keep one verify skill in each project that says how to start and drive the
project's app, so that a QA run in any project knows how to use that project's change.

> "QA --> same as tickertown"

The epic body states the path, `.claude/skills/verify-<app>/`. It names the sources to fork: pstack
`create-verification-skill` and `maintain-verification-skill`, ce-dogfood, and tickertown's qa
detect, audit and drive.

### US-05 A QA report with its evidence decides the ticket

As the operator, I can read one QA report for each ticket, with the evidence that it cites, so
that I can see what the QA run did and why the ticket passed or failed.

> "QA --> same as tickertown"

The epic body states that the output is a QA report, as a pull request section or a check run,
with its evidence. A screenshot is an evidence file that the report cites, not an attachment in
beads. A fail returns the ticket to repair.

### US-06 A bug that QA finds carries QA as its source

As the operator, I can tell a bug that the QA stage found from a bug that I reported, so that I
know which bugs came from QA.

> "QA --> same as tickertown"

The epic body states that a bug gains `--source qa`. The QA fields that the loop adds name no
product path.

## Scope check

US-01 is the new stage in the flow of a ticket. US-02 and US-03 are the two checks that the run
makes. US-04 is the project skill that both checks use. US-05 is the output and its effect on the
ticket. US-06 is the bug field.

**Stories:** 6
**Estimated total:** 3,200 lines
**Lands independently?** Yes. US-04 merges alone, because a pack skill that forks the verify sources and that no stage starts changes no flow. US-06 merges alone, because a bug source that no stage writes changes no flow. US-01 merges after US-04, with a QA run that writes a report and passes each ticket. US-05 merges after US-01, and adds the fail path. US-02 merges after US-05. US-03 merges after US-02 and after factorio-791i, which owns the `ui.paths` rule. Each story depends on the ones before it, and each one still merges on its own.
**Promises provable alone?** Yes. The stage order needs the transition table and a ticket fixture. The verify skill needs the pack check and a fixture project. The bug source needs the bug record only. The report and the return to repair need a fixture pull request and a stub QA run that passes or fails. Fuzzy UAT needs a fixture app and its verify skill. Visual QA needs a fixture ticket whose Files section matches a `ui.paths` glob, and one that does not.
**Verdict:** proceed.

## Goal

Before a ticket merges, a run starts the project's app and uses the change as a user would, toward
the ticket's goal. When the change touches the screens, the run also looks at them. A person can
read what the run did and see its evidence, and a ticket that fails goes back to be fixed.

## Requirements

- Each ticket goes through a QA run after its code review passes, and before it merges.
  → § US-01
- A bug's shaped ticket goes through the QA run as any other ticket does, because it takes the
  build phase.
  → pack/skills/loop-sdlc-glossary/SKILL.md § Phases
- The QA stage is a kernel stage, with its own name in the brief, and not a step of the
  code-reviewer's runbook.
  → § US-01
- The stage cap of the QA stage limits how many QA runs use a browser at one time. No other lock
  holds the browser.
  → § US-01
- The QA stage's cap is a setting that a project can set, as each other stage's cap is.
  → docs/prd/project-scoped-settings.md § US-01
- Each QA run runs fuzzy UAT. It starts the app and uses the ticket's change toward the ticket's
  goal, the way a user would.
  → § US-02
- A QA run runs visual QA on a ticket when a path in the ticket's Files section matches a glob of
  the project's `ui.paths`.
  → § US-03
- A QA run runs no visual QA on a ticket when no path in its Files section matches a glob of
  `ui.paths`, or when the project declares no `ui` block.
  → § US-03
- A QA run that runs visual QA holds `loop-impeccable`.
  → § US-03
- The QA run reads how to start and drive the app from the project's verify skill at
  `.claude/skills/verify-<app>/`.
  → § US-04
- The pack ships a skill that writes a project's verify skill, and a skill that keeps it current.
  Each one records the upstream it forks and the commit, as the pack's other forked skills do.
  → § US-04
- Each QA run writes one QA report for the ticket's head, as a pull request section or a check
  run.
  → § US-05
- The QA report names the checks it ran, fuzzy UAT and visual QA when it ran, with pass or fail for
  each.
  → § US-05
- The QA report cites each evidence file it relies on. A screenshot is an evidence file that the
  report cites.
  → § US-05
- The record holds no screenshot as an attachment of an item.
  → § US-05
- A QA fail returns the ticket to repair.
  → § US-05
- A ticket whose QA run passes goes on to merge.
  → § US-01
- A bug record can carry the source `qa`.
  → § US-06
- The QA fields that this epic adds to tickets and bugs name no product path of any project.
  → § US-06
- A new ADR records the QA stage, and it supersedes the parts of ADR 0002 and ADR 0005 that the
  epic body names. Nobody edits ADR 0002 or ADR 0005.
  → pack/skills/loop-sdlc-documents/SKILL.md § 6. When a decision needs an ADR

## Acceptance criteria

- **A1.** A QA run takes each ticket whose code review passes, before the ticket merges. A ticket
  that merges with no QA run at its reviewed head makes this false.
- **A2.** No QA run takes a ticket whose code review has not passed.
- **A3.** A QA run takes a bug's shaped ticket after its code review passes.
- **A4.** The brief of a QA run names the QA stage and its runbook.
- **A5.** No more QA runs run at one time than the QA stage's cap allows.
- **A6.** Each QA run reports a fuzzy UAT result for its ticket.
- **A7.** A QA run on a ticket whose Files section names a path that a `ui.paths` glob matches
  reports a visual QA result.
- **A8.** A QA run on a ticket whose Files section names no path that a `ui.paths` glob matches
  reports no visual QA result.
- **A9.** A QA run in a project with no `ui` block reports no visual QA result.
- **A10.** A QA run that runs visual QA holds `loop-impeccable`.
- **A11.** A QA run reads the start and drive steps from the project's verify skill.
- **A12.** Each ticket that a QA run takes gets one QA report for the head the run read.
- **A13.** Each evidence file that a QA report cites exists where the report says it is.
- **A14.** No item in the record holds a screenshot as an attachment.
- **A15.** A ticket whose QA report fails returns to repair, and it does not merge at that head.
- **A16.** A ticket whose QA report passes reaches merge.
- **A17.** A bug that the QA stage files carries the source `qa`.
- **A18.** A bug that the operator reports carries no source `qa`.
- **A19.** The pack's skills that write and keep a verify skill name the upstream and the commit of
  each source that they fork.
- **A20.** ADR 0002 and ADR 0005 are the same before and after this epic.

## Boundaries with other epics

- **The simplify stage, factorio-1r94, `docs/prd/simplify-stage.md`.** That epic owns the simplify
  run. Its epic body states the flow of a ticket: implement, simplify, code review, QA, gates,
  merge. So the QA run reads a head that the simplify run already left. This epic adds no simplify
  step.
  → docs/prd/simplify-stage.md § Boundaries with other epics
- **Conditional kernel skills, factorio-791i, `docs/prd/conditional-kernel-skills.md`.** That epic
  owns the `ui` block and its `paths` key, and the rule that a ticket-level run, QA among them,
  holds `loop-impeccable` only when a path in the ticket's `## Files` section matches a `ui.paths`
  glob. This epic follows that rule for visual QA and adds none of its own.
  → docs/prd/conditional-kernel-skills.md § US-02 The design skill reaches only the runs
- **Kernel and userspace skills, factorio-yzrt, `docs/prd/kernel-and-userspace-skills.md`.** That
  epic owns where a userspace skill lives and how a project declares one. A project's verify skill
  is a skill of that project. This epic adds no other way to hold a skill.
- **The design stage, factorio-a4yq, `docs/prd/design-stage.md`.** That epic owns the signed design
  record and the design stage. Its PRD names this epic as the owner of the QA stage and its visual
  QA. This epic does not read or change the design record.
- **Check shipped screens, `docs/prd/shipped-screen-check.md`.** That epic compares shipped screens
  with the signed design record, inside code review and CI. Its PRD names this epic as the owner of
  visual QA and fuzzy UAT. The two checks both look at screens. This epic's visual QA judges the
  screens with the design skill, and it does not compare them with the record.
- **The vocabulary, factorio-vlle, `docs/prd/loop-vocabulary.md`.** That epic owns the names of
  roles and phases. Its PRD names this epic as the owner of the QA step. This epic adds the QA stage
  to the glossary's stage table and to the build phase.
- **Project-scoped settings, factorio-j4x, `docs/prd/project-scoped-settings.md`.** That epic owns
  the `stages` section of `.loop/config.yaml` and the cap of each stage. The QA stage is one more
  stage that the section can hold, and this epic adds no new kind of setting.
- **Retro, factorio-1xq0, `docs/prd/retro.md`.** Its PRD lists this epic as one that adds a stage.
  A QA run produces records that a retro reads, and it owns no event trigger.
- **The optional ADR stage, factorio-ku99, `docs/prd/optional-adr-stage.md`.** That epic owns the
  ADR stage between PRD sign-off and the spec. The operator chose a new ADR for this epic, so this
  epic takes that stage after its sign-off.
- **ADR 0002 and ADR 0005.** They rejected tickertown's QA states and design rules on ticket
  bodies, a bug's `--source`, the `needs` browser lock and screenshot attachments. This epic's ADR
  supersedes those parts, by the epic body, and nobody edits either ADR.
- **The code-reviewer's rules.** The code-reviewer runbook owns them. This epic changes none.
- **Checked against:** `docs/prd/simplify-stage.md`, `docs/prd/conditional-kernel-skills.md`,
  `docs/prd/kernel-and-userspace-skills.md`, `docs/prd/design-stage.md`,
  `docs/prd/shipped-screen-check.md`, `docs/prd/loop-vocabulary.md`,
  `docs/prd/project-scoped-settings.md`, `docs/prd/retro.md`, `docs/prd/optional-adr-stage.md`,
  ADR 0002, ADR 0005, ADR 0007 and the glossary. Two conflicts found. ADR 0002 and ADR 0005 each
  reject a part that this epic adds, and the epic body settles both with a new ADR. Each sibling
  PRD already names factorio-nhb5 as the owner of the QA stage.

## Out of scope

- The simplify run. factorio-1r94 owns it, by the epic body.
- The comparison of shipped screens with the signed design record. The shipped-screen check epic
  owns it.
- The `ui` block and the rule that loads `loop-impeccable`. factorio-791i owns them.
- A port of tickertown's QA states or design rules on ticket bodies. Never, because ADR 0002
  rejects rules that name tickertown's product paths, and the epic body keeps that reason.
- A `needs` lock on the browser. Never, because the epic body puts the stage cap in its place.
- A verify skill for any one project. Each project writes its own.
- Any change to the code-reviewer's rules. The code-reviewer runbook owns them.

## Open questions

- When does the QA stage file a bug with source `qa`, and when does it only fail the ticket? The
  epic body says a fail returns the ticket to repair, and also that a bug gains `--source qa`. This
  PRD reads a bug as a fault that QA finds outside the ticket's change. Blocks: A15, A17.
- ADR 0005 says only the coordinator files a bug in the loop. A QA bug makes the QA stage a second
  filer. Does the new ADR change who may file, or does the coordinator file each QA bug? Blocks:
  A17.
- Which state does a QA fail return the ticket to? The epic body says "repair". Today a red build
  moves a ticket to `repair`, and the implementer then reads the failed checks. Is a QA report a
  failed check, or does the implementer read the report as a rework reason? Blocks: A15.
- After a repair, does the ticket go through code review again before its next QA run? The epic
  body does not say. Blocks: A1, A2.
- What does a QA run do in a project with no verify skill? It can fail the ticket, pass it with a
  note, or escalate. Blocks: A11, A16.
- How does a QA run pick `<app>` when a project has two apps? The path names one skill per app.
  Blocks: A11.
- Does every project get the QA stage, or does a project turn it on? The request says "same as
  tickertown", and the epic body names no setting. Blocks: A1.
- Is the QA report a pull request section or a check run? The epic body allows either. A check run
  can block the merge queue, and a section cannot. Blocks: A12, A16.
- Where does an evidence file live, so that the report can cite it after the run's checkout is
  deleted? Blocks: A13.
- What does "fuzzy" bound? A fuzzy UAT run can take many paths through an app. The epic body sets
  no time or step limit. Blocks: A6.
- Does a QA fail on visual QA alone, with fuzzy UAT green, return the ticket to repair? This PRD
  reads either fail as a fail of the report. Blocks: A15.
- The split skill names a seam. The verify skills of US-04 change the pack and each project. The
  stage of US-01 and US-05 changes the server's flow. The bug source of US-06 changes the bug
  record. The epic body holds all of them as one epic, so this PRD keeps one. Only the operator
  agrees to a split.
