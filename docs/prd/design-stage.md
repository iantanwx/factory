# Give a UI epic a design stage between its PRD and its spec

## Stories

The operator asked for this epic in an intake room, room-0fefd19b. The coordinator opened the room
with a starting proposal, and the operator chose each part of the design in the room. Each story
below quotes the operator's words from that room. The text under each story states the choice that
the operator made.

Today an epic goes PRD, then an optional ADR, then spec (`docs/prd/optional-adr-stage.md` § US-02).
No stage holds a picture of a screen. The operator plans to design the screens of the project
grinch in paper.design, and grinch has no work yet. The operator named three problems from past
projects. Tickets build the wrong screens, the screens of different tickets look and act
differently, and nothing checks the shipped screens against what the operator wanted.

> "all of the above problems apply, from past experience."
>
> "it's easier to show, not tell."

This epic holds the design stage and its signed record. A sibling epic holds the check of the
shipped screens against that record. Section Boundaries with other epics names the seam.

### US-01 Each story of a UI project says whether it touches the UI

As the operator, I can mark each story of a PRD as a story that touches the UI or as one that does
not, so that the loop knows which stories must have a screen.

> "option 1 is better and far more explicit."

The operator chose a line under each story, `**UI:** yes` or `**UI:** no`, which the intake room
writes with the operator. The operator rejected two other ways. The design agent does not decide
which stories need a screen, and a project does not give every story a screen.

### US-02 A UI epic goes to a design stage after its PRD and before its spec

As the operator, I can rely on the loop to send each epic of a UI project that holds a UI story to
a design stage, so that the screens exist before the spec.

> "option 1"

The operator chose this flow. A project declares that it has a UI. In that project, an epic with at
least one `UI: yes` story goes to the design stage after its PRD sign-off. When the epic also goes
through the ADR stage, the design stage comes after the ADR. An ADR can then choose the stack
before the agent builds the prototype in it. An epic with no `UI: yes` story goes to the spec
stage as it does today. Only the operator can skip the design stage for one epic, and the loop
records the operator's words on the epic. The operator rejected a design stage that only the
operator starts, and a design stage that nobody can skip.

### US-03 A design room builds a prototype first, and the operator edits it on a canvas

As the operator, I can work in a design room where an agent builds a prototype of each screen from
the stories, and where I then edit the screens on a design canvas, so that I do not start the canvas
from nothing.

> "we could prototype first, before moving to paper. it's likely to be easier that way, becuase i
> won't have to prompt in paper from scratch."
>
> "all machines are connected via tailscale, so we may not even need a tunnel. i can run paper on
> the client machine."
>
> "other than that, option 1 looks good to me."
>
> "i prefer option 2"

The operator chose a design room, which the operator and an agent share, and in which the agent
builds first. The agent builds the prototype in the project's real stack. The operator rejected
plain HTML files outside the stack, and a page outside the repository. The agent imports each
screen into the canvas, the operator edits it there, and the agent reads the edits back.

> "i choose option 1."

The operator chose that the agent ports each canvas edit back into the prototype before sign-off.
The prototype is then the one source of truth, and each picture of the record comes from it. The
operator rejected a canvas that wins over a prototype that stops changing at the import, and a
canvas used only for comments.

### US-04 The prototype stays on a branch of its own, which never merges

As the operator, I can reach the prototype from any later stage on a branch that never merges, so
that its code never reaches the product and never misleads a later agent.

> "i don't like the idea of option 3, because there's a good chance of it poisoning context of a
> less capable model. branches are cheap to push, and can be reached again from any stage, as long
> as they're not deleted on remote. for that reason, i'm leaning toward option 1."

The operator rejected a prototype that merges into the product, and a prototype that merges into a
directory of the repository that never ships. The record on the base branch names the prototype's
branch and the commit that the operator signed off.

### US-05 The design record maps each UI story to a screen, and a gate refuses a gap

As the operator, I can read one record of each epic's screens, in which each UI story has a screen
and each screen state has a picture, so that later stages read the screens I signed off.

> "option 1 is good. in particular, a US that touches the UI *must* have a corresponding screen, as
> a hard gate."

