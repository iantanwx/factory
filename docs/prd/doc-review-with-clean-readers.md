# Review a PRD, spec or ADR with three clean-context readers and one judge

## Stories

The operator asked for a doc review that runs a meta skill with two subagents. One subagent runs
unslop, and the other runs ASD-STE100. The coordinator created this epic from that request, and
the epic body adds three things. It adds a third reader, the content lenses. It records decision
D1, where the operator chose "readers, one judge". It puts the reviser in charge of every edit to
the document. Each story below quotes the operator's request. The text under each story states
what the epic body adds to it.

Today the doc-reviewer reads a PRD, a spec or an ADR alone, and starts no subagent that reviews
(`pack/skills/loop-sdlc-review/SKILL.md` § 9). The epic body says that today no stage reviews an
ADR. The doc-reviewer runbook now holds a section for an ADR pull request
(`pack/runbooks/doc-reviewer.md` § 4), which the optional ADR stage, factorio-ku99, added. So this
PRD reads the ADR part of the scope as a change to that review.

### US-01 The doc-reviewer starts each reader in a clean context

As the operator, I can rely on the doc-reviewer to start each reader as a subagent with a clean
context, so that no reader's findings lean on another reader or on the reviewer.

> "doc review --> runs a meta skill that combines two subagents, each independently running"

The epic body names three readers, where the request names two. The doc-reviewer run starts all
three. A meta skill states how the run starts the readers and how it combines their findings.

### US-02 An unslop reader and an ASD-STE100 reader read the document

As the operator, I can rely on one reader that applies unslop and one reader that applies
ASD-STE100 to each document, so that the document reads without AI tells and without a sentence
that an agent can misparse.

> "each independently running https://github.com/cursor/plugins/blob/main/pstack/skills/unslop/SKILL.md, https://github.com/danyuchn/asd-ste100-skill"

The epic body names the skills that the pack already holds. The unslop reader follows
`loop-unslop`. The ASD-STE100 reader follows `loop-asd-ste100`, re-pinned to upstream commit
255631a.

### US-03 A content-lens reader reads the document

As the operator, I can rely on a third reader that reads what the document says, so that a fault
of content reaches the reviewer as well as a fault of prose.

> "doc review --> runs a meta skill that combines two subagents"

The epic body adds this reader. Its lenses fork the ce-doc-review personas and pstack
`design-red-flags.md`.

### US-04 Each reader returns findings, and one reviewer judges

As the operator, I can rely on the doc-reviewer to read the whole document, merge and rank the
readers' findings, and write one verdict, so that one reader who read all of it still decides.

> "the result is synthesized"

The epic body states what a finding holds: where it is, which rule it breaks, and the edit it
proposes. Decision D1 changes one rule of `loop-sdlc-review`. Subagents may find, and only the
reviewer judges. The reviewer still reads all of the document, and writes one verdict at the head
it read.

### US-05 The reviser applies one edit set in one commit

As the operator, I can rely on one edit set from the review to land in the document in one commit,
so that the findings reach the final document and only one run edits it.

> "the result is synthesized into the final document"

The epic body states that on a reject the reviser applies one edit set in one commit, and that only
the reviser edits. The body calls the reviser "the amender" today. The glossary already names the
stage `reviser` (`pack/skills/loop-sdlc-glossary/SKILL.md` § Stages).

### US-06 The subloop runs on PRDs, specs and ADRs

As the operator, I can rely on the same readers and the same judge for each PRD, each spec and each
ADR, so that every document an epic carries before its tickets gets the same review.

> "doc review"

The epic body names the three documents. It places the subloop after the brainstorm and after the
plan, which are the two doc review phases of the glossary, and on each ADR.

## Scope check

US-01 is the start of the readers. US-02 and US-03 are the three readers. US-04 is the judge and
the rule change of D1. US-05 is the edit. US-06 is the scope of documents.

**Stories:** 6
**Estimated total:** 1,300 lines
**Lands independently?** Yes. US-02 merges alone, because the re-pin and the two existing reader skills change no run. US-03 merges alone, because a pack skill that no run starts changes no review. US-01 and US-04 merge together. A run that starts readers needs the rule that lets it. The rule change of D1 with no readers changes no verdict. That pair is one unit, and it still merges with no change to the reviser. US-05 merges after them. US-06 widens the pair from the PRD review to the spec and ADR reviews, and merges after it.
**Promises provable alone?** Yes. The re-pin needs the pack check only. Each reader skill needs a fixture document with a known fault and the finding it must return. The start of the readers and the judge need a doc-reviewer run on a fixture pull request, with stub readers that return fixed findings. The edit set needs a fixture reject and the reviser's commit. The scope needs one fixture of each document kind.
**Verdict:** proceed.

