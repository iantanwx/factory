# ADR 0002: Refuse an unanswered spec and a misshaped ticket in the server, and port no tickertown-only document rule

**Status:** Proposed
**Date:** 2026-10-24
**Ticket:** factorio-wnl

## Context

The tickertown parity audit of 2026-10-23 found six gaps in the checks that refuse a badly shaped
document or ticket. Today only the prose of a runbook guards most of them. The operator asked to
"identify all gaps, draft requirements for them, then push them through the entire SDLC process",
and asked for an ADR. Tickertown is the repository at `/home/operator/projects/alpha`, read on
2026-10-24 at the merge of its pull request #4040. This run read the loop at `0943166`, and at
`6bfba44` for the files that main changed since then.

`loop work show factorio-wnl` refuses this run with `CAPABILITY_DENIED` (`item_not_held`). So this
run read the epic body from the `loop epic create` command in the coordinator's transcript, as the
run of ADR 0003 did.

This run measured each fact below in source. The last list names what it assumed.

**G1: a spec can be signed off with open questions.** Measured:

- The spec template says that each open question "blocks the sign-off"
  (`pack/skills/loop-sdlc-documents/spec.md:371-374`). The coordinator runbook tells the
  coordinator to prepare one question for each unanswered entry before a sign-off
  (`pack/runbooks/coordinator.md:165`). The decomposer runbook tells it to read each answer
  (`pack/runbooks/decomposer.md:32`). No code reads the section.
- `loop inbox answer signoff/...` merges the document PR at the pinned head, and it reads no file
  of the PR (`internal/inbox/signoff.go`, `signoff`). `loop epic decompose` reads the merged PRD
  for its story labels (`internal/pipeline/pipeline.go:119`, `prdLabels`), and it never reads the
  spec.
- The PRD template hands its open questions on: "The spec author must settle or escalate each
  question" (`pack/skills/loop-brainstorm/prd.md:258`). The spec-author records each answer under
  Decisions (`pack/runbooks/spec-author.md:30`).
- A person's comment on a document PR in sign-off moves the epic to amending, and the amender is
  the one writer there (`internal/machine/epic.go:55-66`, `amendingFor`).
- Tickertown's `pnpm land` refuses a PRD or a spec PR in sign-off whose `## Open questions`
  section holds a list entry with no bold `**Answered` or `**Deferred` run
  (`scripts/sdlc/land-questions.ts`, `scripts/sdlc/spec-answers.ts:73-75`). It also counts
  `sdlc:answered` markers on the Linear epic, and it passes when the markers are at least as many
  as the open entries. It pairs no marker with a question (`spec-answers.ts:178`). The same rule
  refuses `queue create --parent` on an epic in `Decomposing`. The rule exists because two specs
  landed with open questions on 2026-10-21, and each cost a decomposer run, a fixer run and about
  90 minutes (`land-questions.ts:4-13`).

**G2: no check reads a ticket body at its write.** Measured:

- `loop work create`, `loop work edit`, `loop work shape` and `loop epic decompose` check the
  title, the story labels and secrets (`internal/pipeline/adhoc.go:111`,
  `internal/pipeline/decompose.go:122` at `6bfba44`, `internal/machine/verbs.go:211`, `:322`).
  None reads the body.
- The decomposer runbook lists seven body sections, from Source to Stop conditions
  (`pack/runbooks/decomposer.md:58-67`). The reproducer runbook lists the sections of a shaped
  bug (`pack/runbooks/reproducer.md:55-64`).
- Main already refuses a batch of more than 25 tickets (`internal/pipeline/decompose.go:39` at
  `6bfba44`, `TicketCap`).