The operator chose a record of three parts in the epic's design directory. A screen list names
each screen and each of its states, and each screen cites the stories it serves. Each state has one
picture. A pin line names the prototype's branch and commit. The operator rejected a project-wide
design file of tokens and components for this epic, and an export of canvas code on the base
branch.

> "i'd go with option 1 with an optional human initiated pixel diff where high fidelity is
> paramount."
>
> "option 1"

The operator chose to mark a screen state for a pixel comparison in the screen list, at design
time. This epic holds the mark and its threshold in the record. The sibling epic runs the
comparison.

### US-06 The doc-reviewer reviews the design record, and the operator signs it off

As the operator, I can rely on the doc-reviewer to review each design record before I approve it,
so that a state that the stories need and that nobody drew is caught before the spec.

> "option 1"

The operator chose a design pull request, a doc review, and then the operator's approval as the
sign-off. The server then merges it, as it merges a PRD, an ADR or a spec. The reviewer checks that
each acceptance criterion of a UI story that a user can see has a state in the record. A criterion
that says the product must refuse something needs a state that shows the refusal. The operator
rejected a sign-off with no doc review, and a sign-off in the room with no pull request approval.

### US-07 The loop stays free of any one design tool

As the operator, I can name the design canvas of each project in the project's config, so that the
loop works with paper.design and with any other canvas, or with none.

> "option 1. the kernel stays tool-agnostic."

The operator chose a project setting that names a design skill for the canvas round. Paper is one
such skill. The design stage, the room, the record and its gates belong to the loop. With no design
skill named, the room builds the prototype and captures it with no canvas round. The operator
rejected Paper built into the room's runbook.

## Scope check

The operator agreed to split the design work into two epics. This epic holds the design stage and
its record. The sibling holds the check of the shipped screens against the record.

> "let's go with option 1."

US-01 is the story line and its gate. US-02 is the flow of an epic through the design stage. US-03
is the design room. US-04 is the prototype branch. US-05 is the record and its gate. US-06 is the
review and sign-off of the design pull request. US-07 is the project setting that names a design
skill.

**Stories:** 7
**Estimated total:** 2,600 lines
**Lands independently?** Yes. US-01, US-02 and US-07 wait for factorio-791i, which owns the `ui` block that marks a UI project. factorio-791i waits for factorio-yzrt. After factorio-791i lands, US-01 merges alone, because it changes the PRD template and the `prd` gate only. US-05 merges alone, because its gate reads only record files. US-07 merges alone, because a setting with no reader changes nothing. US-02 needs US-01's line, and still merges alone after it. US-04 merges alone, because a push to a branch that never merges needs no room. US-03 needs US-02, US-04 and US-07, and it merges last. US-06 merges after US-05.
**Promises provable alone?** Yes. The story line needs a PRD fixture. The flow needs the transition table and PRD fixtures with and without a UI story. The prototype branch needs one push fixture and a remote. The record gate needs record fixtures. The review needs one design pull request fixture. The setting needs a config fixture. The room needs a fixture project with a run command, and the canvas round needs a stub design skill, not Paper.
**Verdict:** proceed.

## Goal

In a project with a UI, the operator sees and edits each screen of an epic before its spec exists.
Each story that touches the UI has a screen, each screen state has a picture, and the operator signs
off the record. Later stages read that signed record and can run the prototype it names.

## Requirements

- In a project that declares a UI, each story of a PRD holds one line that says whether the story
  touches the UI, `yes` or `no`.
  → § US-01
- The `prd` gate refuses a PRD of a project that declares a UI when a story has no UI line, and the
  refusal names the story.
  → § US-01
- The `prd` gate asks for no UI line in a project that declares no UI.
  → § US-01
- The PRD template and the brainstorm skill tell the brainstorm room to set the UI line of each
  story with the operator.
  → § US-01
- A project declares a UI when its `.loop/config.yaml` holds a `ui` block. factorio-791i owns that
  block, and this epic adds no UI declaration of its own.
  → docs/prd/kernel-and-userspace-skills.md § Boundaries with other epics
- In a project that declares a UI, the merge of the PRD of an epic with a `UI: yes` story moves the
  epic to the design stage, and not to the plan stage.
  → § US-02
