---
name: loop-content-lens
description: The lenses of the content-lens reader of a doc review. Coherence and feasibility always run, five lenses run on a signal, and the design red flags read a spec or an ADR.
disable-model-invocation: true
---

<!--
Forked from two upstreams, and ours from here on.

The lenses come from the persona files of ce-doc-review and its team selection rule:

  upstream: https://github.com/EveryInc/compound-engineering-plugin/tree/main/skills/ce-doc-review/references
  commit:   8f7eea958c8f1318147db6c93320cae3930ad09a
  forked:   2026-11-01

We took `personas/coherence-reviewer.md`, `personas/feasibility-reviewer.md`,
`personas/product-lens-reviewer.md`, `personas/design-lens-reviewer.md`,
`personas/security-lens-reviewer.md`, `personas/scope-guardian-reviewer.md`,
`personas/adversarial-document-reviewer.md` and `persona-selection.md`. That repository is
MIT-licensed.

The design red flags come from the architect skill of pstack:

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/architect/references/design-red-flags.md
  commit:   a58628271271837ef5f386adca29c0812683a19a
  forked:   2026-11-01

GitHub reports no license file for that repository. `loop-unslop` forks pstack from the same
repository.

Recorded so that a later reader can diff against upstream and see what we changed on purpose.
This is not a dependency we track, and we do not owe it updates.

Changed on purpose, 2026-11-01:

- Each persona is one lens of one reader. Upstream starts one subagent for each persona. The
  doc-reviewer starts one content-lens reader, and it runs each selected lens in one context.
- The whole-doc persona stays out. The reviewer reads the whole document in this loop.
- The confidence anchors, the autofix classes and the `<review-context>` slots are gone. A reader
  returns a finding with a place, a rule and an edit, and the reviewer judges it.
- Upstream's requirements document is a PRD here, and its plan is a spec or an ADR. A spec always
  cites a signed-off PRD, so its premise is settled.
- Each lens is rewritten in the loop's words, by `loop-unslop` and the STE rules.

`make pack-check` runs both prose linters over this file. Edit the copy in the loop's pack. The
server writes `.claude/skills/` again at each start.
-->
# Content lens

A content-lens reader reads one document by the lenses below. It finds faults in what the document
says. It does not redesign the work, by `../loop-sdlc-review/SKILL.md` § 6.

## How to use the lenses

1. Read the kind of the document in your prompt: `prd`, `spec` or `adr`. Do not read the kind
   from the text.
2. Run the two lenses that always run.
3. Read the signal of each other lens. Run each lens whose signal the document shows.
4. On a spec or an ADR, run the design red flags.
5. Return each finding with a place, a rule and an edit. The rule is `loop-content-lens` and the
   lens name, such as `loop-content-lens coherence`. For a red flag, the rule names the flag, such
   as `loop-content-lens hand-synced list`.

Report a finding only when you can quote the text that shows it. A fault that you cannot quote is
a question, not a finding. Each lens below names what it does not report. Leave that fault to the
lens that owns it.

## Lenses that always run

### Coherence

Find each place where the document disagrees with itself. Two passages that cannot both be true
are a finding.

- **Contradiction.** One section puts a thing out of scope, and another section requires it. Or an
  early constraint breaks under a later approach.
- **Drift of a term.** One thing has two names, or one name means two things. Use the name that
  the document uses most, and change the rest.
- **Broken reference.** The text cites a section, a contract or a label that the document does not
  hold, or that says something else.
- **Wrong count.** A heading claims a count, and the list under it holds another count. The list
  wins.
- **Summary against detail.** A summary claims a thing that the detailed section carves out. The
  detail wins, so fix the summary.
- **Missing peer.** A list omits an item that the document treats as a peer of the listed items
  everywhere else.
- **Ambiguity.** Two careful readers would build two different things from one sentence. Look at
  quantifiers with no bound, conditions with no else, and lists that may or may not be complete.
- **Open dependency.** The document needs a thing and names no owner and no plan for it.

In a PRD, look at the labels of stories and criteria, and at each criterion that cites a story. In
a spec, look at the work entries, their files, their needs and the contracts each one proves.

