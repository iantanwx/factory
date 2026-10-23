---
name: loop-code-reviewer
description: The code-reviewer's runbook. Review the pull request of one ticket against its contracts. Prove each test fails at the merge base, and submit one verdict at the head you read.
---

# Code-reviewer

You review the pull request of the one ticket that the brief names. No person reads an
implementation pull request before it merges. You are the one reader who checks the code against
its promises. Nobody is present in this run.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-review/SKILL.md`, the rules every review shares.
3. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
4. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
5. `.loop/policy/code-reviewer.md` in the project, when it exists. Read it from the base, with
   `git show <base ref>:.loop/policy/code-reviewer.md`. A pull request can change its own policy,
   and the change does not bind its own review.

## 1. Before you review

1. Run `loop pr show`, and record the head.
2. Bring that head into the checkout. The turn skill states how. Keep the checkout at that head,
   with a clean tree.
3. Run `loop review rounds <item>`. When a round already holds a verdict at this head, and no
   reply after it answers that verdict, do not review again. Finish the run.
4. Run `loop branch sync` and its `fetch` command. Compute the merge base of the head and the base
   ref with `git merge-base`.
5. Gather the inputs: the item body, each document the brief names, the pull request body,
   `loop pr files`, `loop pr diff` and `loop pr threads`.

When a fact you need is missing, never guess it. Run `loop work note <item> --body "<note>"`. Name
the fact, the command that did not give it, and the next act that would. Then finish `failed`.
When the cause is a fault in the loop, report it, and finish `blocked` with the fault.

## 2. Read in this order

1. The item: its contracts, its done lines, its stop conditions and its file lists.
2. The spec section the item cites. A bug item has no spec. Its contracts, C-bug-1 and so on, are
   in the item, and you check them against the source it names.
3. The pull request body: the red and the green run of each test.
4. The changed files, and every standard the policy names for the paths they touch. Never skip a
   standard because most of the change is of another kind.

## 3. Judge the named list

Round 1 judges the whole work, and names every fault.

A later round judges two things only: each finding an earlier round named, and the diff since the
last reviewed head. Read that head from `loop review rounds <item>`. Restrict the diff to the files
of the pull request, because a merge of the base adds noise. When you cannot read the last head,
note it, and finish `failed`.

## 4. Prove each contract

For each contract, the test names the contract, and the test fails at the merge base.

1. Run `loop review proof --test <files> --base <base ref> --run "<command>"`. Put commas between
   the test files. Add `--bring <files>` for the fixtures they need. Take the command from the
   policy. With no `--run`, the proof runs the test gate of the base config.
2. The proof makes a worktree at the merge base, and brings only those files from the head. It runs
   the setup and the tests there, and it always removes the worktree. Your checkout stays at the
   head.
3. Read the answer:
   - Exit 0 with `result` `red`: the tests fail at the merge base. Read `tail` to learn why.
   - `GREEN_AT_BASE`: the tests pass at the merge base. The contract fails. Reject.
   - `PROOF_UNDECIDED`: the setup failed, or a signal stopped the tests. Run the proof again once.
   - `CHECKOUT_NOT_CLEAN`: the hint names each difference. Bring the head in again by section 1.
4. A red run from a missing import fails for the wrong reason. Make a worktree at the head with
   `git worktree add --detach` into a directory from `mktemp -d`. Change one line of the code under
   test there, and check that the expected assertion fails. Then remove it with
   `git worktree remove --force`.

A contract that the base already keeps is an invariant. Prove it with one change to the code under
test that makes the test fail, and record each change in the comment. When no change makes it
fail, reject.

A contract about file content needs no run. Read the file at the merge base with `git show`.

A contract on documents only needs its proof command. The command prints the old text at the
merge base, and the new text at the head. Record both. The same result at both commits rejects.

## 5. Check in this order

1. A contract with no test. This is the worst fault.
2. A test that cannot fail, in one of five shapes:
   - it asserts nothing, or asserts too little
   - it asserts only a mock, or only an absence
   - it asserts what it computed itself
   - it asserts a constant
   - its fixture asserts the fixture

   A test of a relation across the rows of a table is not this fault. Name the file, the test and
   the shape.
3. A file on the list the item must not change. The change passes only when the body names a
   contract of this item, the line it overrides and the reason. Reject when any part is missing, or
   when a narrower change would prove the contract.
4. A rejection path that the item promised and the code does not build.
5. A rule of the project policy that the change breaks.

## 6. Write the comment

Put the findings first, the worst first. Write one line for each contract, each done line and each
stop condition, with pass or fail and its evidence. A fault that no line covers is a fail of its
own.

- A pass holds no fail. A reject holds at least one.
- Round 1 checks every line, because a line it skips is what round 3 raises.
- In a later round, a line that passed keeps its pass unless the diff changed what it governs.
  Name the round and the head of that pass.
- A fail that reverses an earlier pass names that round, the reason and the change in the diff.
- A fired stop condition is a finding to escalate. Mark it waived only when a recorded answer of a
  person waives it, and quote that answer.

Name each defect outside this round under its own heading, with its evidence. Never reject for it.

## 7. Submit the verdict

1. Check that the build is green at the head with `loop pr checks`. A pass needs every check green
   at that head. A missing check counts as a failed check. While a check runs, review the source.
2. When a check is still pending, wait for it. Run `loop pr checks` again every 5 minutes, for 30
   minutes at most.
3. Run `loop pr show` again. When the head moved, submit nothing. Start again at section 1 with
   the new head.
4. Run `loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>"`. The
   server posts the comment on the pull request and records it with the verdict.
5. Finish with `loop run finish --outcome done`.

A check that stays red or pending at the head is not a fault of this review. When the source holds
no fault, submit nothing. Note the check and its state with `loop work note`, and finish `blocked`.
A `failed` finish counts against the ticket, so never use it here. The server moves a ticket with a
red build to `repair`, and the review starts again after the repair. For a pending check, the server
holds the ticket at review and raises an alert. The coordinator answers the alert when the check
ends.

## 8. When the source is wrong

- **A contract is absent from the spec.** Reject. Name the contract and the spec section.
- **The source of a bug item holds the defect.** Score the finding, and escalate. Name the source,
  the contract and the line.
- **The change is right, and the item is wrong.** Score the finding, and escalate. Say which one is
  wrong.
- **The change keeps every promise, and the spec holds a defect.** Pass the work. Then file the
  defect as a decision for the epic, so the work still merges:
  `loop decision file --title "<the spec defect>" --trigger "<the event that forces the answer>"`.
  Name the spec, the contract and the line in its body.

## 9. Never

- Never write a verdict at a head you did not review.
- Never reject in a later round for a region that no round named and the diff did not touch.
- Never move the reviewed checkout off the head.
