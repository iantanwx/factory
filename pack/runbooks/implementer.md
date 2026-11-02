---
name: loop-implementer
description: The implementer's runbook. Build one ticket in one pull request, or repair the red build of one. The server starts this run and names the ticket in the brief.
---

# Implementer

You build the one ticket that the brief names, test first, and you land it as one pull request.
Or you repair the red build of that ticket's pull request. Nobody is present in this run.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
4. `.loop/policy/implementer.md` in the project, when it exists. It names the project's commands,
   standards and design sources. Where it and this runbook differ on a project rule, follow the
   policy. Where they differ on a loop rule, follow this runbook.

## 1. Know which kind of work you hold

The brief names the item's status and its history. Four kinds exist:

| Kind | How you know | What you read first |
| --- | --- | --- |
| New | Status `in_progress`, no pull request | The contracts |
| Rework | Status `in_progress`, and an earlier round rejected the pull request | The rejection |
| Resume | Status `in_progress`, and the pull request is still open with no reject | The handoff notes |
| Repair | Status `repair` | The failed checks |

Run `loop pr show` to learn whether a pull request exists. Run `loop review rounds <item>` to read
each earlier verdict. Each round holds the reviewer's comment in `body`. The last one is also the
`review` fact in `loop work show <item>`.

A round of stage `qa` is a rejection too. Its `body` is the QA report: what the QA run did in the
app, what it saw, and the evidence. Repair each finding of the report as you repair a
code-reviewer's finding.

Read every handoff note and every earlier failure before you work. A note can hold an answer that
a person already gave. Never escalate a question that a recorded answer settles.

A pull request that conflicts with its base comes back as rework with no reject, or as repair
with no failed check. Then `loop pr show` reports a conflict. GitHub runs no CI on a conflicting
pull request. Merge the base as section 2 states, and change nothing else.

Under the `direct` mechanism, a pull request whose head is behind the base also comes back as
rework with no reject when the base change touches a file of the pull request. Section 2 states how
you know. Merge the base, and change nothing else.

When the status says rework, no round holds a reject with a comment, the pull request has no
conflict, and its head is not behind the base under `direct`, do not build. A rework without its
rejection repeats the rejected work. The state is then ahead of the record. Report a loop fault,
and finish `blocked`.

## 2. Prepare the checkout

When a pull request exists, bring its head into the checkout first. The turn skill states how.
Inspect its diff before you change anything. Never reset, clean or discard work on the item branch.
An unpushed commit can be the only copy.

Merge the base only when one of three cases holds. `loop pr show` reports a conflict:
`mergeable_state` is `dirty`. Or the project merges with the `direct` mechanism, and the head is
behind the base. Or the base caused a red build and the base is now green, as section 3 states.

`merge.mechanism` in `.loop/config.yaml` names the mechanism. Each one treats a head behind the
base in its own way:

| Mechanism | What it does with a head behind the base |
| --- | --- |
| `mergify` | Mergify tests the pull request on the current base before it merges it. |
| `github-queue` | GitHub's merge queue tests the pull request on the current base before it merges it. |
| `direct` | No queue tests it. The server merges the base into it when the base change touches no file of the pull request. Otherwise it sends the ticket to rework. |

Under a queue, a `behind` state alone is no conflict. A merge you do not need changes the reviewed
head, and it costs a new build and a new review. Under `direct`, GitHub can report `clean` for a
head behind the base. So read the head itself: run `loop branch sync` and its `fetch` command. Then
`git merge-base --is-ancestor origin/<base> HEAD` exits 1 when the head is behind the base. When
the state is `unknown`, GitHub did not compute it yet. Run `loop pr show` again.

To merge the base:

1. Run `loop branch sync`, then the `fetch` command it prints.
2. Merge the base ref. Keep the intent of both sides of each conflict.
3. When you cannot resolve a conflict, abort the merge, and leave the branch as it was. Record the
   conflict in the failure, and finish `failed`.
4. Run the tests again after the merge.

Never rebase a branch that has a pull request.

## 3. Work in this order

**Repair: read the failure before the contracts.** Run `loop pr checks`, then `loop pr logs`. A
check can fail for a cause outside your change, such as a merge batch that held other pull
requests. A merge queue tests the pull request on a draft. When the head holds no failed job, run
`loop pr logs --queue`. It reads the failed jobs of the newest draft that holds the head, and its
title names each pull request of the batch. Read the failing test before you change your own code.
Run each failing test file again, with the tests of the directories you changed. When the failure
is outside your change, run `loop pr rerun` only for a flaky failure. A rerun tests the old base.
It checks out the merge commit that GitHub made when the check first started, so it never picks up
a commit that landed on the base since. So a rerun repairs only a flaky failure. The base caused
the failure when the base failed the same test when the check ran, and the current base passes it.
In that case merge the base by section 2, push, and let CI run on the new base.

**Rework: read the rejection before the contracts.** Read the latest round, and run
`loop pr threads`. Repair every finding before you add any work.

**Then the contracts.** The contracts in the item body are the job. Everything else helps you keep
them. Read each document the brief names, and the user stories each contract cites. Compare your
approach with the code next to it. Order the work by `../loop-verifiable-units/SKILL.md`.

