# Simplify each ticket's code in its own run before code review

## Stories

The operator asked for a code simplification step in the build phase, as tickertown has. The
coordinator created this epic from that request, and the epic body records decision D3. The
operator chose a run of its own, before review. The flow of one ticket is: implement, simplify,
code review, QA, gates, merge. Each story below quotes the operator's request. The text under each
story states what the epic body adds to it.

Today the build phase has two runs for each ticket. The implementer builds the ticket, and the
code-reviewer reviews it (`pack/skills/loop-sdlc-glossary/SKILL.md` § Phases). Tickertown is not in
this repository, so this PRD takes tickertown's step only as the epic body describes it.

### US-01 A simplify run takes each ticket between the implementer and the code-reviewer

As the operator, I can rely on the loop to start a simplify run on each ticket after its
implementer finishes and before its code-reviewer starts, so that the code-reviewer reads code that
a second run already made simpler.

> "code unslop/simplification --> same as tickertown"

The epic body states the order: implement, simplify, code review, QA, gates, merge. The run works
on the ticket's pull request branch. It starts with a clean context, and it does not share the
implementer's context. The operator chose a run of its own, and not a step inside the
implementer's run or inside the code-reviewer's run.

### US-02 The simplify run keeps behaviour and leaves the gates green

As the operator, I can trust that the simplify run changes how the code reads and never what the
code does, so that a simpler head is as safe to review as the implementer's head.

> "code unslop/simplification --> same as tickertown"

The epic body states three rules. The run keeps behaviour. The project gates must pass at its head.
It pushes one commit, or no commit when nothing gets simpler.

### US-03 The simplify run follows the loop-simplify runbook

As the operator, I can read one runbook, `loop-simplify`, that states what the simplify run does,
so that every project's tickets get the same simplification.

> "code unslop/simplification --> same as tickertown"

The epic body names three sources for the runbook: ce-simplify-code, pstack `no-comments` and
pstack `comment-sicko`. The runbook forks them and fits them to the loop's SDLC.

### US-04 The code-reviewer reviews the final head once

As the operator, I can rely on the code-reviewer to review the head that the simplify run leaves,
and to review it once, so that no review round is spent on a head that the simplify run replaces.

> "code unslop/simplification --> same as tickertown"

The epic body states that the code-reviewer reviews the final head once. The code-reviewer's rules
stay as they are.

## Scope check

US-01 is the new stage in the flow of a ticket. US-02 is the rules that bind the run's output.
US-03 is the runbook. US-04 is the hand-off to the code-reviewer.

**Stories:** 4
**Estimated total:** 1,400 lines
**Lands independently?** Yes. US-03 merges alone, because a pack runbook that no stage starts changes no flow. US-01 needs the runbook to name in the brief, so it merges after US-03. US-02 merges with US-01 or after it, because its checks read the output of a simplify run. US-04 needs US-01, because the code-reviewer can only wait for a stage that exists. Each story depends on the one before it, and each one still merges on its own.
**Promises provable alone?** Yes. The runbook needs the pack check only. The stage order needs the transition table and a ticket fixture. The rules on the run's output need a fixture pull request and a stub gate that passes or fails. The hand-off needs a ticket fixture that leaves the simplify stage, and a check that one code review starts at that head.
**Verdict:** proceed.

## Goal

A second run makes each ticket's code simpler before anyone reviews it. That run starts from a
clean context, keeps what the code does, and leaves the project gates green. The code-reviewer
then reads the simpler code once.

## Requirements

- Each ticket goes through a simplify run after its implementer opens the pull request, and before
  a code-reviewer run takes it.
  → § US-01
- A bug's shaped ticket goes through the simplify run as any other ticket does, because it takes
  the build phase.
  → pack/skills/loop-sdlc-glossary/SKILL.md § Phases
- The simplify run works on the ticket's pull request branch.
  → § US-01
- The simplify run starts with a clean context. Its brief holds none of the implementer's
  transcript.
  → § US-01
- The simplify stage is a stage of its own, with its own name in the brief, and not a step of the
  implementer's runbook or of the code-reviewer's runbook.
  → § US-01
- The simplify run keeps the behaviour of the code. Each test that passes at the implementer's head
  passes at the simplify run's head.
  → § US-02
- The project gates pass at the head that the simplify run leaves.
  → § US-02
- The simplify run pushes at most one commit.
  → § US-02
- The simplify run pushes no commit when it finds nothing to make simpler, and the ticket then
  goes on to code review at the implementer's head.
  → § US-02
- The simplify run follows a pack runbook named `loop-simplify`.
  → § US-03
- The `loop-simplify` runbook records, for each of its three sources, the upstream it forks
  and the commit, as the pack's other forked skills do.
  → pack/skills/loop-unslop/SKILL.md
- The `loop-simplify` runbook ships in the release pack with the other `loop-*` skills.
  → docs/adr/0007-shared-skill-pack.md § Decision
- The code-reviewer starts on a ticket only after its simplify run finishes.
  → § US-04
- The code-reviewer reviews the head that the simplify run leaves.
  → § US-04
