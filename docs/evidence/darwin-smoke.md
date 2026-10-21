# darwin/arm64 smoke check (U16)

Status: PENDING. Nobody has run this check yet. This file records no result.

The check is manual, because no Mac runs in CI or on the devbox. Record the
result here when an operator runs it.

## What to run

On a Mac with Apple silicon:

1. Build or download `loop-darwin-arm64` for the release under test.
2. `./loop-darwin-arm64 version` prints one JSON envelope with the release
   version and `"platform": "darwin/arm64"`.
3. `./loop-darwin-arm64 install` writes
   `~/Library/LaunchAgents/dev.loop.loop.plist` and loads it. Before this can
   pass, `internal/release/provision.go` needs the measured darwin/arm64
   archive and binary sha256 of `bd` and `dolt`.
4. `loop install --check` exits 0.
5. `loop status` answers from the launchd-started server.
6. `loop doctor` runs and lists its lines.

## Result

| Field | Value |
| --- | --- |
| Date | PENDING |
| Release | PENDING |
| macOS version | PENDING |
| Steps passed | PENDING |
| Notes | PENDING |