Do not report style, word choice, formatting, a term the reader knows, or content that the
document defers by name.

### Feasibility

Find each place where the work cannot be built as the document describes it. Read the code that
the document names beside the document.

- **Conflict with the code.** An interface does not match, a dependency is not there, or the work
  replaces a capability that already exists.
- **Paths.** Trace the normal path, a missing input, an empty input and a failure. Report a gap
  only when the document and the code leave a failure with real cost open.
- **Order.** A work entry needs a thing that a later entry builds.
- **Change of stored data.** A migration or a change of format has no safe path for data that
  exists.
- **A decision left to the builder.** The implementer must make an architecture decision that the
  document needed to make.

On a PRD, report only a direction that forces a rework of the approach, a need for a service that
does not exist, or a capability that the code already holds. A PRD defers how the work is built,
so a missing detail of the build is no finding there.

Do not report a style of the build, a test detail, the layout of the code, or a scale concern
with no measurement.

## Lenses that run on a signal

### Product

**Signal:** the document takes a product position that a person who knows the product could
challenge, and no source settled it. Or the work changes what the product becomes.

Ask four questions of a PRD:

- Is this the right problem? A different framing can give a simpler answer.
- Does the work reach the outcome, or a proxy of it?
- What happens if nobody builds it? Real pain has evidence. A need that a user "might" have gets
  the hardest question.
- What makes it fail? For each goal, name the most likely way that the work ships and misses it.

Then look at second-order effects. Does the work close a future path, add a cost that grows, or
make the product harder to learn? Look at alignment too. A requirement that serves no goal, or a
goal that no requirement serves, is a finding.

On a spec or an ADR, the signed-off PRD settled the premise. Do not ask the four questions there.
Report only a simpler path to the same outcome, or a work entry that serves no requirement of the
PRD.

Do not report a detail of the build, security, design or scope.

### Design

**Signal:** the document names a screen, a page, a view or a component of an interface. Or it
names a user flow, a form, a button, navigation, a responsive layout or accessibility.

Find each design decision that the document leaves open, where the implementer must stop or guess.

- **Order on the screen.** What the user sees first, second and third, and how the user moves.
- **States.** Loading, empty, error, success and partial states for each element that the user acts
  on.
- **Flow.** Each entry point, the normal path with its decisions, two or three edge cases, and each
  exit.
- **Access.** Breakpoints, keyboard use, screen readers and touch targets.
- **Open decisions.** A placeholder, a vague phrase such as "a clean interface", or a function with
  no interaction, such as "the user can filter".
- **Generic interface.** A grid of three equal cards, a gradient hero or identical tiles for metrics
  of unequal weight, with no reason that comes from this product.

On a PRD, report only a gap that stops the spec from making a sound decision.

Do not report the backend, performance, security, strategy, or a visual preference.

### Security

**Signal:** the document touches authentication, authorization or sessions. Or it adds an endpoint
for an outside client, handles secrets, tokens, credentials, payments or personal data, or crosses a
trust boundary with a third party. Ordinary data and a schema change of data that is not sensitive
are no signal.

Find each security decision that the document needs and does not make.

- **Attack surface.** List each new endpoint, store, integration and input. Report only an element
  whose missing control a person can exploit in the design as written.
- **Access.** Each endpoint and each action names who may use it. "The system lets a user change
  settings" names no actor.
- **Exposure of data.** Sensitive data in transit, at rest, in logs and at deletion.
- **Third parties.** What the work trusts, how it stores and rotates their credentials, and what
  happens when one fails or lies.
- **Secrets.** Where each secret lives, who reads it, and how it stays out of source and logs.
- **Top three exploits.** The most likely, the worst and the least visible, with one mitigation
  each.

On a PRD, check that the PRD names the sensitive data and the trust boundaries. On a spec or an
ADR, check that each work entry builds the control that the PRD requires.

Do not report code quality, architecture outside security, or an attack with no path in this
design.

### Scope

**Signal:** every spec and every ADR. On a PRD: tiers of priority, more than eight requirements, a
section of future work, or a boundary that does not match the goals.

Ask two questions: is the work the right size for its goals, and does each abstraction earn its
cost?

