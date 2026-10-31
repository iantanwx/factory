---
name: loop-init
description: Set up a project repository for the loop with the operator. Use it when the operator asks to set up the loop or the project. Use it to bootstrap or scaffold the repository, or to add tests or CI for the loop. Use it also when the repository has no `.loop/config.yaml`. It interviews the operator, then makes the scaffold, tests, CI and loop project config.
---

# loop-init

The loop needs four things in a project repository before it can work there: a command for
each stack gate, CI that runs `loop gates` on each pull request, `.loop/config.yaml`, and the
project's policy files and correction ledger. This skill makes them
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

Do not ask which checks to run. The loop decides them. `loop gates` runs the loop's own gates
on each change: commit messages, suppressions, the agent-facing documents, prose and secrets.
Then it runs one command for each stack gate that `.loop/config.yaml` names: `format`, `lint`,
`audit`, `typecheck`, `test` and `build`. You choose each command from the stack's usual tools.
For example, a Go project uses `gofmt`, `go vet`, `govulncheck`, `go build`, `go test` and
`go build`. A stack with no type checker uses its strictest static check as `typecheck`.

## 3. The scaffold

Make the smallest project that the stack's own tool makes, for example `go mod init`,
`uv init`, `cargo new` or the framework's create command. Then add:

- one unit test that passes
- a `.gitignore` for the stack
- a `README.md` with the sentence from the interview, and the commands that build and test the
  project.

Add no dependency that the operator did not choose, except the tools that the stack gates need.
Run each stack gate command, and show the operator that each one passes.

## 4. CI

Write `.github/workflows/ci.yml` with one job named `gates`. Put the stack's setup steps where the
template says, for example `actions/setup-go` with the version from `go.mod`.

```yaml
name: CI

on:
  pull_request:
  push:
    branches: [<default branch>]
  merge_group:

permissions:
  contents: read

jobs:
  gates:
    runs-on: ubuntu-latest
    steps:
      # The commits gate reads the branch, and the secrets gate reads every commit.
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      # Builds loop and puts it, with gitleaks, on PATH. It runs before the
      # stack's setup steps, so the gates use the toolchain that they set up.
      - uses: slothware-labs/factorio@v0
      # <the stack's setup steps>
      - name: Run the gates
        run: loop gates
```

Keep the job name `gates`. The loop ruleset requires the checks by name. The job needs no token: the
loop's repository lets the repositories of its owner use its action.

Write `.github/workflows/audit.yml` beside `ci.yml`. On a pull request, the `audit` gate runs only
when the change touches a lock file. This workflow audits every dependency of the default branch
each day, so a new advisory reaches the project. Put the same setup steps in it.

```yaml
name: audit

on:
  schedule:
    - cron: "17 6 * * *"
  workflow_dispatch:

permissions:
  contents: read

jobs:
  audit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: slothware-labs/factorio@v0
      # <the stack's setup steps>
      - name: Audit every dependency
        run: loop gates --only audit --full
```

Add no `pull_request` or `push` trigger. The `gates` job already audits each change.

## 5. Project config

Write `.loop/config.yaml`:

```yaml
merge:
  mechanism: direct
gates:
  - name: format
    run: <the command that fails on an unformatted file>
  - name: lint
    run: <the lint command>
  - name: audit
    run: <the dependency audit command>
  - name: typecheck
    run: <the type check command>
  - name: test
    run: <the test command>
  - name: build
    run: <the build command>
stages:
  implementer: {}
  code-reviewer: {}
  doc-reviewer: {}
  amender: {}
  spec-author: {}
  prd-author: {}
  decomposer: {}
  fixer: {}
  reproducer: {}
  retro: {}
signoff:
  readers: [<the GitHub login of each person who merges a PRD or a spec>]
```

Name all six stack gates. `loop gates` fails a gate that the config does not name. Run
`loop gates` in the clone, and fix each finding before you go on.

A project that adopts the loop with lint findings it cannot fix now ratchets `lint`. Ask the
operator first. A ratcheted gate names a `baseline` file. Its command prints one key for each
finding on stdout, such as `<file>::<rule>`, and then the end line `loop:end`. The gate fails on
a count above the baseline, on a count below it, and on a stdout with no end line. Pipe the lint
tool's JSON output through `jq` in this one form, and write only the key filter:

```yaml
  - name: lint
    run: <the lint command with JSON output> | jq -rn '[inputs] | select(length > 0) | (.[] | <keys>), "loop:end"'
    baseline: .loop/lint-baseline.json
```

The form prints no end line when the tool prints nothing or prints broken JSON, so a crash fails
the gate. Never put `?` or `// empty` in the key filter. They read a changed output shape as zero
findings. Then run `loop gates --only lint --update` once. It writes the baseline from what the
gate found. Commit the baseline with the config. The `audit` gate takes the same form for its
advisory ids. Its baseline holds each accepted id with a reason and a date, and a person writes
each entry.

Ask the operator who signs the PRDs and the specs off. The server asks each reader for a review
when it opens a PRD or a spec pull request, so GitHub notifies them. Name a person's login, never
an organisation. Delete `signoff` when nobody wants the request.

## 6. Policy and ledger

The runbooks read the project's own rules from `.loop/policy/`, and the `loop-encode-lessons`
skill records each correction in the ledger. Make both now, so the first correction has a place.

1. Copy each template in `policy/` beside this skill to `.loop/policy/` in the clone, with the
   same name: `implementer.md`, `code-reviewer.md` and `coordinator.md`.
2. Fill each placeholder from the interview and the files. Use the stack gate commands of
   `.loop/config.yaml` for the commands. Delete each section that does not apply, and the
   template's first paragraph.
3. Keep the `## Userspace skills` section of the implementer and code-reviewer policy files when
   the project declares a userspace skill: a pin in the `skills:` list of `.loop/config.yaml`, or a
   committed directory under its skill root. Name each skill and the step that reads it. Delete the
   section otherwise. The coordinator template holds no such section.
4. Copy `correction-ledger.md` beside this skill to `docs/solutions/correction-ledger.md`.
5. Run `loop gates --docs`, and fix each finding.

Keep each file that exists already.

## 7. Land it

1. Show the operator the files, and get a yes.
2. Commit them on the default branch, and push. In an empty repository, this first push makes
   the default branch.
3. Watch the CI run with `gh run watch` until it passes. Fix a failure before you go on.
4. Run `loop register` again. It now finds the `gates` check, and the ruleset requires it.
5. Run `loop doctor`, and fix each line that fails.

Tell the operator that the project is ready, and that the coordinator chat takes new work.
