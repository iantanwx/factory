---
name: loop-decomposer
description: The decomposer's runbook. Cut the approved spec of one epic into vertical-slice tickets, and create them all in one batch. The server starts this run and names the epic in the brief.
---

# Decomposer

The operator signed off the spec of the epic that the brief names. You cut it into tickets. Each
ticket is one vertical slice that lands and reverts alone. You create every ticket in one batch.
Nobody is present in this run.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-sdlc-split/SKILL.md`, which states why you never split the epic.
4. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.

## 1. Read the whole spec

Read the spec on the base branch. When the base holds no spec, report a loop fault, and finish
`blocked`.

Read every section, not only Work:

- **Work**: the entries and what each one needs.
- **Contracts**: which slice proves each one.
- **Domain model**: the types to define before their first use.
- **Assumptions**: restate each one that a ticket depends on.
- **Out of scope**: never cut work that it excludes.

Read every answer to the spec's open questions before you cut. An answer can add or change work.
Pair each answer with its question. When a question has no answer, escalate. A comment that does
not answer the question is no answer.

An entry under `## Open questions` is decided only when it holds a bold run that starts with
`**Answered` or `**Deferred`. The server refuses a batch on a spec with an undecided entry, with
`SHAPE_REFUSED` and the rule `open_questions`. Never write the answer into the spec yourself.
Escalate each undecided entry by section 6.

## 2. Check the spec against itself

- Every contract belongs to a slice.
- Every slice proves its contracts when it lands, with a test written first.
- Every slice can land alone.

When a check fails and you cannot repair it without a new contract, escalate. Never invent a
contract.

## 3. Cut vertical slices

A slice can be shown working on its own. It fits one fresh context, so you can list its files. It
lands green and reverts alone.

- Reshape the work freely, and never add or drop a contract.
- A change that several slices share is its own ticket. It comes first, and it blocks them.
- A wide mechanical change has three steps. One ticket adds the new form. Batches move the users,
  each blocked by the first. One last ticket deletes the old form, blocked by every batch.
- One ticket is one branch and one pull request. Never cut a ticket that holds only tests.
- A ticket names its contracts by id and title, and never copies their text. The spec holds the one
  text. When a contract spans two slices, say which part each ticket owns.

Each ticket body holds these sections. Write each one as a `## ` heading with this name. The order
is free, and the server reads no other heading.

1. `## Source`: the spec entry and the epic. In an epic that holds `design_merged`, copy the
   entry's `- Screens:` line into `## Source` as a `**Screens:**` line, such as
   `**Screens:** S1.1, S1.3` or `**Screens:** none. <reason>`. A ticket cut from two entries cites
   the states of both. In an epic with no `design_merged`, write no `**Screens:**` line.
2. `## Change`: the behaviour after the change, not the implementation. At most 150 words.
3. `## Contracts to prove`: one list item for each contract, with its id and title. Each one needs a
   proof that fails before the change. At most 8 contracts, at most 80 words each, and at least one
   id such as `C1`.
4. `## Files`: what the ticket may change, and what it must not change.
5. `## Done means`: the project's gates and each contract's proof. A person checks none of it. At
   most 60 words.
6. `## Out of scope`: each part with its owner. At most 60 words.
7. `## Stop conditions`, optional: only for an act outside the repository, an act that spends
   money, or a precondition you can measure.

The body holds at most 400 words outside `## Contracts to prove`. Heading lines do not count.

## 4. Check the graph

- At least one ticket has no blocker.
- No cycle exists.
- Every ticket is reachable from a ticket with no blocker.
- Every ticket cites at least one story label of the epic's PRD. An architecture epic has an ADR
  and no PRD. Each of its tickets cites no story label, and its body names the spec contracts
  (such as `C1`) that it builds.

## 5. Create the batch

Run the document form of `../loop-bro/SKILL.md` over each ticket body before you write the batch.
Its second pass applies `../loop-unslop/SKILL.md`.

Write the batch to a file in a directory from `mktemp -d`. It is one JSON object:

```json
{"tickets": [{"key": "a", "title": "...", "body": "...", "stories": ["US-01"], "blocked_by": []}]}
```

`key` names the ticket inside the batch, and `blocked_by` names other keys. Then run:

```bash
loop epic decompose --file <batch file>
```

The server creates every ticket and every dependency, or none. It refuses a ticket whose story
labels the merged PRD does not hold, with `STORY_UNKNOWN`. It also refuses a ticket of an
architecture epic that cites a story label. Read the hint, repair the batch, and run it again.

The server reads the spec at the base before it writes. It refuses with `SHAPE_REFUSED` in four
cases. Each refusal creates no ticket.

- `no_spec`: the base holds no one spec of the epic. Report a loop fault, and finish `blocked`.
- `open_questions`: the spec holds an undecided entry. The hint names its line. Escalate it.
- `ticket_shape`: a body misses a section of section 3, or a count is over its limit. The hint
  names each fault of each ticket, by key. Repair each body, and run the batch again.
- `ticket_screens`: a `**Screens:**` line is missing from a ticket of a UI story, holds no reason,
  or cites a state that the design record at the base does not hold. The hint names each fault by
  ticket key. Repair each line from its spec entry, and run the batch again.

The server refuses a batch of more than 25 tickets with the rule `runaway_batch`. That is a runaway
limit and no sizing rule. Never merge slices to fit under it. Escalate, and name how many slices
the spec needs and the seam where the epic would split.

Finish with `loop run finish --outcome done`. Report how many tickets you cut, which ones start at
once, and which contracts each one proves.

## 6. When a person must decide

Score each case by `../loop-sdlc-triage/SKILL.md`, and escalate at level 4 or 5. Name the specifics.

- An open question of the spec has no answer.
- No slice can prove a contract.
- Two slices must land together.
- A work entry names no files, and you cannot infer them.
- The spec contradicts its own Out of scope. Name both lines.
- A work entry of an epic that holds `design_merged` holds no `- Screens:` line.

## 7. Never

- Never answer an open question yourself.
- Never merge slices to make the batch smaller.
