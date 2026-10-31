# loop

`loop` is a standalone agentic SDLC loop: one static Go binary that holds the
server, the agent-only CLI and the embedded skill pack. Agents call the CLI.
The server is the only writer of state.

Every CLI verb prints one JSON envelope and exits with one shared exit code.
`loop schema` prints every verb, flag, exit code and refusal code, and
`loop <noun> <verb> --help` prints one verb's slice of that schema.

## Develop

- `make gates` runs lint, tests, the race detector and the proto drift check.
- `loop gates` runs the loop's own gates, then each stack gate in
  `.loop/config.yaml`. CI runs it, and it must pass before a change merges.
- `make proto` regenerates `gen/` after a change under `proto/`.
- `make build` builds `bin/loop`; `make build-all` builds every platform.

## Requirements

`release.Requirements` in `internal/release/requirements.go` names each
binary the loop runs. The `requirements` gate fails when this section does
not name a binary of that table.

To build loop:

- `go` 1.26, for the build.
- `make`, for `make gates`, `make proto` and `make build`.
- `buf` 1.71, for `make proto`.
- `golangci-lint` v2, for `make lint`.
- `bd` and `dolt`, for the parity suite of `internal/beadsx` only. Set
  `LOOP_BD` and `LOOP_DOLT` to the pinned binaries.
- `gitleaks`, for the `secrets` gate. It runs in CI only.

To run a host. `loop install` refuses when a required binary is missing,
and `loop doctor` prints one `requires_<bin>` line for each binary:

- `git`, required, for each checkout, mirror and gate.
- `python3`, required, for the prose rules of `loop gates` and of each body
  the server checks.
- `orca`, required, for the agent host. The Orca app puts its CLI at
  `~/.config/orca/cli/bin/orca`.
- `gh`, required, for `loop register` and `loop upgrade`. Run
  `gh auth login` after you install it.
- `systemctl` and `systemd-run`, required on Linux, for the service and
  for an upgrade that the coordinator starts.
- `launchctl`, required on macOS, for the launchd agent.
- `loginctl`, optional on Linux, for linger. Without it, the service stops
  when you log out.
- `tailscale`, optional, for the tailnet address of the GitHub App flow.
- `op`, optional, for `loop secret import` from 1Password.

The triage panel's judges, `claude` and `codex`, have their own line in
`loop doctor`: `judge_binaries`.
