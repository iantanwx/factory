# Give a PRD epic an optional ADR stage, and one test for when a decision needs an ADR

## Stories

The operator asked for this epic in the coordinator's chat. The coordinator wrote the operator's
choices into the epic body of factorio-ku99: the four parts of the test, the flow, who sends an
epic to `architecture`, and what makes the test enforceable. Each story below quotes the operator's
words from the `request` fact. The text under each story states the part of the epic body that the
story covers.

Today the loop has one kind of ADR. An architecture epic, created with
`loop epic create --architecture`, waits in `architecture` until a merge adds its ADR
(`pack/runbooks/coordinator.md` § 8). The template's rule for when to write one is a sentence: "Use
one when a choice has more than one defensible answer and a later reader will ask why"
(`pack/skills/loop-sdlc-documents/adr.md`). All 12 ADRs in this repository name an epic created
with `--architecture`, and each one exists because the operator asked for it.

### US-01 One test says when a decision needs an ADR

As the operator, I can read one test that decides whether a decision needs an ADR, and where each
other decision goes, so that I know when an ADR gets created.

> "it's still not clear to me when an ADR gest created. we need to tighten that criteria."

The epic body states the test. A decision needs an ADR only when all four parts hold:

1. It binds more than one epic, or the whole project.
2. It has a rejected alternative.
3. It is costly to undo. It changes a data format, a schema or stored state. Or it changes a public
   interface, such as a CLI, an API, a wire format or a file layout that others read. Or it changes
   a dependency, a framework or a vendor, the deployment or process topology, or a trust or
   security boundary. Or it sets a rule that every later epic must follow.
4. It decides how, not what. A PRD requirement can be its source.

Each decision that fails the test goes to one place. A choice inside one epic goes to the spec's
Decisions section. A choice with no alternative goes to the spec's Assumptions section. A product
requirement goes to the PRD. A rule for agents goes to a runbook, a skill or
`.loop/policy/<role>.md`. A fact that a check can enforce goes to a gate. A bug fix goes to the
shaped bug ticket.

When an ADR on the base branch already decides the question, a new ADR with a higher number
supersedes it. Nobody edits the earlier ADR.

The epic body names the pack files that take the test. `loop-sdlc-documents/SKILL.md` replaces the
"use one when" sentence. `adr.md`, `coordinator.md` § 8, `spec-author.md` and `architect-room.md`
take the test. `loop-brainstorm/SKILL.md` names where a decision with no product source goes.

### US-02 A PRD epic can take an ADR stage between its PRD and its spec

As the operator, I can send a PRD epic to an ADR stage after its PRD, so that an epic whose
requirements need an architectural change goes PRD, then ADR, then spec.

> "but an ADR can live alongside a PRD, if implementing it would require a significant
> architectural change, right?"
>
> "yes, so it would be PRD --> (optional) ADR --> spec."

The epic body states the flow. A PRD epic goes `prd_signoff`, then `architecture`, then
`spec_needed` when its signed requirements need an ADR. Otherwise it goes `prd_signoff`, then
`spec_needed`, as it does today. The ADR's `**Ticket:**` line names the PRD epic. When a merge adds
the ADR to the base branch, the epic moves from `architecture` to `spec_needed`. The spec-author
then reads the PRD and each ADR whose Ticket line names the epic. The operator can send an epic to
`architecture` at any time. An epic created with `epic create --architecture` and no PRD keeps
working as it does today.

Two consequences follow from that flow. A PRD epic in `architecture` waits for its ADR as an
architecture epic does today, so the coordinator ends its inbox entry in the same three ways. The
epic stays a PRD epic, so its tickets cite the PRD's story labels, as the tickets of every PRD epic
do.

### US-03 The loop runs the test, and sends an epic to the ADR stage when it passes

As the operator, I can rely on the loop to run the test on each PRD and each spec, so that a
decision that needs an ADR reaches the ADR stage before its tickets exist.

> "we need to tighten that criteria."
>
> "yes, so it would be PRD --> (optional) ADR --> spec."

The epic body names two runs of the test. The doc review of the PRD runs it on the signed
requirements. The spec-author runs it on each entry of the spec's Decisions section. When an entry
passes, the spec-author adds an open question, which holds the spec sign-off. The coordinator then
moves the epic back to `architecture`.