## Goal

Three readers with clean contexts read each PRD, spec and ADR for prose, for parse and for content.
One reviewer reads the whole document, weighs what the readers found, and writes one verdict. The
edits that the review asks for then land in the document in one commit by one run.

## Requirements

- The doc-reviewer run starts each reader as a subagent with a clean context. No reader reads the
  reviewer's context or another reader's findings.
  → § US-01
- The doc-reviewer run starts three readers on each document it reviews: an unslop reader, an
  ASD-STE100 reader and a content-lens reader.
  → § US-01
- A meta skill in the release pack states how the doc-reviewer starts the readers and how it
  combines their findings.
  → § US-01
- Each reader writes no file in the checkout, and decides no question. It returns each open
  question to the reviewer.
  → pack/skills/loop-sdlc-turn/SKILL.md § 8. Never
- The unslop reader reads the document by `loop-unslop`.
  → § US-02
- The ASD-STE100 reader reads the document by `loop-asd-ste100`.
  → § US-02
- The kernel pin of `asd-ste100` names upstream commit 255631a of `danyuchn/asd-ste100-skill`.
  → § US-02
- The content-lens reader reads the document by a pack skill whose lenses fork the ce-doc-review
  personas and pstack `design-red-flags.md`.
  → § US-03
- The content-lens skill records, for each source, the upstream it forks and the commit, as the
  pack's other forked skills do.
  → pack/skills/loop-unslop/SKILL.md
- Each finding of a reader names where it is, which rule it breaks, and the edit it proposes.
  → § US-04
- The reviewer reads the whole document itself, whatever the readers return.
  → § US-04
- The reviewer merges the readers' findings, ranks them, and writes one verdict at the head it
  read.
  → § US-04
- A reader's finding enters the verdict only when the reviewer judges it. A reader's finding
  alone rejects nothing.
  → § US-04
- `loop-sdlc-review` lets a subagent find, and lets only the reviewer judge. Its rule against a
  subagent that reviews changes to say so.
  → § US-04
- A later round judges the named findings and the diff since the last reviewed head. A reader's
  finding outside the diff stays out of that round's verdict.
  → pack/skills/loop-sdlc-review/SKILL.md § 4. Report every fault in one round
- On a reject, the reviser applies the review's edit set to the document in one commit.
  → § US-05
- Only the reviser edits the document. The reviewer and the readers edit nothing.
  → § US-05
- The subloop runs on each PRD review, each spec review and each ADR review.
  → § US-06

## Acceptance criteria

- **A1.** A doc review of a PRD starts three readers: unslop, ASD-STE100 and content lens. A review
  with fewer readers makes this false.
- **A2.** A doc review of a spec starts the same three readers.
- **A3.** A doc review of an ADR pull request starts the same three readers.
- **A4.** No reader's context holds the reviewer's context or another reader's findings.
- **A5.** Each finding that a reader returns names a place in the document, a rule and a proposed
  edit. A finding that lacks one of the three makes this false.
- **A6.** No reader writes a file in the checkout.
- **A7.** No reader submits a verdict or raises an escalation.
- **A8.** Each doc review records one verdict at the head the reviewer read.
- **A9.** A reader's finding that the reviewer does not judge as a fault never appears in the
  verdict as a reason to reject.
- **A10.** The pin of `asd-ste100` names upstream commit 255631a.
- **A11.** The content-lens skill names the upstream and the commit of the ce-doc-review personas
  and of pstack `design-red-flags.md`.
- **A12.** After a reject, the reviser lands the review's edit set in one commit on the document's
  pull request. A second commit of that run on the branch makes this false.
- **A13.** No commit from the doc-reviewer run or from a reader lands on the document's pull
  request.
- **A14.** `loop-sdlc-review` no longer forbids a subagent that finds, and it still forbids a
  verdict from anyone but the reviewer.

## Boundaries with other epics