- Tickertown's `ticketGate` (`scripts/sdlc/ticket-body.ts:170-179`) runs on `queue create
  --parent`, on `queue edit` of a child, and on the bug file and shape steps. It holds a word
  budget: 400 words for the body, 150 for Change, 60 for Done means, 60 for Out of scope, and at
  most 8 contracts of 80 words each (`scripts/sdlc/ticket-budget.ts:84-128`). The budget exists
  because tickets of 1385, 2552 and 1662 words reached review on 2026-09-30. It also runs STE and
  unslop prose rules, two bold-markup rules, and four rules of tickertown's product: QA states,
  design tickets, frames from `design/manifest.json`, and raster art under
  `apps/tickertown/public/`.

**G3: the `citations` gate has no coverage report, `--since`, file arguments, `@scope/` alias or
cross-PRD rule.** Measured:

- The loop's `citations` gate checks every `docs/prd/*.md`, and it compares story labels with the
  merge base of `HEAD` and `--base` (`internal/gates/citations.go`, `droppedStories`).
- In the loop, each PRD defines its own labels in its own headings, and the server reads the
  labels of the epic's own PRD (`internal/pipeline/pipeline.go:119`). A label `US-01` in two PRDs
  names two stories.
- In tickertown, stories are files in a `user-stories/` folder outside the PRDs, and each PRD
  claims some of them. `--coverage` counts the locked stories that no PRD claims, and
  `duplicate-story` refuses a story that two PRDs claim (`scripts/sdlc/prd-ownership.ts:122-133`,
  `:170-176`). `@scope/` resolves to a folder of tickertown's product notes
  (`scripts/sdlc/validate-citations.ts:29-32`).

**G4: no gate refuses a placeholder spec, and no command names an epic's spec path.** Measured:

- The `prd` gate refuses a sizing line that holds the template's placeholder
  (`internal/gates/prd.go:50`). The `adr` gate refuses the template's instruction block and checks
  the four sections (`internal/gates/adr.go`). No gate reads `docs/plans/*-spec.md`, except the
  `prose` gate (`internal/gates/prose.go:32`).
- The epic records its PRD path as `prd_path` and its ADR path as `adr`
  (`internal/machine/rows.go:34`, `:36`), and `loop work show` prints both. It records no spec
  path. The server finds the spec PR by its branch, `loop/<epic>-spec`
  (`internal/github/service.go:210`).
- Tickertown's `pnpm artifact exists` counts a file under 200 bytes as a placeholder
  (`scripts/sdlc/artifact.ts:35-36`). `pnpm artifact path` builds a new path from the Linear
  branch name (`artifact.ts:113-126`).

**G5: no command checks the review checklist.** Measured:

- `loop review submit` takes a verdict, a head and one free-text body. The code-reviewer runbook
  tells the reviewer to prove each contract (`pack/runbooks/code-reviewer.md:56-58`), and the
  review skill tells it to report every fault in one round (`pack/skills/loop-sdlc-review/SKILL.md`
  section 4). No code reads the body.
- Tickertown's `pnpm reviews checklist` reads the ticket file, and it refuses a code verdict that
  holds no JSON check for each contract id, each Done means line and each stop condition
  (`scripts/sdlc/review-checklist.ts:180-224`). It exists because three tickets escalated at
  round 3 on 2026-10-11: round 3 raised a fault that the ticket named from the start
  (`review-checklist.ts:1-8`).

**G6: the `prd` gate has no frame rules.** Measured:

- Tickertown's `prd` gate runs `doc-frames.ts` and `spec-frames.ts`. The first refuses wordings
  that put off the look, such as "the Figma lock", "Septin's lock" and "Layer 2 pending"
  (`scripts/sdlc/doc-frames.ts:57-66`). The second requires a frame id from `design/manifest.json`
  for each work entry that reaches `apps/tickertown/src/app/` or `src/components/`. Tickertown's
  ADR 0020 decided both after its game app shipped with default browser styles on 2026-10-09.
- `grep -rn 'design/manifest.json' internal pack` in the loop prints nothing.

Assumed, not measured:

- The coordinator can carry an operator's answer to the amender as a comment on the spec PR. The
  amender's trigger reads "a person's comments". The spec of factorio-wnl checks which author the
  reactor counts as a person.
- A spec that passed review always has its sections in the template's order. The doc-reviewer
  rejects one that does not (`pack/runbooks/doc-reviewer.md:70-71`), so a gate on the order
  rejects no spec on main today. The spec checks this against the two specs in `docs/plans/`.

## Decision

| Gap | Verdict | Where it lands |
| --- | --- | --- |
| G1 | Port, for the spec only | `inbox answer signoff/...` and `epic decompose` |
| G2 | Port the sections and the budget | `work create`, `work edit`, `work shape`, `epic decompose` |
| G3 | Obsolete | Not ported |
| G4 | Port as a `spec` gate and a `spec_path` fact | `loop gates --docs`, the epic's facts |
| G5 | Port as a contract-id check | `review submit` on a ticket |
| G6 | Tickertown policy | Not ported |

**G1. The server refuses to merge a spec at sign-off, and refuses `epic decompose`, while an
entry under `## Open questions` holds no bold `**Answered` or `**Deferred` run.** The operator's
answer stands in the spec at the head the operator approves. The amender writes it there, as it
answers any comment on a document in sign-off. We reject tickertown's marker count, because it
pairs no answer with its question, and because an answer outside the spec is the fault that
caused the rule. We reject the same refusal on a PRD, because the loop's PRD template hands each
open question to the spec-author, who settles or escalates it. We reject a docs gate for the
rule, because open questions are normal in review, and a red gate would stop the review that
should read them. The refusal at decompose catches a spec that a person merged on GitHub.

**G2. The server refuses a ticket body under an epic, and a shaped bug body, that lacks a
required section of its runbook or breaks tickertown's word budget.** The required sections are
Source, Change, Contracts to prove, Files, Done means and Out of scope. A shaped bug also needs
Reproduction and What was seen. The limits are 400 words for the body without Contracts to prove,
150 for Change, 60 for Done means, 60 for Out of scope, and 8 contracts of 80 words each. A
PRD-less ticket that the coordinator creates (plan R48) gets no check, because its body quotes the
operator. We reject the STE and unslop rules on a body, because they need Python on the host, and
the server would then run the prose toolchain on each write. The `prose` gate already reads the
documents that bodies cite. We reject the QA states, design, frame and art rules, because each
names tickertown's product paths.

**G3. The loop ports none of the `citations` extensions.** In the loop a PRD defines its own story
labels, so no story stands outside a PRD to be uncovered, and no story can be claimed twice. Plan
R45 states that each PRD's stories carry labels that its specs and tickets cite. `--base` already
does the work of `--since`. The gate reads every PRD in milliseconds, so file arguments save
nothing. We reject the `@scope/` alias, because it names a folder of tickertown's product notes.

**G4. A new `spec` gate in the `--docs` list checks each `docs/plans/<epic id>-<slug>-spec.md`
for the template's sections in order and for no template placeholder, and the server records the
spec's path as `spec_path` when the spec merges.** `loop work show` then names the PRD, the ADR
and the spec of an epic. We reject tickertown's 200-byte floor, because a copy of the template is
far longer, and it passes. We reject an `artifact path` verb, because it builds a path from the
Linear branch name, and the loop's spec path holds the epic id already. We reject a new verb for
the lookup, because `loop work show` already prints the facts of an epic.

**G5. `loop review submit` on a ticket refuses a verdict whose body does not name each contract id
under the ticket's `## Contracts to prove`.** The rule holds for a pass and for a reject. We
reject tickertown's JSON checklist, because the loop records one comment for each round, and a
second format of the same review would be a second text to keep true. We reject checks for each
Done means line and each stop condition, because CI checks the gates that Done means names, and a
stop condition stops the implementer, not the reviewer.

**G6. The loop does not port the frame rules.** They name Figma, Septin, Layer 2,
`design/manifest.json` and tickertown's app paths, and tickertown's ADR 0020 decided them for its
own screens. The plan names design issues as tickertown policy, not loop behaviour (plan, Scope
Boundaries). We reject a general form of the rule, because no other project names a frame
manifest. A project that needs it adds the rule to its own `lint` command.

## Consequences

What changes:

- **Sign-off.** `internal/inbox/signoff.go` reads the spec at the pinned head before it merges,
  and refuses with a new rule, `open_questions`, that names each undecided entry. A PRD sign-off
  is unchanged.
- **Decompose.** `internal/pipeline/decompose.go` reads the spec at the base through `spec_path`,
  and refuses the batch with the same rule. It also checks each ticket body by G2.
- **Ticket bodies.** A new body check in `internal/pipeline` serves `work create --epic`,
  `epic decompose` and `work shape`. `internal/machine/verbs.go` `workEdit` runs it on a ticket
  that holds an `epic` or `bug` fact. A refusal names each missing section and each count over
  its limit.
- **The epic's facts.** `internal/machine/rows.go` gains `spec_path`. The reactor sets it from the
  changed files of the spec PR on `doc_merged`.
- **Gates.** `internal/gates` gains a `spec` gate with `Docs: true`. The section list lives in Go
  once, and a test holds it equal to `pack/skills/loop-sdlc-documents/spec.md`.
- **Review.** `review submit` reads the ticket body, and refuses with a new rule,
  `contract_unnamed`, that names each missing contract id.
- **The pack.** The coordinator runbook says how an answer reaches the spec before sign-off. The
  decomposer and reproducer runbooks name the limits of G2. The code-reviewer runbook names the
  rule of G5.

What this makes easy: a spec with an open question cannot become tickets, and a ticket that
copies its contracts' text never reaches the implementer.

What this makes hard: an answer at sign-off moves the spec's head, so the operator approves the
spec a second time (ADR 0010). A ticket with nine contracts must split.

A later reader must not:

- count an answer that stands outside the spec, such as an inbox answer or a comment, as an
  answer to an open question.
- refuse a PRD sign-off for its open questions.
- run project tools or Python from the server to check a ticket body.
- port `--coverage`, the `@scope/` alias, the frame rules or the JSON checklist without a new ADR
  that names the fault behind it.

Postponed on purpose: per-project limits for the budget. The spec decides whether
`.loop/config.yaml` may name them.

## Acceptance criteria

- `loop gates --docs` passes.
- `grep -c '^\*\*Ticket:\*\* factorio-wnl$' docs/adr/0002-document-and-ticket-gates.md` prints `1`.
- `ls docs/adr | grep -c '^0002-'` prints `1`.
- `grep -c '^| G[0-9] |' docs/adr/0002-document-and-ticket-gates.md` prints `6`. Every gap has one
  verdict.
- `grep -rn 'design/manifest.json\|@scope/' internal pack` prints nothing.
- `loop schema | grep -c '"name":"coverage"'` prints `0`.

Epic factorio-wnl owns these checks, through its spec and tickets:

- A test proves that a sign-off answer on a spec with an undecided open question merges nothing,
  and refuses with `open_questions`.
- A test proves that `epic decompose` refuses on a spec at the base with an undecided entry.
- A test proves that the server refuses a ticket body with no Change section or with 401 words,
  and that it accepts a PRD-less ticket with the same body.
- `loop gates --list --docs` names a `spec` gate.
- A test proves that `review submit` refuses a body that omits one contract id of the ticket.