### US-04 Each new ADR shows that it passes the test

As the operator, I can read in each new ADR which epics it binds and why it needs to be an ADR, and
a gate refuses an ADR that does not say, so that the test holds on each ADR and not only in a skill.

> "we need to tighten that criteria."

The epic body adds two parts to the ADR template: a `**Binds:** <epic ids | all>` header line, and
a `## Why an ADR` section that answers the four parts, one line each. The `adr` gate refuses an ADR
without them. The epic leaves the existing ADRs as they are, and a rewrite of them is out of scope.

### US-05 The doc-reviewer reviews each ADR

As the operator, I can rely on the doc-reviewer to review each ADR before I merge it, so that an
ADR whose decision belongs in one spec never binds later work.

> "it's still not clear to me when an ADR gest created. we need to tighten that criteria."

The epic body states that today no stage reviews an ADR (`internal/github/baseaudit.go`, the
comment above `mergeRules`). The reviewer rejects an ADR whose decision binds one epic only.

## Scope check

US-01 is text in the pack. US-02 is the flow of an epic through the ADR stage, with the operator's
command that starts it. US-03 adds the two runs of the test that start the same flow on their own.
US-04 is the template and the `adr` gate. US-05 is a review of an ADR pull request.

**Stories:** 5
**Estimated total:** 2,100 lines
**Lands independently?** Yes. US-01 merges alone, because it changes only pack text. US-02 merges alone, because the operator's send to `architecture` starts the flow with no run of the test. US-03 needs US-01's test and US-02's flow, and it still merges alone after them. US-04 merges alone, because the gate reads only ADR files. US-05 merges alone, because the reviewer can judge what an ADR binds from its text before the Binds line exists.
**Promises provable alone?** Yes. The test text needs only the pack files. The flow needs the transition table, the reactor and an ADR fixture on a base branch. The two runs of the test need a PRD fixture and a spec fixture. The gate needs ADR fixtures. The ADR review needs one ADR pull request fixture.
**Verdict:** proceed.

## Goal

The operator reads one test of four parts that decides whether a decision needs an ADR, and each
other decision has one named place. A PRD epic whose requirements need an architectural change
goes PRD, then ADR, then spec, and every other PRD epic goes PRD, then spec, as it does today. Each
new ADR states which epics it binds and why it is an ADR, and both a gate and the doc-reviewer
refuse one that fails the test.

## Requirements

- The loop's document skill states one test that decides whether a decision needs an ADR. A
  decision needs an ADR only when all four parts of the test hold.
  → § US-01
- The test states its four parts as the epic body names them: the decision binds more than one
  epic or the whole project, it has a rejected alternative, it is costly to undo, and it decides
  how and not what.
  → § US-01
- The test lists each change that makes a decision costly to undo, as US-01 names them.
  → § US-01
- The test names one place for each decision that fails it, as US-01 names them.
  → § US-01
- The test replaces the "Use one when" sentence of the ADR template.
  → § US-01
- The brainstorm skill sends a decision with no product source to the test, and not to an
  unnamed decision record.
  → § US-01
- The coordinator runbook, the spec-author runbook and the architect room runbook each use the
  same test, and none states a second rule for when a decision needs an ADR.
  → § US-01
- When an ADR on the base branch already decides the question, a new ADR with a higher number
  supersedes it, and the earlier ADR stays as it is.
  → § US-01
- The operator can send a PRD epic in `prd_signoff` to `architecture`.
  → § US-02
- The merge of the PRD of an epic that the loop or the operator sent to the ADR stage moves the
  epic to `architecture`, and not to `spec_needed`.
  → § US-02
- The merge of the PRD of every other epic moves the epic to `spec_needed`, as it does today.
  → § US-02
- A merge that adds an ADR whose Ticket line names a PRD epic in `architecture` moves that epic to
  `spec_needed`.
  → § US-02
- The coordinator can end the `architecture` inbox entry of a PRD epic in each of the three ways
  that end the entry of an architecture epic today.
  → § US-02
- The spec-author reads the PRD and each ADR whose Ticket line names the epic, for every epic that
  went through the ADR stage.
  → § US-02
