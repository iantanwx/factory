---
name: loop-sdlc-documents
description: Where a project keeps its PRDs, specs and ADRs, the template of each, the document gates that check them, and the two rules every requirement keeps. Read this before you write, revise or review one of the three.
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

## 5. Two rules every requirement keeps

A requirement is a line that a later stage must prove. The rules below bind each place that holds
one: `## Requirements` of a PRD, `## Consequences` and `## Acceptance criteria` of an ADR, and
`## Data` and `## Contracts` of a spec. A ticket copies no requirement. It names the contracts of
its spec. No gate reads either rule, so each reviewer applies both by `../loop-sdlc-review/SKILL.md`.

### Assert a vendor's outcome, never a vendor's mechanism

A vendor owns its infrastructure, and it changes that infrastructure on its own schedule. So a
criterion that asserts a vendor's hostname, record value or internal step fails when the vendor
moves. It fails on the vendor's schedule. It proves nothing about the project, and a person reads
it as a fault in the project.

- **Assert the outcome you depend on.** The mail vendor reports both sending domains as
  `verified`. That outcome holds whatever hostnames the vendor publishes.
- **Never assert the value behind the outcome.** An SPF string such as
  `v=spf1 include:amazonses.com ~all` is the vendor's mechanism. A vendor can stop publishing it
  and tell nobody.
- **Name a vendor's mechanism only as the place you look.** Then name what you prove by reading
  it. Warn a later reader that the value can change.
- **Keep every check that survives a vendor change.** Two DKIM keys that differ prove a split of
  sending reputation. The vendor's choice of selector proves nothing.
- **Never read this rule as a ban on checks.** An outcome you depend on still needs a check that
  can fail.

One ADR asked for SPF, DKIM and DMARC records on each sending subdomain. Its ticket turned that
sentence into five criteria. The vendor then changed the hostnames, so two of the five could never
pass. The implementer escalated, and a person read DNS that already worked. A red gate costs one
more run. A criterion that cannot pass costs a person's attention, and the person can close
nothing.

### A later ADR wins a conflict with the ADR it amends

A later ADR can amend an earlier one. The two can then hold requirements that disagree. Follow the
later ADR. The later ADR is the one with the higher number.

This rule decides which requirement holds. It gives no document permission to edit either ADR.

- **Read the amendment before the ADR it amends.** Before you follow ADR NNNN, search `docs/adr/`
  for each ADR that cites it: `grep -rln "NNNN" docs/adr/`. A later ADR that cites it can amend it.
- **Never follow the earlier requirement.** The word an ADR chooses for itself decides nothing. A
  later ADR that calls itself an amendment, and not a supersession, still wins.
- **Never apply this rule outside `docs/adr/`.** An ADR amends another ADR. It amends no PRD, and
  no user story in a PRD.

One ADR asked for price bars from a fixed start date. The data window of an earlier ADR starts
five years before the run date, so it cannot reach that day. A spec kept the earlier window, and a
person then had to choose the later ADR.

No gate reports this fault. The later ADR can state that the earlier ADR keeps its window, and
that sentence is true on its own. The conflict is arithmetic between two requirements, and the
`adr` gate reads neither requirement.
