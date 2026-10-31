---
name: loop-sdlc-glossary
description: One definition for each word the loop's runbooks share. Roles, documents, loop states and review terms. Read this before any runbook of the loop.
---

# loop-sdlc-glossary

Each word below has one meaning in every runbook of the loop. A runbook defines only the words that
belong to it alone. No person is present to say what a word meant. Two runbooks that define one
word differently make two agents that act differently, and no gate compares them.

## People and agents

- **The operator**: the person who runs the loop on a host. The operator talks to the coordinator
  and in rooms. Only the operator decides what the loop cannot decide.
- **A person**: any human. Write "a person", not "the user" or "the human".
- **The coordinator**: the one resident agent of an instance. It is the operator's interface. It
  reads the inbox, brings each question to the operator, records each answer and opens rooms.
- **A run**: one agent session that the server starts for one stage and one item.
- **A room**: an agent session where the operator talks to an agent. Its kinds are brainstorm,
  decision, review and architect. A room prepares a decision and applies it. A room never makes it.
- **The author**: in a review, the run or the person who wrote the work.

## Phases

An epic passes these phases in this order. A phase is a step of the loop. One or more stages or
rooms do the work of each phase. A bug's shaped ticket takes the build phase, and a closed bug
takes the retro phase.

1. **Ideate** (optional): the operator shapes a raw idea before a PRD exists. The loop has no room
   for this phase.
2. **Brainstorm**: an unattended brainstorm or a brainstorm room writes the PRD.
3. **Doc review**: the doc-reviewer reviews the PRD, and the reviser answers its findings. The
   operator signs it off.
4. **ADR** (optional): an architect room writes the ADRs of an epic that needs them, and the
   doc-reviewer reviews each ADR pull request.
5. **Plan**: the planner writes the spec.
6. **Doc review**: the doc-reviewer reviews the spec, and the reviser answers a person's comment
   on it. The operator signs it off.
7. **Decompose**: the decomposer cuts the approved spec into tickets.
8. **Build**: the implementer builds each ticket, and the code-reviewer reviews it.
9. **Retro**: the retro stage learns from the finished unit of work.

## Stages

Name the stage the first time a runbook mentions a role. Use the short form after that.

| Stage | What it does | Item states it takes |
| --- | --- | --- |
| `implementer` | Builds one ticket in one pull request, and repairs a red build | ticket `todo`, `repair` |
| `code-reviewer` | Reviews the pull request of one ticket | ticket `in_review` |
| `unattended-brainstorm` | Writes the PRD of one epic from the operator's request | epic `prd_needed` |
| `doc-reviewer` | Reviews one PRD, one spec or one ADR pull request | epic `prd_review`, `spec_review`, `adr_review` |
| `reviser` | Answers the findings on a PRD after its first reject, and a person's comment on a PRD or a spec | epic `prd_amending`, `spec_amending` |
| `planner` | Writes the spec of one epic whose PRD merged | epic `spec_needed` |
| `decomposer` | Cuts one approved spec into tickets in one batch | epic `spec_approved` |
| `triage` | Applies the triage rubric to each escalation, and routes each finding to a person or back to the loop | escalations that wait for a panel |
| `reproducer` | Turns one bug report into a failing test and a shaped ticket | bug `reported` |
| `retro` | Learns from one epic that reached `done` or one bug that closed, and routes each learning | retros that wait for a run |

The server performs every other move. It reacts to GitHub: a red build moves a ticket to `repair`,
a new head after a pass returns it to `in_review`, and a merge moves it to `done`. A person's
unanswered comment on a PRD or a spec in review or in sign-off moves its epic to the reviser. It hands each
eligible pull request to the project's merge mechanism. No agent merges.

## Loop states

- **Ticket**: `backlog`, `todo`, `in_progress`, `in_review`, `repair`, `ready_to_merge`, `queued`,
  `escalated`, `done`, `canceled`.
- **Epic**: `backlog`, `prd_needed`, `prd_drafting`, `prd_review`, `prd_amending`, `prd_signoff`,
  `architecture`, `adr_review`, `spec_needed`, `spec_drafting`, `spec_review`, `spec_signoff`,
  `spec_amending`, `spec_approved`, `decomposing`, `execution`, `escalated`, `done`. An epic in
  `backlog` waits on a blocker. No stage claims it. An epic in `adr_review` waits in the ADR stage
  while the doc-reviewer reviews an open pull request that adds an ADR for it. The verdict returns
  it to `architecture`.
- **Bug**: `reported`, `reproducing`, `asked`, `awaiting_build`, `escalated`, `shaped`, `duplicate`,
  `canceled`. A bug in `awaiting_build` waits for the ticket or the epic that builds what it asks
  for. No stage claims it.
- **Decision**: `pending`, `needs_human`, `done`, `canceled`.

`loop schema` prints every state and every move. A run never names a raw state in a write. It
names a verb, and the transition table picks the state.

## Documents

- **PRD**: states what a user can do, as user stories with stable labels such as US-01. It names
  no implementation. Either an unattended brainstorm or a brainstorm room writes it. The unattended
  brainstorm writes it from the operator's request. A brainstorm room writes it with the operator.
- **Spec**: states how one epic is built. It is the last document a person reads before the
  decomposer cuts tickets.
