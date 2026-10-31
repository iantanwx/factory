---
name: loop-sdlc-split
description: How to tell when one epic must become several, who splits it, and how. Read this when a PRD or a spec reaches more than one subsystem, or covers work that cannot land as one unit.
---

# loop-sdlc-split

An epic that reaches more than one subsystem cannot be specified or tested as one unit. This skill
states where the seam falls, who may cut along it, and how the cut runs.

A split creates epics, and a wrong epic costs a person's time to undo. So each step of the
procedure below makes the create the last thing that happens.

## 1. The seam is the source, not the size

Ask which source each part of the work comes from.

- Product behaviour has a source that a person approved: a user story or the operator's own words.
  It belongs to a PRD.
- A decision with no product source is architecture. It belongs to a decision record, not to a PRD.

One epic must not hold both. A review checks each PRD requirement against its source. An invented
requirement in an architecture epic has no source, so no check reads it.

## 2. Two questions decide the unit

1. Can each part land on its own?
2. Can each promise be proven without building the rest?

Two parts that must land together are one epic, and the ticket count does not change that. An epic
that fails either question is two epics. A size number only prompts the questions. It decides
nothing.

## 3. Who splits

Only the operator agrees to a split, in an intake room. The room keeps the epic it holds, and each
new epic gets an intake room of its own. Section 7 states why.

- **The doc-reviewer names the seam.** At the PRD stage it rejects the PRD and names the seam in
  its comment. It never creates an epic.
- **The unattended brainstorm never splits.** It writes one PRD for one epic. When the request needs a
  second epic, it escalates with a score.
- **The amender never splits.** When the seam needs a second epic, it escalates with a score.
- **The spec-author never splits.** It names the seam under Open questions, and proposes the
  split.
- **The decomposer never splits an epic.** It cuts tickets inside one epic.
- **The coordinator does the steps that a room cannot.** It retitles the epic that the split
  keeps, opens a room for each new epic, and creates each architecture epic.

## 4. Stop when a split is too late

- **The epic already holds tickets.** A split then moves work under a new parent. Escalate, and
  name each ticket.
- **The epic is past its PRD sign-off.** A split at that point is two documents to rewrite, and a
  spec that already cites the old scope. Escalate with the seam and its evidence.

## 5. Reuse the epic you started with

Keep the epic that the room holds for one part of the split. It already holds its status, its
pull request and its history. Reuse costs one revision. A replacement costs a submit and a cancel,
and the cancel deletes the record of why the work exists.

1. Rewrite the epic's PRD so that it holds one part only. Keep its path. Never move a PRD after its
   epic exists, because the server reads the story labels at that path.
2. Push the revision to the epic's pull request with `loop branch push`.
3. Write the new title and the other parts in the decision file, and apply it by the intake-room
   runbook. A room cannot retitle an epic. The coordinator reads the decision, and runs
   `loop work edit <epic> --title "<title>"`.

When the room holds no epic, the first PRD that it submits is the epic it creates. Section 7 then
applies to each other part.

## 6. Write every document first, and gate it

Write each PRD before any command creates its epic:

1. Write the PRD at `docs/prd/<slug>.md`. Under Boundaries with other epics, name each sibling and
   say who owns each shared piece.
2. Run `loop gates --docs`. Correct each finding.
3. Commit.

The order makes a crash harmless. A crash here leaves a spare file, which a person reads in
seconds. A crash after a create leaves an epic whose PRD fails its gates, and the doc-reviewer
must reject it.

An architecture part has no PRD. Its ADR must name its epic on the `**Ticket:**` line, so the ADR
comes after the epic. The coordinator creates that epic with
`loop epic create --architecture --title "<title>" --body "<what the ADR decides>"`. Then the ADR
names the new id. Run `loop gates --docs` on the ADR before its pull request opens, because the
`adr` gate refuses an ADR with no Ticket line.

## 7. Create each sibling in a room of its own

One intake room creates one epic. The server keys the epic to the room. A second
`loop prd submit` from the same room finds the first epic again, and a room that holds an epic
submits nothing.

1. The decision file of the first room names each other part: its title, its scope and the seam.
2. The coordinator opens one room for each part:
   `loop room open --kind intake --project <project> --reason "<the part and the seam>" --score <level>`.
3. Each new room writes its PRD by section 6, then submits it once:

```bash
loop prd submit --path docs/prd/<slug>.md --title "<title>"
```

The loop records no edge between two epics. `loop epic decompose` blocks a ticket only on a ticket
of the same batch. So write what one sibling needs from another under Boundaries with other
epics, in both PRDs. Two siblings that each need the other are one epic, by section 2.

## 8. Read each write back

A command that exits 0 is no proof that the state changed. Read each epic back:

```bash
loop work show <epic>                                   # status, title and prd_path
loop work list --kind epic --status prd_review          # each sibling appears once
```

- `loop work show <epic>` reads `prd_review` as the status, the new title, and the PRD's path in
  the `prd_path` fact. Compare the title with the decision file, not with the command you ran.
- The answer of `loop prd submit` names each story label in `stories`. Compare that list with the
  PRD. A story that the list lacks has no label, and no ticket can cite it.
- `loop work list --kind epic --status prd_review` holds each sibling once. Two epics with one
  title are a submit from two rooms for one part.

## 9. If it goes wrong

- **The submit exits 4, or the session ends during the submit.** Run the same `loop prd submit`
  again, with the same path and title. The epic's key is the room. So the repeat finds the same
  epic, the same branch and the same pull request. Never open a second room for the same part.
- **The submit refuses with `room_holds_item`.** This room revises the epic it holds. Push the
  PRD with `loop branch push`. Each other part needs a room of its own, by section 7.
- **The submit refuses with `prd_moved`.** This room already submitted a PRD at another path.
  Submit that path again, or name the part in the decision file for a room of its own.
- **The submit refuses with `prd_past_review`.** The epic left PRD review. The split is too late,
  so section 4 applies.
- **The room open refuses with `CAP_FULL`.** The project's epics in counted document states fill
  the intake cap. Each sibling waits until the count drops, or until the operator raises the cap.
  Tell the operator which epics hold the cap.

## 10. Never

- Never split an epic that the operator has not agreed to split.
- Never create an epic from a run. Only an intake room creates one, with `loop prd submit`, or the
  coordinator, with `loop epic create`.
- Never create an epic before its document passes its gates.
- Never cancel an epic that a split can keep.
- Never trust a write that you did not read back.
