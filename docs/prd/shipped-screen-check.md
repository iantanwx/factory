# Check shipped screens against the signed design record

## Stories

The operator asked for this epic in two intake rooms. In room-0fefd19b the operator agreed to split
the design work into two epics. The sibling epic, factorio-a4yq, holds the design stage and the
signed design record (`docs/prd/design-stage.md` on its pull request, #243). This epic holds the
check of the shipped screens against that record. The operator chose each part of this epic in
room-36e9bc8c. Each story below quotes the operator's words from those rooms. The text under each
story states the choice that the operator made.

The operator named three problems from past projects. Tickets build the wrong screens, the screens
of different tickets look and act differently, and nothing checks the shipped screens against what
the operator wanted.

> "all of the above problems apply, from past experience."
>
> "it's easier to show, not tell."
>
> "let's go with option 1. [the split into two epics] ... send it"

Today the spec-author runbook asks each work entry that draws a screen to name its design
references, when the project keeps them (`pack/runbooks/spec-author.md:60`). The decomposer stops
on a screen that has no matching design reference (`pack/runbooks/decomposer.md:134`). No stage
captures a shipped screen, and no review compares one with a design.

### US-01 Each spec entry and ticket of a UI story cites the screen states it builds

As the operator, I can rely on each spec entry and each ticket of a `UI: yes` story to cite the
screen states that it builds, so that each ticket names the picture that it must match.

> "yes"
>
> "option 2"

The operator accepted this story as written. The operator then chose an explicit line for work
that draws no screen. A spec entry or a ticket of a `UI: yes` story cites at least one state of the
epic's screen list, or it holds a `Screens: none` line with a reason. The operator rejected a rule
that makes every ticket cite a state, because a ticket that only builds an API route would cite a
screen that it never touches. The operator also rejected a rule with no count check, which only
refuses a wrong citation.

### US-02 The implementer captures each cited state at the pull request head

As the operator, I can rely on the implementer to capture each cited state at the head of its pull
request, so that the reviewer reads what shipped and not a description of it.

> "option 1"

The operator chose a capture branch of each ticket that never merges, `loop/<ticket>-captures`. The
captures of one head sit at `<head sha>/<state id>.png` on it. The operator rejected captures that
merge into the product with the ticket's diff. The operator also rejected a reviewer that takes its
own captures, which doubles the capture time of each review.

> "option 1"

The operator chose to keep each capture branch on the remote, and the loop never deletes it. A
later bug report or retro can then read what shipped at each head. The operator rejected a delete
when the ticket merges, and a delete when the epic closes.

### US-03 The code-reviewer rejects a structural difference that the operator did not accept

As the operator, I can rely on the code-reviewer to reject a ticket whose captured state differs
in structure from the signed state, unless I accepted the difference, so that the shipped screens
match what I signed off.

> "yes"
>
> "option 2"

The code-reviewer compares structure: the elements, the copy, the flow and the states that exist.
It compares no pixels. The operator chose to accept a difference once for each state, on the epic.
Each later ticket of the epic that cites the state reads that acceptance. The signed record stays
as it is. The operator rejected an acceptance that holds for one ticket only, because the next
ticket that cites the state would see the same difference again. The operator also rejected an
acceptance that only a new design pull request can give, because each accepted difference would
then wait on a full design review.

### US-04 A state marked for pixel fidelity gets a pixel diff on each ticket that cites it

As the operator, I can mark a screen state `fidelity: pixel` at design time, so that each ticket
that cites the state gets a pixel diff, and the code-reviewer rejects a result over its threshold.

> "i'd go with option 1 with an optional human initiated pixel diff where high fidelity is
> paramount."
>
> "option 1"
>
> "option 2"

The operator made two choices in room-36e9bc8c. First, the pixel diff is a loop gate, and CI runs
it at each head against the captures on the capture branch. The operator rejected a diff inside the
review run only, and a CI run that takes its own captures. Second, the default threshold is 0.5% of
the pixels, and a threshold can be from 0% to 5%. The operator rejected 0.1% as too strict for
text rendering on two hosts, and 1% as too loose for a small icon.

## Scope check

The operator accepted the four stories as written.

> "yes"

US-01 is the citation of states and its check at decompose. US-02 is the capture of each cited
state on the capture branch. US-03 is the structural review and the acceptance of a difference.
US-04 is the pixel gate. Each story reads the merged design record of factorio-a4yq, so this epic
starts after that record format exists.

**Stories:** 4
**Estimated total:** 2,000 lines
**Lands independently?** Yes. US-01 merges alone, because it changes the spec and ticket shape and the decompose check only. US-02 merges after US-01, because it captures the states that a ticket cites. US-03 merges after US-02, because it reads the captures. US-04 merges after US-02, because its gate reads the captures. It reads the acceptance record of US-03 only when that record exists.
**Promises provable alone?** Yes. "Decompose refuses a ticket with no citation and no `Screens: none` line" needs a record fixture and a batch fixture. "Each cited state has a capture at the head" needs one fixture ticket, a stub capture command and a remote. "A structural difference with no acceptance is a reject" needs a pair of fixture pictures and one fixture epic. "A marked state over its threshold fails the gate" needs a pair of fixture PNGs and a record fixture. No promise needs a real canvas or a real product.
**Verdict:** proceed.

## Goal

In a project with a UI, each ticket that draws a screen names the signed screen states that it
builds. The reviewer reads a capture of each state at the ticket's head. A shipped screen that
differs from the signed state merges only when the operator accepted the difference. A state that
the operator marked for pixel fidelity merges only within its threshold.

## Requirements

- Each spec entry of a `UI: yes` story cites at least one state of the epic's screen list, or it
  holds a `Screens: none` line with a reason.
  → § US-01
- Each ticket of a `UI: yes` story cites at least one state of the epic's screen list, or it holds
  a `Screens: none` line with a reason.
  → § US-01
- The decompose step refuses a batch in which a ticket of a `UI: yes` story holds neither a
  citation nor a `Screens: none` line, and the refusal names the ticket.
  → § US-01
- The decompose step refuses a `Screens: none` line that holds no reason.
  → § US-01
- The decompose step refuses a ticket that cites a state that the merged screen list does not
  hold, and the refusal names the state.
  → § US-01
- The spec-author runbook and the decomposer runbook name the screen list's states as the design
  references of a screen.
  → § US-01
  → pack/runbooks/spec-author.md
- The doc-reviewer checks the reason of each `Screens: none` line of a spec entry.
  → § US-01
- The implementer captures each state that its ticket cites, with the project's capture command,
  at each head that it pushes.
  → § US-02
- The implementer pushes the captures to the ticket's capture branch, `loop/<ticket>-captures`, at
  `<head sha>/<state id>.png`.
  → § US-02
- The capture branch never merges.
  → § US-02
- The loop never deletes a capture branch.
  → § US-02
- The ticket's pull request holds no capture.
  → § US-02
- The code-reviewer reads the captures of the head that it reviews.
  → § US-02
- The code-reviewer rejects a ticket when a cited state has no capture at the head it reviews, and
  the reject names the state.
  → § US-02
- The code-reviewer compares each capture with the record's picture of the state. It compares the
  elements, the copy, the flow and the states that exist, and it compares no pixels.
  → § US-03
- The code-reviewer rejects a structural difference that the operator did not accept, and the
  reject names the state and the difference.
  → § US-03
- An implementer that cannot match a cited state escalates, and the escalation names the state and
  the difference.
  → § US-03
- The operator can accept a difference of one state through the coordinator, and the loop records
  the operator's words against the state's id on the epic.
  → § US-03
- The loop refuses an acceptance that holds no words of the operator.
  → § US-03
- The loop refuses an acceptance of a state that the merged screen list does not hold.
  → § US-03
- The code-reviewer passes a difference that the operator accepted for the state, on each ticket of
  the epic that cites the state.
  → § US-03
- An acceptance changes no file of the design record.
  → § US-03
- The pixel gate runs in CI at each head of a ticket that cites a state with a `fidelity: pixel`
  mark.
  → § US-04
- The pixel gate compares the capture of each marked state on the capture branch with the record's
  picture of the state, as the share of the pixels that differ.
  → § US-04
- The pixel gate fails when the share is over the state's threshold, and the failure names the
  state and the measured share.
  → § US-04
- The pixel gate fails when a marked state that the ticket cites has no capture at the head.
  → § US-04
- A mark with no threshold of its own uses the default threshold, 0.5%.
  → § US-04
- The pixel gate passes a marked state whose difference the operator accepted.
  → § US-04
- The code-reviewer rejects a ticket whose pixel gate failed.
  → § US-04
- The pixel gate reads no state that holds no pixel mark.
  → § US-04

## Acceptance criteria

- **A1.** The decompose step refuses a ticket of a `UI: yes` story that cites no state and holds
  no `Screens: none` line, and the refusal names the ticket.
- **A2.** A ticket of a `UI: yes` story with a `Screens: none` line and a reason passes decompose.
- **A3.** The decompose step refuses a `Screens: none` line with no reason.
- **A4.** The decompose step refuses a ticket that cites a state that the merged screen list does
  not hold, and the refusal names the state.
- **A5.** After the implementer pushes a head of a ticket that cites states, the capture branch
  holds one capture of each cited state at that head's path.
- **A6.** A merged ticket adds no capture to the base branch.
- **A7.** The capture branch of a ticket is on the remote after the ticket merges, and after the
  epic closes.
- **A8.** A ticket whose cited state has no capture at the reviewed head gets a reject that names
  the state.
- **A9.** A ticket whose capture lacks an element that the signed state shows gets a reject that
  names the state.
- **A10.** A ticket whose capture shows copy that differs from the signed state gets a reject that
  names the state.
- **A11.** A ticket whose only difference from the signed state is one that the operator accepted
  for that state passes the structural review.
- **A12.** An acceptance holds for each later ticket of the epic that cites the state. A second
  reject of the same accepted difference makes this false.
- **A13.** An acceptance with no words of the operator changes nothing.
- **A14.** An acceptance leaves each file of the design record unchanged.
- **A15.** A marked state whose capture differs by more than its threshold fails the pixel gate,
  and the failure names the state and the measured share.
- **A16.** A marked state with no threshold of its own fails the pixel gate at a difference over
  0.5%, and passes at a difference of 0.5% or less.
- **A17.** A marked state whose difference the operator accepted passes the pixel gate.
- **A18.** A ticket that cites no marked state never fails the pixel gate.
- **A19.** A ticket whose pixel gate failed gets a reject from the code-reviewer.

## Boundaries with other epics

- **The design stage and its record, factorio-a4yq.** It owns the record's format, the screen
  list, the `UI:` line of a story, the pixel mark, the default threshold and the allowed range, and
  the capture command in the project's config. This epic reads all of them and changes none. The
  operator chose 0.5% as the default and 0% to 5% as the range in room-36e9bc8c. The sibling's PRD
  holds that question open, so the coordinator carries the answer to it. This epic starts after the
  record format merges.
- **factorio-791i, the conditional load of a kernel skill.** This epic adds no config, so it reads
  no `ui` block that 791i or a4yq defines, except the capture command. The question of who owns the
  `ui` block belongs to a4yq.
- **A QA stage, factorio-nhb5.** It owns visual QA and fuzzy user acceptance tests. This epic adds
  no stage, and its check runs inside code review and CI.
- **The spec-author and decomposer runbooks, `pack/runbooks/`.** This epic narrows their existing
  design-reference lines to the screen list's states. It adds no stage to either.
- **Project-scoped settings, `docs/prd/project-scoped-settings.md`.** It owns the `stages`, `pools`
  and `merge` settings. This epic changes none.
- **Checked against:** the PRD of factorio-a4yq on its pull request #243,
  `docs/prd/optional-adr-stage.md`, `docs/prd/project-scoped-settings.md`, ADR 0002, and the items
  factorio-791i and factorio-nhb5. ADR 0002 rejected tickertown's QA-state rules because each names
  tickertown's product paths. This epic's checks read the project's design directory and the
  ticket's capture branch, and name no product path of any project. No conflicts found.

## Out of scope

- The design record's format, the pixel mark and the capture command. factorio-a4yq owns them.
- A QA stage, visual QA and fuzzy user acceptance tests. factorio-nhb5 owns them.
- A change to the design record when the operator accepts a difference. The operator can open a new
  design pull request by hand, through the flow of factorio-a4yq.
- A comparison of pixels for a state with no pixel mark. Never, by the operator's choice of a pixel
  diff "where high fidelity is paramount".
- Captures in the product's repository. Never, by US-02.

## Open questions

- The pixel gate counts a pixel as different past a small colour tolerance, which absorbs
  antialiasing. The operator left its value to the spec, in room-36e9bc8c: "option 1". The spec
  sets the value with a contract and a fixture. Blocks: A15, A16.
