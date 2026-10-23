---
name: loop-init
description: Set up a project repository for the loop with the operator. Use it when the operator asks to set up the loop or the project. Use it to bootstrap or scaffold the repository, or to add tests or CI for the loop. Use it also when the repository has no `.loop/config.yaml`. It interviews the operator, then makes the scaffold, tests, CI and loop project config.
---

# loop-init

The loop needs three things in a project repository before it can work there: a test command,
CI that runs that command on each pull request, and `.loop/config.yaml`. This skill makes them
with the operator. The operator decides each choice. You ask, you build and you check.

The operator starts it with a plain request, such as "set up my loop". Run it in the project's
clone, after `loop register` put the loop skills there. Run it again on a
repository that already has code. Then keep each part that exists, and make only the parts that
are missing.

## 1. The session

Ask one question at a time. Give two to four options for each, with what each option costs.
Recommend one option, and say why. Then wait for the answer.

Read the repository before you ask. Do not ask a question that the files answer. A repository
with a `go.mod`, a `package.json`, a `Cargo.toml` or a `pyproject.toml` already has a stack.

## 2. The interview

Get these answers, in this order:

1. **The project.** One sentence: what it is, and who uses it.
2. **The stack.** The language, the framework, and the package manager.
3. **The tests.** The test framework, and the command that runs the tests.
4. **The other checks.** A linter, a formatter or a type check, if the operator wants one.

Keep the list short. Each check you add runs on each pull request, and an agent must keep each
one green.

## 3. The scaffold

Make the smallest project that the stack's own tool makes, for example `go mod init`,
`uv init`, `cargo new` or the framework's create command. Then add:

- one unit test that passes
- a `.gitignore` for the stack
- a `README.md` with the sentence from the interview, and the commands that build and test the
  project.

Add no dependency that the operator did not choose. Run the test command, and show the operator
that it passes.

## 4. CI

Write `.github/workflows/ci.yml` with one job named `ci`. The job must:

- run on `pull_request`, on `push` to the default branch, and on `merge_group`
- install the stack and its dependencies
- run each check from the interview, and then the test command.

Keep the job name `ci`. The loop ruleset requires the checks by name.

## 5. Project config

Write `.loop/config.yaml`:

```yaml
merge:
  mechanism: direct
gates:
  - name: test
    run: <the test command>
stages:
  implementer: {}
  code-reviewer: {}
  doc-reviewer: {}
  amender: {}
  spec-author: {}
  decomposer: {}
  fixer: {}
```

Add one gate for each other check from the interview. A gate name is a lower-case identifier.
Agents run each gate before they open a pull request.

## 6. Land it

1. Show the operator the files, and get a yes.
2. Commit them on the default branch, and push. In an empty repository, this first push makes
   the default branch.
3. Watch the CI run with `gh run watch` until it passes. Fix a failure before you go on.
4. Run `loop register` again. It now finds the `ci` check, and the ruleset requires it.
5. Run `loop doctor`, and fix each line that fails.

Tell the operator that the project is ready, and that the coordinator chat takes new work.
