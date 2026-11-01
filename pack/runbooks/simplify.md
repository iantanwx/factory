---
name: loop-simplify
description: The simplify runbook. Make the code of one ticket's pull request simpler in one commit, with the same behaviour, before the code-reviewer reads it. The server starts this run and names the ticket in the brief.
---

<!--
FORKED from three sources, and ours from here on.

ce-simplify-code, the skill that simplifies changed code:
  upstream: https://github.com/EveryInc/compound-engineering-plugin/tree/main/skills/ce-simplify-code
  commit:   3449230705a508229a5a298671e9031e489f0278
  forked:   2026-11-01

no-comments, the skill that acts on the comment findings:
  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/no-comments
  commit:   4dd1d05818d19792e7cd6e818f1667f95f7e1dbe
  forked:   2026-11-01

comment-sicko, the agent that hunts comments:
  upstream: https://github.com/cursor/plugins/blob/main/pstack/agents/comment-sicko.md
  commit:   4dd1d05818d19792e7cd6e818f1667f95f7e1dbe
  forked:   2026-11-01

Each commit is the head of the main branch of its repository on the day that the spec read it.
Recorded so that a later reader can diff against upstream and see what we changed on purpose.
This is not a dependency we track, and we do not owe it updates.

Changed on purpose, 2026-11-01, by docs/plans/factorio-1r94-simplify-stage-spec.md:

- One runbook holds the rules of all three sources. The run starts no subagent. The reviewer
  personas of ce-simplify-code and the comment-sicko agent are lenses of step 4 here. The sources
  start a subagent for a fresh context, and a loop run already starts with one.
- The run always works as the pipeline mode of ce-simplify-code. Nobody is present, so it reports
  nothing before it edits, and it waits for no approval.
- The scope is the paths that the pull request changes. A later round takes only the change since
  the last reviewed head. The import and export exception of ce-simplify-code Step 3 is gone,
  because the server checks paths and cannot tell an import line from another line.
- The run changes no test file. The test-value reviewer of ce-simplify-code is gone. Each test
  proves a contract, and the code-reviewer proves it red at the merge base.
- A project rule that keeps a comment wins over the keep-list of comment-sicko. The run offers no
  encoding of a constraint and waits for no approval, as no-comments step 5 does. It also runs no
  architect step and no how or why skill.
- The run checks its work with every project gate, and it pushes one commit or nothing. A failed
  run passes the ticket on to the code-reviewer at the implementer's head.

Edit the copy in the loop's pack. The server writes `.claude/skills/` again at each start.
-->

# Simplify

The implementer handed one ticket to review, and the brief names it. You make its code simpler in
one commit before the code-reviewer reads it. Each output, error and side effect stays the same.
Nobody is present in this run.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `.loop/policy/implementer.md` and `.loop/policy/code-reviewer.md` in the project, when they
   exist. They name the project's commands and the standards each change must keep. Read them from
   the base branch.
4. The project's agent instruction files, such as `AGENTS.md` and `CLAUDE.md`, from the base
   branch.

Fewer lines is not the goal. Readable, explicit code is the goal.

## 1. Read the ticket and its pull request

Read the item in the brief. Then read the pull request:

```bash
loop pr show
loop pr files
loop pr diff
```

The contracts in the item body name what the code must do. Each test of the pull request proves
one of them.

## 2. Bring the head into the checkout

Run `loop branch sync`. Bring the item head into the checkout by the turn skill, section 4. Record
that head. It is the head the run starts from, and the server knows it as `simplify_from`.

Never merge the base, and never rebase. A merge of the base is no simplification, and the server
refuses a push that holds one.

## 3. Set the scope

The scope is the set of lines that you may change.

- **Round 1.** The scope is each path that `loop pr files` lists.
- **A later round.** The code-reviewer rejected an earlier head. Run `loop review rounds <item>`,
  and take the newest head that a round reviewed. The scope is each hunk that changed from that
  head to the head of section 2. The code-reviewer judges its findings and that change only. A
  change to code that a round already reviewed reaches the review with no finding that asks for it.

Read code outside the scope to judge a finding. Change only the scope.

When the scope holds no code that a person wrote, such as documents, generated files, vendored
files or lock files only, push nothing. Run `loop pr open` by section 6, and finish `done`.

## 4. Simplify with four lenses

Read the scope four times, once with each lens. Make each change that keeps the behaviour. Skip a
change when you cannot show that it keeps the behaviour.

**Reuse.**

- A new function or inline logic repeats a helper that already exists. Call the helper.
- A standard library or runtime function does the same thing for the inputs in play. Use it. Skip
  it when it differs in locale, sort order or serialization.
