# Evidence: the v1 end-to-end run

U17 verification. The parity suite in `acceptance/` proves the acceptance
examples and the state tables on the real server and real beads, with the fake
agent host and synthetic GitHub. This file holds the operator's procedure for
the one live run that the suite cannot make: real agents, the real loop App on
a real repository, and the project's own merge mechanism.

The live run is an operator act. It spends harness quota and makes real pull
requests and merges. No agent can run it.

## What the suite proves, and what it leaves to this run

Run the suite with `make acceptance`. It needs the pinned `bd` 1.3.1 and `dolt`
2.2.0 (`LOOP_BD` and `LOOP_DOLT`, or `~/.cache/loop-dev/bin/`), and it skips
with a message when they are missing. The CI job `acceptance` provisions both
from the pins in `internal/release/provision.go` and checks their sha256.

The suite proves these with fakes in place of the agents and GitHub:

- AE1 to AE9, F1 and F4, in `acceptance/scenarios/`.
- Every row of the ticket and epic tables that a CLI verb, the reactor, the
  merge hand-off or the scheduler performs. `catalogue_test.go` fails a full
  run that leaves such a row unapplied. Two epic rows have no caller in v1:
  `create_architecture` and `adr_landed`. Machine unit tests apply them.
- A scripted agent that reads only `.loop/brief.json` and `loop schema --json`
  takes a ticket from claim to merge. It holds no GitHub credential and no
  push credential.
- The 20 findings of the escalation corpus in `acceptance/corpus/` each route
  to their labelled route through `loop escalation route`.
- A merge while a run holds the lease, a merge while the item is escalated,
  and a merge while a review room is open each leave one `done` writer.
- Each stage, each room kind and the coordinator start once through the fake
  host, and every command reference in the runbook each brief names resolves
  against the schema.

The live run must prove what the fakes stand in for: real agents follow the
runbooks, the real App pushes and merges, and the coordinator carries an
escalation and a fault between the loop and the operator in chat.

The CI job `acceptance` is not a required check. The operator decides whether
the default-branch ruleset of this repository requires it.

## Procedure

The operator does these steps. Record each result under Results with the
command or the page that shows it.

### Set up

1. Name a test repository. Install the loop App on that repository only. Set
   the default-branch ruleset that `docs/operating.md` names.
2. Commit `.loop/config.yaml` to the default branch. Name the merge mechanism
   the run proves, and enable these stages: `implementer`, `code-reviewer`,
   `doc-reviewer`, `amender`, `spec-author`, `decomposer` and `fixer`.
3. Run `loop install`, then `loop doctor`. Expect exit 0.
4. Run `loop secret set GITHUB_APP_KEY --project _host` with the App's key on
   stdin.
5. Run `loop project add <id> --repo <owner/name> --path <clone>`. The
   coordinator session starts at the next tick. A restart is not necessary.
6. Open the coordinator's chat.

### One epic from intake to done

1. In chat, ask the coordinator to start new work. Expect it to run
   `loop room open --kind intake`.
2. In the room, write a short PRD with two story labels, `US-01` and `US-02`.
   Let the room agent run `loop prd submit`. Record the epic id and the PR.
3. Let the doc-reviewer stage review the PRD. If it rejects twice, the epic
   waits in the inbox. Answer it through the coordinator.
4. When the PRD reaches `prd_signoff`, merge its PR on GitHub. Expect the epic
   to reach `spec_needed` within one reactor tick.
5. Let the spec-author, the doc-reviewer and the operator's merge take the
   spec to `spec_approved`.
6. Let the decomposer run `loop epic decompose`. Expect the epic in
   `execution`, and tickets that cite `US-01` and `US-02`.
7. Let the implementer and the code-reviewer take each ticket to
   `ready_to_merge`. Let the merge mechanism merge each one. Expect each ticket
   in `done`, and then the epic in `done`.

### One escalation through the coordinator

1. Give one ticket a task that needs a fact only the operator holds, for
   example a value no repository document records.
2. Expect the run to raise it with `loop escalation raise` at level 4 or 5,
   and the fixer to route it to a person.
3. Expect the coordinator to ask in chat. Answer in chat. Expect the
   coordinator to run `loop inbox answer`, and the ticket to return to `todo`.

### One loop fault and its resolution

1. Use the first fault in the loop that a run meets during this procedure. Do
   not invent one. If no run meets a fault, record that, and use the first
   fault of a later run.
2. Expect the run to write a report and run `loop fault report --file`. Expect
   one fault entry in the coordinator's inbox.
3. Fix the fault in a release. Run `loop upgrade <version>`.
4. Let the coordinator run `loop fault resolve <F-n> --fixed-in <version>`.
   Expect `loop fault show <F-n>` to read `fixed`, with the release this host
   runs.

## Results

PENDING — operator run. Nothing below is recorded yet. Do not fill it from the
acceptance suite or from a fixture.

Record each item with the command or the page that shows it.

- The date, the loop release, the repository (owner/name) and the merge
  mechanism.
- The `loop doctor` answer and the `loop project add` answer.
- The epic id, its PRD PR and its spec PR, and `loop work show` of the epic at
  `done`.
- Each ticket id, its PR, its reviewed head and its merge commit.
- The `transitions` rows of each item. Each item has exactly one row with
  `to_status` `done`.
- The escalation id, its panel route, the coordinator's question in chat, the
  answer, and the item's status after the answer.
- The fault id, the report, the release that fixed it, and the
  `loop fault show` answer after the resolution.
- Any step that did not behave as this file states, with the answer the loop
  or GitHub gave.