- The simplify stage leaves the code-reviewer's runbook and review rules as they are.
  → § US-04

## Acceptance criteria

- **A1.** A simplify run takes each ticket whose implementer opens its pull request, before any
  code-reviewer run takes it. A code review that starts first makes this false.
- **A2.** A simplify run takes a bug's shaped ticket before its code review.
- **A3.** The brief of a simplify run names the simplify stage and the `loop-simplify` runbook.
- **A4.** The brief of a simplify run holds no part of the implementer's run transcript.
- **A5.** A simplify run that finds nothing to make simpler pushes no commit, and the ticket then
  reaches code review at the implementer's head.
- **A6.** A simplify run pushes at most one commit. A second commit of the run on the branch makes
  this false.
- **A7.** A simplify head at which a project gate fails never reaches code review.
- **A8.** Each test that passes at the implementer's head passes at the simplify run's head.
- **A9.** The code-reviewer's verdict names the head that the simplify run left.
- **A10.** A ticket reaches one code review for the head that the simplify run left. A second
  review of the same head makes this false.
- **A11.** The `loop-simplify` runbook names the upstream and the commit of ce-simplify-code,
  pstack `no-comments` and pstack `comment-sicko`.
- **A12.** The code-reviewer's runbook is the same before and after this epic.

## Boundaries with other epics

- **The QA stage, factorio-nhb5.** That epic owns the QA stage. The epic body puts QA after code
  review, so QA reads a head that the simplify run already left. This epic adds no QA step.
- **Conditional kernel skills, factorio-791i, `docs/prd/conditional-kernel-skills.md`.** That
  epic names the simplify run as a ticket-level run, which gets `loop-impeccable` only when a path
  in the ticket's `## Files` section matches a `ui.paths` glob. This epic follows that rule and
  adds none of its own.
  → docs/prd/conditional-kernel-skills.md § Stories
- **The vocabulary, factorio-vlle, `docs/prd/loop-vocabulary.md`.** That epic owns the names of
  roles and phases, and it names the simplify run as this epic's. This epic adds the simplify stage
  to the glossary's stage table and to the build phase, and uses the vocabulary epic's names.
- **Project-scoped settings, factorio-j4x, `docs/prd/project-scoped-settings.md`.** That epic owns
  the `stages` section of `.loop/config.yaml` and the cap of each stage. The simplify stage is one
  more stage that the section can hold, and this epic adds no new kind of setting.
- **Retro, factorio-1xq0, `docs/prd/retro.md`.** Its PRD lists this epic as one that adds a
  stage. A simplify run produces records that a retro reads, and it owns no event trigger.
- **The code-reviewer's rules.** The code-reviewer runbook owns them, and this epic changes none,
  by the epic body.
- **`loop-unslop` and `loop-least-code`, skills in the pack.** ADR 0007 owns how the pack holds
  them. They cover prose and the size of a change. This epic may cite them from `loop-simplify`,
  and changes neither.
- **Checked against:** `docs/prd/conditional-kernel-skills.md`, `docs/prd/loop-vocabulary.md`,
  `docs/prd/kernel-and-userspace-skills.md`, `docs/prd/project-scoped-settings.md`,
  `docs/prd/retro.md`, `docs/prd/design-stage.md`, `docs/prd/shipped-screen-check.md`, ADR 0002,
  ADR 0007 and the glossary. No conflict found. Each sibling PRD already names factorio-1r94 as
  the owner of the simplify stage.

## Out of scope

- The QA stage. factorio-nhb5 owns it, by the epic body.
- Any change to the code-reviewer's rules. Never in this epic, by the epic body.
- A simplify step for documents, PRDs, specs or ADRs. The epic body places the run in the flow of a
  ticket only.
- A port of tickertown's own simplify files. ADR 0002 rejects a port of rules that name
  tickertown's product, and this epic forks from the three upstream sources that the epic body
  names.

## Open questions

- A review reject sends a ticket back to `todo`, and the implementer pushes a new head. Does a
  simplify run take the ticket again before the next code review? The epic body says the
  code-reviewer reviews the final head once, and it does not name a second round. Blocks: A1, A10.
- A red build sends a ticket to `repair`, and the implementer pushes a fix. Does the fix go through
  a simplify run before the ticket returns to review? Blocks: A1, A10.
- What happens to the ticket when the simplify run fails, or cannot find a simpler head at which
  the gates pass? This PRD reads that case as no commit, so the ticket reaches code review at the
  implementer's head. A failed run that counts as an attempt is the other reading. Blocks: A5, A7.
- Does every project get the simplify stage, or does a project turn it on? The request says "same
  as tickertown", and the epic body names no setting. Blocks: A1.
- May the simplify run change a file that the ticket's pull request does not already change? The
  epic body does not say. This PRD sets no limit. Blocks: A8.
- By their names, pstack `no-comments` and `comment-sicko` act on code comments. A project's own
  rules can ask an agent to keep a comment. Which rule wins in `loop-simplify` when the two
  disagree? Blocks: A8, A11.