- An epic that goes through the ADR stage and holds a `UI: yes` story moves to the design stage when
  its ADR merges, and not to the plan stage.
  → § US-02
- An epic with no `UI: yes` story moves to the plan stage at the merge of its PRD, or of its ADR,
  as it does today.
  → § US-02
- The operator can skip the design stage for one epic through the coordinator, and the loop records
  the operator's words on the epic.
  → § US-02
- The loop refuses a skip of the design stage that holds no words of the operator.
  → § US-02
- The merge of an epic's design record moves the epic to the plan stage.
  → § US-02
- When an epic enters the design stage, the coordinator brings it to the operator and opens a
  design room for it.
  → § US-03
- The design room builds a prototype of each screen of the epic's UI stories in the project's own
  stack, before any canvas round.
  → § US-03
- The project's config states how the design room runs the prototype and captures one screen state
  as a picture.
  → § US-03
- When the project names a design skill, the design room imports each screen into that canvas, and
  the operator edits it there.
  → § US-03
- The design room ports each canvas edit back into the prototype before it opens the design pull
  request.
  → § US-03
- Each picture of the record is a capture of the prototype at the pinned commit.
  → § US-03
- The design room pushes the prototype to a branch of its own, apart from the branch of the design
  pull request.
  → § US-04
- The prototype's branch never merges, and the loop never deletes it.
  → § US-04
- The design pull request changes only files in the epic's design directory.
  → § US-04
- The record pins the prototype by its branch and by the commit that the operator signs off.
  → § US-04
- The record lives in the epic's design directory on the base branch.
  → § US-05
- The record's screen list names each screen and each state of each screen.
  → § US-05
- Each screen of the screen list cites at least one story label of the epic's PRD.
  → § US-05
- Each state of the screen list names one picture in the design directory.
  → § US-05
- A state of the screen list can carry a pixel mark, with a threshold or with the default
  threshold.
  → § US-05
- The design gate refuses a record in which a `UI: yes` story of the PRD has no screen, and the
  refusal names the story.
  → § US-05
- The design gate refuses a record in which a screen cites no story of the PRD.
  → § US-05
- The design gate refuses a record in which a state names no picture, or a picture that does not
  exist.
  → § US-05
- The design gate refuses a record with no pin line, or with a pin whose commit the named remote
  branch does not hold.
  → § US-05
- The design gate refuses a pixel mark whose threshold is not a number in the allowed range.
  → § US-05
- The doc-reviewer reviews each design pull request, and submits one verdict at the head it read.
  → § US-06
- The doc-reviewer rejects a record in which an acceptance criterion of a UI story that a user can
  see has no state, and the reject names the criterion.
  → § US-06
- The doc-reviewer rejects a record in which a criterion that says the product must refuse
  something has no state that shows the refusal.
  → § US-06
- The operator's approval of the design pull request is its sign-off, and the server merges it.
  → § US-06
- A project can name one design skill in its config for the canvas round.
  → § US-07
- With no design skill named, the design room builds and captures the prototype with no canvas
  round, and the record holds the same three parts.
  → § US-07
- No loop runbook, gate or state names paper.design or any other canvas.
  → § US-07

## Acceptance criteria

- **A1.** In a project that declares a UI, a PRD with a story that has no UI line fails the `prd`
  gate, and the refusal names the story.
- **A2.** In a project that declares no UI, a PRD whose stories have no UI line passes the `prd`
  gate.
- **A3.** In a project that declares a UI, an epic with a `UI: yes` story is in the design stage
  when its PRD merges, and not in the plan stage.
- **A4.** An epic that went through the ADR stage and holds a `UI: yes` story is in the design stage
  when its ADR merges.
- **A5.** An epic whose stories are all `UI: no` is in the plan stage when its PRD merges.
- **A6.** An epic that the operator skipped past the design stage is in the plan stage.
- **A7.** A skip of the design stage with no words of the operator changes nothing.
- **A8.** An epic in the design stage moves to the plan stage when its design record merges, and
  not before.
- **A9.** An epic that enters the design stage has a design room.
- **A10.** The prototype of a design room runs in the project's own stack, by the run command in
  the project's config.
