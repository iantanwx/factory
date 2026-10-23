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

Requires Go 1.26, buf 1.71 and golangci-lint v2.
