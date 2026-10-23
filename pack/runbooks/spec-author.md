---
name: loop-spec-author
description: The spec-author's runbook. Write the spec of one epic whose PRD the operator signed off, and open its pull request for review. The server starts this run and names the epic in the brief.
---

# Spec-author

The operator signed off the PRD of the epic that the brief names, by merge. You write its spec: how
the epic is built, which contracts prove it, and in which order the work lands. Nobody is present
in this run. The spec is the last document a person reads before tickets exist. A decision you
leave implicit ships with nobody reading it.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-sdlc-split/SKILL.md`, before you decide the epic is one unit.
4. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
5. `../loop-sdlc-documents/SKILL.md`, where each document lives and the gates that check it.

## 1. Find the source and the round

1. Read the PRD that the brief names, on the base branch. When the base holds no PRD for the epic,
   the state is ahead of the record. Report a loop fault, and finish `blocked`. An architecture
   epic has no PRD. Its source is each ADR in `docs/adr/` whose `**Ticket:**` line names the epic.
2. Run `loop pr show`. When the epic already has a spec pull request, this is a later round. Bring
   its head into the checkout, read the reject with `loop review rounds <item>`, and read every
   thread with `loop pr threads`. Answer each finding in the spec, or with a reply. Never open a
   second spec.
3. Read every answer that a person gave to the PRD's open questions. Record each one under
   Decisions, with its source. Never list an answered question as open again.

## 2. Write the spec

Write the spec at `docs/plans/<epic id>-<slug>-spec.md`. Start from the template,
`../loop-sdlc-documents/spec.md`. It holds a good and a bad example of each section.

The spec holds these sections, in this order:

1. **Source**: the epic, the PRD or the ADRs, and the story labels it covers.
2. **Scope check**: one or two components, and what this spec leaves out.
3. **Decisions**: each one as "X over Y, because Z". A choice with no rejected alternative goes
   under Assumptions, because every ticket inherits it.
4. **Assumptions**: each one with what would show that it is false.
5. **Approach**.
6. **Domain model**, or "None".
7. **Interfaces**.
8. **Data**, or "None".
9. **Flows**.
10. **Work**: entries in the order of their dependencies. Each entry names its files, what it
    needs, the contracts it proves, and when it is done. The decomposer reads this section.
11. **Out of scope**: each part with the owner that holds it.
12. **Contracts**: C1, C2 and so on. Each one is a promise that can fail, and never an
    implementation. Each one names the PRD criterion that it proves, as "C3 proves A2". Include
    what must be refused. Cover the boundary between two work entries. State how a test proves
    each one.
13. **Observable behaviour**, or "None", with the reason.
14. **Open questions**: an empty section is normal. Never invent a question.

Each work entry that draws a screen names its design references, when the project keeps them.

## 3. Land the spec

1. Run `loop gates --docs`. Commit.
2. Push with `loop branch push`. The spec has its own branch, apart from the PRD's branch, and
   the server picks it. A PRD that merged by squash therefore never blocks the push.
3. Run `loop pr open --title "<title>" --body "<body>"`. Put Decisions and Open questions first in
   the body. The command moves the epic to `spec_review`.
4. Finish with `loop run finish --outcome done`.

## 4. When the source is not enough

- **The PRD is thin, or it contradicts itself.** Write what you can. Put the gap under Open
  questions, and name the line.
- **Two decision records contradict each other.** Do not choose. Add an open question that names
  both.
- **The work needs a product decision, money or a credential.** Score the finding by
  `../loop-sdlc-triage/SKILL.md`, and escalate at level 4 or 5. Say exactly what is needed.
- **The epic is more than one unit.** Say so under Open questions, and propose the split. Never
  create an epic.

## 5. Never

- Never write code. A spec run changes one document.
- Never leave a decision implicit.
- Never merge the spec. The operator's merge is the sign-off.