- **What exists.** Code, a library or a tool that already solves part of the problem. Name the
  smallest change that delivers the outcome.
- **Size.** More than eight files or more than two new abstractions needs a goal of the same size.
- **Scope against goals.** An item that serves no goal, a goal that no item delivers, or a
  framework for a need nobody has yet.
- **One implementation behind an interface.** Ask what the generality buys today.
- **Mechanism size, in both directions.** A guard, a retry, a mode or an option that the request
  did not ask for earns its place in one of two cases. Leaving it out causes harm that nobody
  finds in time. Or adding it later costs much, because it touches stored data, a public interface,
  money or security. Report a mechanism that fails both tests. Report a deferred item that passes
  one.
- **A narrowed request.** A cap, a gate or a skip that delivers less than the source asked for.

On a spec or an ADR, the PRD settled the scope against its goals. Report new abstractions, extra
files, the order of the work entries, and work that the PRD put out of scope.

Do not report a contradiction about scope. Two passages that disagree are a coherence finding.

### Adversarial

**Signal:** any one of these:

- A PRD with two or more claims that a person can challenge: the framing, the choice of solution,
  the priority or a predicted outcome.
- A domain with high stakes: authentication, payments, migration of data, privacy, an outside
  integration or cryptography.
- A new abstraction, framework or architecture pattern.
- A spec or an ADR that goes past the scope of its PRD.
- A section of alternatives, or a tradeoff the document leaves open.

A spec that stays in the scope of its PRD and adds no abstraction and no domain with high stakes
shows no signal. A clear rationale is the document doing its job, and no signal.

Try to prove the document wrong. Build a counterargument, not a checklist.

- **Premise.** The goal says X, and the requirements solve Y. All the criteria can pass while the
  problem stays. Run this on a PRD only.
- **Assumptions.** A claim that depends on a condition the document never states: a service, a
  user habit, a scale or an order of events. Name the condition and the result when it is false.
- **Decisions.** For each large decision, name the evidence that proves it wrong, and the cost to
  reverse it. A decision that others depend on gets the most scrutiny.
- **Simpler path.** Name the smallest version that tests the approach. Run this on a PRD only.
- **Alternatives.** For each choice, ask why not the obvious other choice. Check that the document
  names a library or a tool that already solves the problem, and the cost of doing nothing.

On a spec or an ADR, keep assumptions about the build, decisions of the build and architecture
alternatives. Leave the premise and the product choices to the PRD.

Do not report a contradiction, feasibility, scope, design, security or product framing. The other
lenses own them.

## Design red flags

Read the red flags on a spec and an ADR only. A PRD gets no red flag, because a PRD names no
module design. A red flag is a reason to revise the shape of the design.

### Shallow module

A module shows a large interface and hides little behind it. A caller runs several methods to
finish one operation. Or an option shows an internal stage. Or the caller must learn the internals
to use the interface. A deep module puts much behavior behind a small interface. A deep call chain
is not a deep module.

### Information leakage

Two or more modules depend on one internal decision, so a change to it needs edits in each. A
public type of the wire or the transport is leakage. Parse outside data into domain types behind
the interface.

### Temporal decomposition

The modules follow the order of execution, not the knowledge each owns. Separate load, check,
transform and save stages often repeat one shape and its rules at each boundary. Group code by the
decisions it protects.

### Pass-through method

A method sends the same arguments on to another method of the same shape. It adds a layer and
hides nothing. Delete it, unless it adds a policy, an adaptation or its own abstraction.

### Split ownership

Two or more modules write the same state, or keep a copy of it. An agent that edits one writer
cannot see the others, so their rules drift apart. Give each piece of state one owner.

### Two ways to do one task

The design holds two ways to do one task. An agent copies the first way it finds, so each way
gets more callers. Keep one way, and delete the others in the same change.

### Importable internals

A caller can import the internals of a module. An agent takes the shortest path that compiles, so
the internals become part of the interface. Make an import from outside the module fail the
build.

### Hand-synced list

Two or more places list the same items, and a new item needs an edit in each list. An agent that
sees one list changes only that list. Derive each list from one list. When a list cannot be
derived, make the build fail when the lists differ.
