# Implementer policy

## Commands

- Fast gates: `loop gates --fast`. Run them after each commit.
- Tests of the changed directories: `go test ./<dir>/...`.
- One test file: `go test ./<dir> -run <TestName>`.
- The full suite: `loop gates`, then `make acceptance` when the change touches the server, the
  state machine, the reactor or the CLI surface. CI runs both on every pull request.

## Standards

- The commit message rules: the `commits` gate of `loop gates`. The subject holds at most 72
  characters.
- A change to the CLI surface regenerates `gen/` with `make proto`, and `make proto-check` proves
  it.
- A change under `pack/` passes `make pack-check`.

## Priorities

- Fix loop faults and bugs before you start new work.