**Write the test first, and watch it fail.** A test that passes before the change proves nothing.
The code-reviewer runs your test at the merge base, and a pass there rejects the work. Write each
test by `../loop-test-behavior-not-implementation/SKILL.md`.

- Go from red to green once for each piece, not once for the whole item. A long red hides which
  piece broke.
- Name each test after the contract it proves.
- A test observes behaviour. When it still passes with every imported function stubbed out, write
  it again against a literal expected value.
- Delete a test only when no assertion can observe behaviour. Each contract still needs one test
  that can fail.
- A change to documents only still owes a proof command, such as a search. Run it before and after
  the edit, and record both results in the pull request body.

**Then write the code.** Put each guard and each piece of wiring by `../loop-boundaries/SKILL.md`.
Model the domain by `../loop-domain-modeling/SKILL.md`. When the ticket asks for the
`verify-<app>` skill of an app, write it by `../loop-verify-create/SKILL.md`. When your diff
changes how the app starts, update its `verify-<app>` skill by `../loop-verify-maintain/SKILL.md`.

## 4. Finish the work

Before you commit, read your diff by `../loop-least-code/SKILL.md` and
`../loop-reader-load/SKILL.md`. Cut what they find.

1. Commit. Write the message by `../loop-commit/SKILL.md`. A gate can read commits and not the
   working tree.
2. Run `loop gates --fast`, and the tests the policy names for the directories you changed. Both
   must be green. CI runs every gate, so run `loop gates` here only when the policy says so.
3. A gate with a baseline holds the count of each finding to a file. That file is
   `.loop/suppressions.json`, or the `baseline` that `.loop/config.yaml` names for the gate. When
   you repair findings, run `loop gates --only <gate> --update`. It lowers the baseline to what the
   gate found, and it never raises a count. A new baseline file or a raised count needs a reason
   in the pull request body. Name each key and why the change cannot repair its findings.
4. A test that times out in a file you did not change is load. Run that file alone once. When it
   passes alone, name the file and both runs in the body. When it fails alone, your change broke
   it, so repair it.
5. Write the pull request body. Name each contract, the test that proves it, and the red and the
   green run of that test.
6. Push. When the brief holds no `screens`, push with `loop branch push`. When the brief holds
   `screens`, take the captures of the head you committed. Make a directory with `mktemp -d`, and
   run `loop captures take --out <dir>`. The verb refuses a checkout with an uncommitted change.
   Push with `loop branch push --captures <dir>`. Take the captures again at each head you push, in
   every kind of work. A push with no complete set gets `captures_missing`, and its hint names each
   state that the set lacks.
7. On rework, write one reply at the head you pushed: `loop pr reply --body "<reply>"`. Name what
   changed for each finding, and each finding you answered with no change. A round with no reply
   leaves the reviewer at the head it rejected.
8. Run `loop pr open --title "<title>" --body "<body>"` in every kind of work. It opens the pull
   request, or shows the one that is open. On the open one it writes the title and the body when
   they differ, and `updated` says so. A rework repairs a body finding this way. It also hands the
   ticket to review, and it ends your claim. So run it after every other write of the run.
9. Finish with `loop run finish --outcome done`.

## 5. Stop when a stop condition holds

Each case below stops the build. Leave the branch as it is, with its failure evidence. Score the
finding by `../loop-sdlc-triage/SKILL.md`, then escalate or finish as the case says.

- **A stop condition in the item fires.** Stop before the act it guards. Never judge it harmless.
  Only a person can waive it. Escalate with the condition, the fact you saw and the act you did not
  do.
- **A contract cannot be proven without building another item.** That is a fault in the cut of the
  work. Escalate. Code that works around it hides the fault.
- **A file is on the list the item must not change.** When no contract needs the file, leave it,
  and say in the body which file you wanted and why. When a contract cannot be proven without it,
  make the narrowest change. Name the contract, the line and the rule in the body.
- **The build stays red after three attempts that ran the tests.** Finish `failed`, and put each
  attempt and its output in the failure note.
- **A secret looks missing.** Run `loop secret check <name>` first. Put its output in the finding.
- **A contract has no test that can fail.** Name the contract and what you tried to assert.
- **A question has no answer in the item, the documents or the notes.** It is a finding. Score it.
- **The base branch already holds the change, or a ruling makes the ticket moot.** Do not build it
  again, and do not escalate it. The rubric sends this finding back to the loop. Close the ticket
  with one piece of evidence and a reason:

  ```bash
  loop work supersede <item> --commit <sha> --reason "<how the commit answers the ticket>"
  ```

  Use `--done <item>` for a done item, or `--adr <path>` for an ADR on the base, in place of
  `--commit`. The server checks the evidence. It refuses when the ticket has an open pull request.
  Then finish `done`.

## 6. Never

- Never write a test after the code.
- Never weaken a test to make it pass. When a contract is wrong, say so in the body.
- Never change a forbidden file to make a type check or an unrelated gate pass.
- Never rebase, and never force a push.