- The tickets of a PRD epic that went through the ADR stage cite the PRD's story labels, as the
  tickets of every PRD epic do.
  → § US-02
- An epic created with `loop epic create --architecture` and no PRD moves through `architecture`
  as it does today.
  → § US-02
- The doc review of a PRD runs the test on each requirement, and its verdict names each
  requirement that passes.
  → § US-03
- A PRD whose doc review names a requirement that passes the test sends its epic to the ADR stage
  at the merge of the PRD.
  → § US-03
- The spec-author runs the test on each entry of the spec's Decisions section.
  → § US-03
- When an entry passes the test, the spec-author adds an open question to the spec that names the
  entry.
  → § US-03
- That open question holds the spec sign-off until the epic leaves the spec stage.
  → § US-03
- The coordinator moves an epic whose spec holds that open question back to `architecture`.
  → § US-03
- The ADR template holds a `**Binds:**` header line that names the epic ids the decision binds, or
  `all`.
  → § US-04
- The ADR template holds a `## Why an ADR` section that answers each part of the test on one line.
  → § US-04
- The `adr` gate refuses a new ADR with no Binds line.
  → § US-04
- The `adr` gate refuses a new ADR with no Why an ADR section, or with a section that does not
  answer each of the four parts.
  → § US-04
- The `adr` gate passes each ADR that a project held before this epic lands, with no edit to that
  ADR.
  → § US-04
- The doc-reviewer reviews each ADR pull request, and submits one verdict on it.
  → § US-05
- The doc-reviewer rejects an ADR whose decision binds one epic only.
  → § US-05

## Acceptance criteria

- **A1.** The loop's document skill holds the test with its four parts, and an ADR needs all four.
  A skill that says an ADR needs fewer than four parts makes this false.
- **A2.** The test names one place for each of the six kinds of decision that fail it: a choice
  inside one epic, a choice with no alternative, a product requirement, a rule for agents, a
  checkable fact and a bug fix.
- **A3.** No pack file states a rule for when a decision needs an ADR other than the test. A grep
  of the pack for the sentence "Use one when a choice has more than one defensible answer" finds
  nothing.
- **A4.** The brainstorm skill names where a decision with no product source goes, by the test.
- **A5.** A new ADR that decides a question that an earlier ADR decided leaves the earlier ADR's
  file unchanged.
- **A6.** The operator can send a PRD epic in `prd_signoff` to the ADR stage. When its PRD merges,
  the epic is in `architecture` and not in `spec_needed`.
- **A7.** A PRD epic that nobody sent to the ADR stage is in `spec_needed` when its PRD merges.
- **A8.** A merge that adds an ADR whose Ticket line names a PRD epic in `architecture` moves that
  epic to `spec_needed`.
- **A9.** A merge that adds an ADR whose Ticket line names a different epic leaves a PRD epic in
  `architecture` where it is.
- **A10.** The coordinator can file the ADR's ticket under a PRD epic in `architecture`, open an
  architect room on it, or defer its inbox entry for an ADR the operator writes by hand.
- **A11.** The spec-author's source for an epic that went through the ADR stage is the PRD and each
  ADR whose Ticket line names the epic. A spec whose Source names only one of the two makes this
  false.
- **A12.** A decomposition of a PRD epic that went through the ADR stage refuses a ticket that
  cites no story label of the PRD, as it does for every PRD epic.
- **A13.** An epic created with `--architecture` and no PRD still moves from `architecture` to
  `spec_needed` when a merge adds its ADR, and its tickets still cite no story label.
- **A14.** A PRD whose requirement passes the test reaches sign-off with a doc review verdict that
  names that requirement.
- **A15.** The merge of a PRD whose doc review named a requirement that passes the test moves the
  epic to `architecture`.
- **A16.** A spec whose Decisions section holds an entry that passes the test holds an open
  question that names the entry.
- **A17.** A spec that holds that open question cannot be signed off.
- **A18.** The coordinator can move an epic whose spec holds that open question back to
  `architecture`.
- **A19.** The `adr` gate refuses a new ADR with no `**Binds:**` line, and the refusal names the
  line.
- **A20.** The `adr` gate refuses a new ADR whose `## Why an ADR` section is missing, or answers
  fewer than four parts, and the refusal names the section.