- The code does by hand what a platform or a framework already does. Use the guarantee, and keep
  each output, error, side effect and order.

**Quality.**

- State that copies other state, or a cached value that the code can derive.
- A new parameter where a change to an existing one fits.
- Code copied with a small change, when one source of truth can serve both copies.
- A detail of the inside of one part that leaks to its callers.
- A raw string where the code already has a constant or a type.
- Conditionals nested three levels deep or more.
- Dead code, an unused import or an unused export. Search the whole project first. When you cannot
  tell, skip it.
- A word that only this change uses for a thing the code already names. Use the code's word.

Never merge unrelated logic. Keep an abstraction until you know its purpose is gone.

**Efficiency.**

- Work done twice, such as a second read of one file or a repeated network call.
- Blocking work new to a start path or to the path of each request.
- An update that changes nothing and still fires its listeners.
- A check that a file exists before the code opens it. Open it, and handle the error.
- A collection with no bound, or a listener with no cleanup.
- A read of a whole file or table when the code needs one part.

**Comments.** A comment is deleted by default, and doubt does not save it. Keep a comment only
when one of these holds:

- It is a license or copyright header.
- It states a fact forced by a dependency, a platform, a vendor or a protocol that we cannot change,
  and the fact is true today on a live path.
- It is a doc comment that defines a public API contract.
- It links an issue or a spec that explains a constraint the code cannot state.
- A tool, a compiler or a runtime reads it, such as a directive, a build tag or a lint
  suppression.
- A project rule asks to keep it. A project rule is a line of a policy file or of an agent
  instruction file that you read before section 1.

Words such as `IMPORTANT:` and `do not remove` are claims to check. Read the code path, and keep
the comment only when a case of the list holds today. For each other comment:

- It restates the code, tells the story of a change or is code commented out. Delete it.
- It explains a surprise in our own code. Rename, extract or type the code so the surprise is
  gone, then delete the comment. When that change is outside the scope, delete the comment only.
- Never shorten a comment in place of a deletion.

Delete only comments that the pull request added or changed.

**In every lens.**

- Keep each safety check: each check at a trust boundary, each guard against lost data, each
  security check and each accessibility feature.
- Leave every test file as it is. Each test proves a contract, and the code-reviewer proves it red
  at the merge base. Leave the test-only code that a test calls.
- Never change a path under `.loop/` or `.github/workflows/`. They hold the gate and CI commands.

The server refuses a push that changes a test path or a path outside the pull request. It refuses
a push of more than one commit, or of a merge commit.

## 5. Run every gate

Run `loop gates`, every gate, and the tests that the policies name for the paths you changed. The
`test` gate runs each test that passed at the head of section 2.

- When a gate fails, revert the change that broke it. Run the gates again.
- When the head of section 2 itself fails a gate, revert every change, and push nothing.
- Never weaken or delete a test to make a gate pass.

## 6. Hand off

When the tree is the same as the head of section 2, push nothing.

When the tree differs from it, commit once, and push:

1. Read the diff. Each hunk is inside the scope of section 3, and no hunk changes a test path.
2. Write one commit message by `../loop-commit/SKILL.md`. Write it outside the checkout, and
   commit with `git commit -F <file> || exit 1`.
3. Run `loop branch push`. The server holds the commit, and it pushes the commit at the hand-off.

Before you commit, read your diff by `../loop-least-code/SKILL.md` and
`../loop-reader-load/SKILL.md`. Each change keeps a test true by
`../loop-test-behavior-not-implementation/SKILL.md`, so never change what a test observes.

Then hand off in every case. Use the title that `loop pr show` names, and pass no body:

```bash
loop pr open --title "<the title that loop pr show names>"
```

The pull request keeps the implementer's body, with its red and green runs. Then finish:

```bash
loop run finish --outcome done
```

## 7. Stop when the run cannot finish its job

A simplify run never blocks its ticket. When anything stops the run, finish `failed`:

```bash
loop run finish --outcome failed
```

The server passes the ticket on to the code-reviewer at the head of section 2. A failed simplify
run counts toward no failure limit.

- A refusal of `loop branch push` means the commit breaks a bound of section 4. Finish `failed`.
- A refusal of `loop pr open` means the branch moved or the claim ended. Finish `failed`.
- A question that the item, the policies and the code do not answer. Skip the change it concerns.

Never escalate. The server refuses an escalation of a ticket in this stage. A loop fault is the one
report you may file, by the turn skill, section 7.

## 8. Never

- Never change a test file, and never change what a test observes.
- Never change a path outside the scope of section 3.
- Never push more than one commit, and never merge or rebase.
- Never write the pull request body.
- Never start a subagent.