- **The optional ADR stage, factorio-ku99, `docs/prd/optional-adr-stage.md`.** That epic owns the
  ADR stage, the ADR test and the ADR review section of the doc-reviewer runbook. This epic adds
  the readers to that review and changes none of its checks. By the epic body, the ADR test and the
  ADR stage stay out of this epic.
- **The vocabulary, factorio-vlle, `docs/prd/loop-vocabulary.md`.** That epic owns every rename
  and names the doc review phase. Its PRD names this epic as the owner of the doc review subloop.
  This epic renames nothing and uses the glossary's names.
- **Kernel and userspace skills, factorio-yzrt, `docs/prd/kernel-and-userspace-skills.md`.** That
  epic keeps `asd-ste100` a kernel pin, and does not move it. This epic changes only the commit of
  that pin.
- **The design stage, `docs/prd/design-stage.md`.** That epic has the doc-reviewer review each
  design record. This epic adds no reader to that review, because the epic body names PRDs, specs
  and ADRs only.
- **Retro, factorio-1xq0, `docs/prd/retro.md`.** Its PRD lists this epic as one that adds a stage.
  This epic adds no stage. The readers run inside the doc-reviewer run. The retro reads the
  doc-reviewer's records as it does today.
- **The simplify stage, factorio-1r94, `docs/prd/simplify-stage.md`.** That epic places a simplify
  run in the flow of a ticket only, and keeps documents out. Both epics may cite `loop-unslop`, and
  neither changes it.
- **`loop-unslop` and `loop-asd-ste100`, skills in the pack.** ADR 0007 owns how the pack holds
  them. This epic starts readers that follow them, and changes no rule in either.
- **Checked against:** `docs/prd/optional-adr-stage.md`, `docs/prd/loop-vocabulary.md`,
  `docs/prd/kernel-and-userspace-skills.md`, `docs/prd/design-stage.md`, `docs/prd/retro.md`,
  `docs/prd/simplify-stage.md`, `docs/prd/conditional-kernel-skills.md`,
  `docs/prd/project-scoped-settings.md`, `docs/prd/shipped-screen-check.md`, ADR 0007, ADR 0013,
  the doc-reviewer runbook, the reviser runbook and the glossary. One conflict found, with the
  retro PRD, which says this epic adds a stage. This PRD adds none.

## Out of scope

- The ADR test and the ADR stage. factorio-ku99 owns them, by the epic body.
- Each rename, such as amender to reviser. The vocabulary epic, factorio-vlle, owns them, by the
  epic body.
- Readers on a design record. The design stage epic owns that review.
- Readers on a ticket's code. The code-reviewer reads code alone, and the simplify stage epic owns
  the cleanup of code.
- Any change to the rules inside `loop-unslop` or `loop-asd-ste100`. Never in this epic.

## Open questions

- `loop-sdlc-review` § 6 says "Never reject on style that no linter flagged." The unslop and
  ASD-STE100 readers find faults of prose that the `prose` gate does not flag. May the reviewer
  reject on such a finding alone? If not, how does a prose edit reach "the final document" when the
  verdict is a pass? Blocks: A9, A12.
- A first PRD reject sends the epic to the reviser. A spec reject returns the epic to the planner,
  and an ADR review is advisory and returns the epic to `architecture`
  (`pack/runbooks/doc-reviewer.md` § 3 and § 4). The epic body says that on a reject the reviser
  applies the edit set. Does a spec reject and an ADR reject go to the reviser too? That change
  reaches the server's transition table as well as the runbooks, which is a second seam by
  `loop-sdlc-split`. Blocks: A2, A3, A12.
- The reviser today answers each finding once. It agrees and changes the document, or it disagrees
  and changes nothing (`pack/runbooks/reviser.md` § 2). Does "one edit set in one commit" keep the
  reviser's right to disagree with a finding of the edit set? Blocks: A12.
- A second PRD reject escalates the epic to the operator. Does the reviser apply the second
  round's edit set before that escalation, or does no edit follow a second reject? Blocks: A12.
- ce-doc-review holds eight personas. Does the content-lens reader take all eight, or the two that
  ce-doc-review always starts, coherence and feasibility, with the rest by a signal? Blocks: A11.
- A reader adds one model run each, so a review costs three more runs. Does every project get the
  readers, or does a project turn them on? The epic body names no setting. Blocks: A1, A2, A3.
