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
- **A room**: an agent session where the operator talks to an agent. Its kinds are intake,
  decision, review and architect. A room prepares a decision and applies it. A room never makes it.
- **The author**: in a review, the run or the person who wrote the work.

## Stages

Name the stage the first time a runbook mentions a role. Use the short form after that.

| Stage | What it does | Item states it takes |
| --- | --- | --- |
| `implementer` | Builds one ticket in one pull request, and repairs a red build | ticket `todo`, `repair` |
| `code-reviewer` | Reviews the pull request of one ticket | ticket `in_review` |
| `prd-author` | Writes the PRD of one epic from the operator's request | epic `prd_needed` |
| `doc-reviewer` | Reviews one PRD or one spec | epic `prd_review`, `spec_review` |
| `amender` | Answers the findings on a PRD after its first reject, and a person's comment on a PRD or a spec | epic `prd_amending`, `spec_amending` |
| `spec-author` | Writes the spec of one epic whose PRD merged | epic `spec_needed` |
| `decomposer` | Cuts one approved spec into tickets in one batch | epic `spec_approved` |
| `fixer` | Routes each escalation through the triage panel | escalations that wait for a panel |
| `reproducer` | Turns one bug report into a failing test and a shaped ticket | bug `reported` |
| `retro` | Learns from one epic that reached `done` or one bug that closed, and routes each learning | retros that wait for a run |

The server performs every other move. It reacts to GitHub: a red build moves a ticket to `repair`,
a new head after a pass returns it to `in_review`, and a merge moves it to `done`. A person's
unanswered comment on a PRD or a spec in review or in sign-off moves its epic to the amender. It hands each
eligible pull request to the project's merge mechanism. No agent merges.

## Loop states

- **Ticket**: `backlog`, `todo`, `in_progress`, `in_review`, `repair`, `ready_to_merge`, `queued`,
  `escalated`, `done`, `canceled`.
- **Epic**: `backlog`, `prd_needed`, `prd_drafting`, `prd_review`, `prd_amending`, `prd_signoff`,
  `architecture`, `spec_needed`, `spec_drafting`, `spec_review`, `spec_signoff`, `spec_amending`,
  `spec_approved`, `decomposing`, `execution`, `escalated`, `done`. An epic in `backlog` waits on
  a blocker. No stage claims it.
- **Bug**: `reported`, `reproducing`, `asked`, `awaiting_build`, `escalated`, `shaped`, `duplicate`,
  `canceled`. A bug in `awaiting_build` waits for the ticket or the epic that builds what it asks
  for. No stage claims it.
- **Decision**: `pending`, `needs_human`, `done`, `canceled`.

`loop schema` prints every state and every move. A run never names a raw state in a write. It
names a verb, and the transition table picks the state.

## Documents

- **PRD**: states what a user can do, as user stories with stable labels such as US-01. It names
  no implementation. Either a PRD author or an intake room writes it. The PRD author writes it from
  the operator's request. An intake room writes it with the operator.
- **Spec**: states how one epic is built. It is the last document a person reads before the
  decomposer cuts tickets.
- **Ticket**: the unit that the implementer and the code-reviewer read.
- **Sign-off**: the operator's approval of a PRD or a spec pull request at one head. The
  coordinator records it, and the server merges the document. No stage merges a document.
- **Project policy**: the rules of one project for one role, in `.loop/policy/<role>.md` in the
  project's repository. The loop's runbooks name roles. The policy names the project's tools.

## Work

- **Item**: one record of work that the server holds: a ticket, an epic, a bug or a decision. A
  spec's list of work entries is not a list of items.
- **Epic**: one slice of the product. It owns a PRD, a spec and its tickets.
- **Slice**: one ticket that crosses every layer it touches, and lands and reverts alone.
- **Contract**: a numbered promise that can fail, such as C1. The spec holds its text. A ticket
  names the contracts it proves.
- **Proof**: what shows that a contract holds. It fails before the change. A test is the strong
  proof, because it runs again after every merge. A command is the weak proof.
- **Blocker**: a dependency. A ticket or an epic with an open blocker waits in `backlog`.
- **Decision**: a question that no ticket holds. Every decision names its trigger, the event that
  forces the answer.
- **Escalation**: a finding scored at level 4 or 5, or answered `outside`, that waits on a person
  in the inbox. Levels 1 to 3 stay in the loop.
- **Loop fault**: a fault in the loop itself, not in the project. Its id, such as F-1, never
  changes.
- **Attempt**: one run on an item. The brief names the attempt number. Two failed attempts with
  unchanged inputs stop the item.

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