- **A11.** With a design skill named, a canvas edit of the operator appears in the prototype at the
  pinned commit. A picture of the record that shows the screen before the edit makes this false.
- **A12.** With no design skill named, the design room opens a design pull request whose record
  passes the design gate.
- **A13.** The design pull request changes no file outside the epic's design directory. A prototype
  file in its diff makes this false.
- **A14.** The prototype's branch is on the remote after the design record merges, and after the
  epic closes.
- **A15.** A record whose pin names a commit that the remote branch does not hold fails the design
  gate.
- **A16.** A record in which a `UI: yes` story has no screen fails the design gate, and the refusal
  names the story.
- **A17.** A record with a screen that cites no story of the PRD fails the design gate.
- **A18.** A record with a state that names a missing picture fails the design gate, and the
  refusal names the state.
- **A19.** A record whose pixel mark holds a threshold outside the allowed range fails the design
  gate.
- **A20.** Each design pull request gets one doc-reviewer verdict at the head the reviewer read.
- **A21.** The doc-reviewer rejects a record that lacks a state for a visible acceptance criterion,
  and the reject names the criterion.
- **A22.** The doc-reviewer rejects a record that lacks a state showing a refusal that a criterion
  asks for.
- **A23.** A design pull request merges only after the operator's approval.
- **A24.** With a stub design skill named under a name that no loop file holds, the design room
  opens a design pull request whose record passes the design gate. A runbook, gate or state that
  waits for one named canvas makes this false.
- **A25.** An epic that the operator skipped past the design stage holds the operator's words.
- **A26.** The operator's inbox names the design room of an epic that enters the design stage.
- **A27.** In a fixture project whose capture command writes one fixed picture, each picture of
  the record is that picture. A picture that the capture command did not make falsifies this.
- **A28.** With a stub design skill named, the stub's first import comes after the prototype's
  branch holds a commit. An import that comes before that commit makes this false.
- **A29.** A record with a state that carries a pixel mark and no threshold passes the design gate.
- **A30.** A project whose `.loop/config.yaml` holds a `ui` block counts as a project that declares
  a UI in A1 to A5. A UI key of this epic's own in the config makes this false.
- **A31.** The prototype's branch is not the head branch of the design pull request. A head branch
  that holds a commit of the prototype's branch, and not of the base, makes this false.
- **A32.** A search of the loop's runbooks, gates and states for each canvas name of a list finds
  nothing. The list holds paper.design, Figma, Penpot and Framer. It grows by one name with each
  canvas that a project's design skill drives.

## Boundaries with other epics

- **The check of the shipped screens, the sibling epic.** It owns the citation of screen states in
  specs and tickets, the implementer's capture at a pull request head, the code-reviewer's
  comparison of structure, and the pixel comparison of a marked state. This epic owns the record's
  format, the pixel mark and its threshold, and the capture command in the project's config. The
  sibling reads all three and changes none. A room of its own writes its PRD, and no item holds it
  yet.
- **Conditional kernel skills, factorio-791i.** That epic owns the `ui` block of
  `.loop/config.yaml`, and the design skill that replaces `loop-impeccable` in a run. The
  operator chose "Kernel, loaded only with a UI", and the same answer split the conditional load
  into factorio-791i (`docs/prd/kernel-and-userspace-skills.md` § Stories). The amendment fact of
  factorio-yzrt records that the operator accepted all of parts a to g, and that factorio-791i's
  body holds them.
  The first draft of that PRD, commit 03e3068, holds the `ui` block and its design skill override.
  This epic reads "a project declares a UI" as "the project config holds a `ui` block", and adds
  no declaration of its own. Open questions hold whether the canvas skill of US-07 is that
  override's key or a second key in the block.
- **The optional ADR stage, factorio-ku99, `docs/prd/optional-adr-stage.md`.** It owns the
  `architecture` state and the ADR test. Its A7 and A8 move an epic to the plan stage at the merge
  of its ADR, or of its PRD. This epic changes the target of both moves to the design stage, for an
  epic with a `UI: yes` story in a project that declares a UI. Its A7 and A8 still hold for every
  other epic. factorio-ku99 is in spec sign-off and lands first. This epic's spec then changes the
  two moves that factorio-ku99 adds, and changes nothing else in them.
