---
name: loop-sdlc-documents
description: Where a project keeps its PRDs, specs and ADRs, the template of each, and the document gates that check them. Read this before you write, revise or review one of the three.
---

# loop-sdlc-documents

Three documents carry an epic before its tickets exist. Each one has one place in the project, one
template, and gates that `loop gates --docs` runs on each change.

## 1. Where each one lives

| Document | Path | Template |
| --- | --- | --- |
| PRD | `docs/prd/<slug>.md` | `../loop-brainstorm/prd.md` |
| Spec | `docs/plans/<epic id>-<slug>-spec.md` | `../loop-sdlc-documents/spec.md` |
| ADR | `docs/adr/NNNN-<slug>.md` | `../loop-sdlc-documents/adr.md` |

The epic id is the work-item id that the server gave the epic. The slug is a few words of the
title, in lower case, joined by dashes. An ADR takes the next free four-digit number. Never reuse
a number, and never renumber an ADR.

Never move a PRD after its epic exists. The server records the PRD by its path, and reads its story
labels there.

## 2. The gates

Run `loop gates --docs` before you commit a document. Each gate below names the fault it refuses.

- **`prd`** reads each PRD. The scope check holds the five lines Stories, Estimated total, Lands
  independently?, Promises provable alone? and Verdict. No line holds a placeholder. The verdict
  starts with proceed.
- **`citations`** reads each PRD. Each top-level bullet under `## Requirements` has a `→` citation
  on a line under it, and each citation resolves. The PRD names a story label US-n. No two
  headings define one label. A label that the PRD held on the base branch is still there.
- **`adr`** reads each ADR. The filename and the heading hold one number. The header holds Status,
  Date and Ticket. The four sections are there. Decision names an alternative that it rejected.
  Acceptance criteria holds a list item. The template's instruction block is gone. No two ADRs
  share a number, and each `docs/adr/NNNN` that an ADR cites exists.
- **`prose`** lints the agent-facing documents, the ADRs and the PRDs.

The spec has no gate of its own. The doc-reviewer checks it against its template,
`../loop-sdlc-documents/spec.md`.

## 3. Citations

A PRD requirement cites its source on the next line:

```
- A signal fills at the next day's open, never at the same close.
  → § US-01
- Every strategy reads price, volume and dividend data only.
  → docs/adr/0003-market-data.md § Decision
```

`→ § <heading>` names a heading of the PRD itself, usually a story. A story quotes the operator's
words, so a requirement that cites it traces to them. `→ <path> § <heading>` names a heading in a
file of the repository. The heading match ignores case, and matches the opening words of the
heading.

## 4. The Ticket line of an ADR

The `**Ticket:**` line holds the id of the architecture epic that the ADR decides, or `none`. The
server moves the epic from `architecture` to `spec_needed` when a merge adds an ADR whose Ticket
line names the epic id. An epic id anywhere else in the ADR moves no epic. An ADR whose Ticket line
reads `none` moves no epic. An ADR with no Ticket line fails the `adr` gate, because the epic would
wait with no error.
