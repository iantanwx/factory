# Code-reviewer policy

The loop's code-reviewer runbook reads this file from the base branch at the start of each run.

## Commands

- Tests of the changed directories: `go test ./<dir>/...`.
- One test file at the merge base: `go test ./<dir> -run <TestName>`.
- The parity suite: `make acceptance`. It takes about eight minutes.

## Operator cost

Each step that the operator must take is a cost. A step is a secret or a token to make, a click
in a browser, a setting on GitHub, or a `loop doctor` check that can fail. A change that adds a
step must say, in its pull request body, what attack or failure the step stops, and why no
mechanism that the operator already has stops it. When the body does not say both, request
changes.

Prefer what the host and GitHub already give: the operator's `gh` login, an action that the
owner's private repository shares, and the App installation.

These changes all failed this rule, on 2026-10-23:

- A public loop App. A private App for each owner installs with no extra risk.
- A release token in each project's Actions secrets. The loop's own action needs no token.
- A doctor check that failed an App installed on all repositories. The loop acts only on the
  projects that the operator registers.