- **A21.** In any project, an ADR that was on the base branch before this epic's change to the `adr`
  gate passes the gate with no Binds line and no Why an ADR section, and with no edit to the ADR.
  This repository's 12 existing ADRs are one fixture.
- **A22.** Each ADR pull request gets one doc-reviewer verdict at the head the reviewer read.
- **A23.** The doc-reviewer rejects an ADR whose decision binds one epic only, and the reject names
  the epic that the decision binds.

## Boundaries with other epics

- **The vocabulary epic.** It owns each rename of a role or a path. This epic keeps the names
  `architecture`, `architect room`, `prd-author` and `doc-reviewer` as they are today. No item
  holds that epic yet.
- **The doc review subloop.** A later epic owns a doc review that sends subagent readers. This epic
  adds the ADR to the documents that today's doc-reviewer reviews, and it changes no other part of
  that review. No item holds that epic yet.
- **ADR 0012, the base audit of ADR merges.** It decided that no stage reviews an ADR, and that the
  base audit applies no reviewed-head rule to an ADR merge. US-05 changes the first half. See
  Open questions.
- **Project-scoped settings, `docs/prd/project-scoped-settings.md`.** It owns the `stages`, `pools`
  and `merge` settings. This epic adds no setting.
- **Checked against:** `docs/prd/project-scoped-settings.md`, the only other PRD on the base
  branch, and the 12 ADRs in `docs/adr/`. One conflict found, with ADR 0012, named under Open
  questions.

## Out of scope

- Renames of roles and paths. The vocabulary epic owns them.
- The doc review subloop with subagent readers. A later epic owns it.
- A rewrite of the existing ADRs. Never, by the epic body. ADR 0004, which reads as a spec, and ADR
  0011, which restates part of ADR 0006, stay as they are.
- A change to how an epic created with `--architecture` and no PRD moves. It keeps working as it
  does today.

## Open questions

- ADR 0012 § Consequences says "No stage reviews an ADR" and forbids a reviewed-head rule on an
  ADR merge. US-05 adds a review. Does a doc-reviewer reject stop the merge of the ADR, or does the
  operator read it and merge anyway? When a reject stops the merge, the epic's own test applies to
  that change: it binds the whole project, it has an alternative, it changes a rule every later
  epic follows, and it decides how. So it needs a new ADR that supersedes that part of ADR 0012.
  Blocks: A22, A23.
- The epic body says the operator can send an epic to `architecture` "at any time". Read as
  written, that includes an epic in `execution` whose tickets exist. From which states may the
  operator send an epic there? Blocks: A6, A18.
- When the coordinator moves an epic back to `architecture` from the spec stage, its spec pull
  request is open. Does the pull request stay open for the spec-author's next round, or does it
  close? Blocks: A18.
- The doc review of the PRD names a requirement that passes the test. Does the operator see that
  finding at sign-off, and can the operator overrule it so that the epic skips the ADR stage? The
  epic body does not say. Blocks: A14, A15.
- The ADR template's Status line offers "Superseded by ADR NNNN". That value needs an edit to the
  earlier ADR, and the epic body says nobody edits the earlier ADR. ADR 0012 § Consequences also
  says that an edit to an ADR that landed raises an `unreviewed_merge` alert. Does the template
  keep that Status value? Blocks: A5.
- The epic body says `loop-sdlc-documents/SKILL.md` replaces the "use one when" sentence. That
  sentence is in `pack/skills/loop-sdlc-documents/adr.md`, and `SKILL.md` holds no rule for when a
  decision needs an ADR. This PRD reads the body as: the test lives in `SKILL.md`, and `adr.md`
  drops the sentence and points to the test. Blocks: A1, A3.
- The seam, by `pack/skills/loop-sdlc-split/SKILL.md`. The stories reach four parts of the loop:
  pack text (US-01), the transition table and the reactor (US-02, US-03), the `adr` gate (US-04),
  and the doc-reviewer stage (US-05). The scope check finds that each story lands alone and each
  promise is provable alone, so this PRD proposes one epic. A reviewer who reads US-05 as a new
  review stage of its own can name it as the seam. Only the operator agrees to a split. Blocks:
  none.