- **The vocabulary, factorio-vlle, `docs/prd/loop-vocabulary.md`.** That epic owns the names of
  roles and phases, and its glossary names nine phases only. This epic adds the design phase, and
  its doc review, to the glossary's list between ADR and plan. This PRD uses the new names. The
  plan stage is the stage in which the planner writes the spec, `spec_needed` today.
- **Kernel and userspace skills, factorio-yzrt and ADR 0013.** They own how a project declares a
  userspace skill, as a pin in the `skills` list of `.loop/config.yaml` or as a committed skill
  directory. The canvas skill that a project names is a userspace skill of that project, or a
  kernel skill. This epic adds no other way to hold a skill.
  → docs/adr/0013-userspace-skill-pins.md § Decision
- **Retro, factorio-1xq0.** Its PRD lists each new stage of another epic that claims an item. The
  design stage is one more such stage. Its runs produce records that a retro reads, and it owns no
  event trigger.
  → docs/prd/retro.md § Boundaries with other epics
- **The QA stage, factorio-nhb5.** That epic owns the QA stage and its visual QA.
- **Project-scoped settings, `docs/prd/project-scoped-settings.md`.** It owns the `stages`, `pools`
  and `merge` settings. This epic's UI declaration and design skill sit outside those three
  sections.
- **`loop-impeccable`, a third-party skill in the pack.** ADR 0007 owns how the pack holds it. This
  epic may name it as one design skill a project can choose, and changes nothing in it.
- **Checked against:** `docs/prd/optional-adr-stage.md`, `docs/prd/project-scoped-settings.md`,
  `docs/prd/kernel-and-userspace-skills.md`, `docs/prd/loop-vocabulary.md`, `docs/prd/retro.md`,
  ADR 0002, ADR 0005, ADR 0007 and ADR 0013. ADR 0002 rejected tickertown's QA-state, design and
  frame rules because each names tickertown's product paths. This epic's design gate reads the
  project's own design directory, and names no product path of any project. Four conflicts found.
  The optional ADR stage sends every epic to the plan stage, and the kernel and userspace skills
  PRD gives the `ui` block to factorio-791i. The vocabulary PRD lists nine phases, and the retro
  PRD does not name the design stage. This section settles each one.

## Out of scope

- The citation of screen states in specs and tickets, and every comparison of shipped screens with
  the record. The sibling epic owns them.
- A QA stage. factorio-nhb5 owns it, by the operator's choice of the code-reviewer as the checker.
- A project-wide design file of tokens, type and components. The operator chose a record without
  it, so the shared visual standard waits for a later epic.
- An export of canvas code onto the base branch. Never, by the operator's choice of record.
- A merge of prototype code into the product. Never, by US-04.
- A design skill for paper.design itself. It belongs to the project or the operator, as a
  userspace skill that the project declares by factorio-yzrt and ADR 0013.

## Open questions

- Paper's MCP server runs only inside Paper Desktop. Its HTTP endpoint listens on
  `127.0.0.1:29979`, so a Tailscale peer cannot reach it directly. Nobody tested a design room
  on the agent host against Paper Desktop on the client machine. One way is `tailscale serve` on
  the client. Another is a session on the client that runs `paper mcp`. A live test decides which
  way the Paper skill uses. Blocks: none of this epic's criteria, because A11 passes with a stub
  skill. It blocks the first grinch design room.
- Is the canvas skill of US-07 the same key as factorio-791i's design skill override, which
  replaces `loop-impeccable` in a run, or a second key in the `ui` block? The override's skill
  guides the agent's own design work, and the canvas skill runs the canvas round. Blocks: A11,
  A12, A24, A28.
- Which commit does the operator sign off when the prototype's branch moves after the design pull
  request opens? This PRD reads the pin line at the head the doc-reviewer read as the signed
  commit. Blocks: A15.
- What is the default threshold of a pixel mark, and its allowed range? The operator did not name
  numbers. Blocks: A19, A29.
- Does the design gate run in a project that declares a UI on an epic whose design stage the
  operator skipped? This PRD reads a skip as no record and no gate run. Blocks: A6, A25.