- **Sign-off**: the operator's approval of a PRD or a spec pull request at one head. The
  coordinator records it, and the server merges the document. No stage merges a document.
- **Project policy**: the rules of one project for one role, in `.loop/policy/<role>.md` in the
  project's repository. The loop's runbooks name roles. The policy names the project's tools.

## Work

- **Item**: one record that the server holds: a ticket, an epic, a bug or a decision. A spec's
  list of work entries is not a list of items.
- **Work kinds**: `epic`, `bug` and `ticket`. A decision is an item, but no work kind.
  `loop work list` lists no decision, and `loop decision list` lists them.
- **Unit of work**: an epic or a bug. The loop counts its work in units, and each finished unit
  gets one retro.
- **Epic**: one slice of the product, and one unit of work. It owns a PRD, a spec and its tickets.
- **Bug**: one fault that a person reported, and one unit of work. The reproducer turns it into a
  failing test and one shaped ticket.
- **Ticket**: one vertical slice of a unit of work. It crosses every layer it touches, and it lands
  and reverts alone. A ticket is never a unit by itself. The implementer and the code-reviewer read
  it.
- **Contract**: a numbered promise that can fail, such as C1. The spec holds its text. A ticket
  names the contracts it proves.
- **Proof**: what shows that a contract holds. It fails before the change. A test is the strong
  proof, because it runs again after every merge. A command is the weak proof.
- **Blocker**: a dependency. A ticket or an epic with an open blocker waits in `backlog`.
- **Loop fault**: a fault in the loop itself, not in the project. Its id, such as F-1, never
  changes.
- **Attempt**: one run on an item. The brief names the attempt number. Two failed attempts with
  unchanged inputs stop the item.

## Inbox

The inbox holds each question that waits on a person. Two kinds of hold share one word, so each
has a name of its own.

- **Decision**: a question that no ticket holds. It is an item with a record of its own. Every
  decision names its trigger, the event that forces the answer. A pending decision waits for its
  trigger, and no inbox shows it.
- **Inbox hold**: a decision whose trigger fired, in state `needs_human`. It waits on the operator
  in the inbox, under the inbox kind `hold` and the entry id `hold:<project>:<item>`.
- **Hold on an item**: a room or an escalation that keeps every stage off one item.
  `loop work show` lists it under `wait.holds`. It never ends a running lease. An inbox hold is no
  hold on an item.
- **Escalation**: a finding scored at level 4 or 5, or answered `outside`, that waits on a person
  in the inbox. Levels 1 to 3 stay in the loop.
- **Triage rubric**: the five levels and the two-option gate in `../loop-sdlc-triage/SKILL.md`.
  Two judges score each finding against it. The triage stage is the run that applies it to each
  escalation.

## Retro

- **Retro**: the step that learns from one finished unit of work. The `retro` stage runs it, and
  routes each learning through two judges. It is the last phase.
- **Epic done**: `epic.done`, the event of an epic that reaches `done`. It starts the epic's retro.
- **Bug closed**: `bug.closed`, the event of a bug that closes. It starts the bug's retro.
- **Closed bug**: the operator's rule for when a bug closes. A shaped bug closes when its shaped
  ticket reaches `done`. A canceled bug closes at once. A duplicate bug never closes, because the
  retro of its original covers it. The rule reads the state of the shaped ticket, because a bug
  reaches `shaped` before its ticket is built.

## Settings

- **A setting**: one value in the `stages`, `pools` or `merge` section of a project's
  `.loop/config.yaml`, such as `stages.implementer.cap`. A setting that the project does not set
  takes the host config value or the default. `loop settings show` lists each setting in force,
  with its source.
- **A settings change**: one setting changed by `loop settings set`, with a reason. Only the
  coordinator runs it. The server writes the change on its own pull request, and the project's
  merge mechanism merges it after CI. The merge puts a notice in the inbox. A stage run never
  changes a setting, and the server refuses its push with `SETTINGS_CHANGE`.

## Overwatch

- **Overwatch**: the server's native job that sweeps on a timer, and rings the coordinator when the
  sweep holds work or a failed read. It reads, and it never writes. The coordinator acts.
- **Sweep**: one read of every part at once. `loop overwatch sweep` runs one, and it rings nobody.
  Its report's code is 0 when no part holds work, 1 when a part holds an unfiled finding, and 2
  when a read failed.
- **Part**: one read of the sweep, such as `stalls` or `main`. It returns its findings and its
  notes. A note holds no work. The coordinator runbook's part table names the section that acts on
  each part.
- **Finding key**: the key of one finding, `overwatch:<part>:<subject>`, or a loop fault's id for
  the `bugs` part. A finding is work until an open bug, decision, fault or prepared question quotes
  its key.

## Review

- **Round**: one attempt of the author and one review of it. `loop review rounds <item>` lists
  them.
- **Last reviewed head**: the head that the latest earlier round reviewed. A later round judges the
  diff since that head.
- **Named finding**: a finding that an earlier round wrote. A later round judges the named findings
  and the new diff, and nothing else.
- **Gate**: a check that `loop gates` runs. The loop ships its own gates, and the project's
  `.loop/config.yaml` names a command for each stack gate. A run starts them on its own host.
- **Check**: a result that CI reports on a pull request. A missing check counts as a failed check.
- **Eligible**: a pull request with a pass at its current head and a green build. Only an eligible
  pull request merges.
