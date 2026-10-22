# Operator checklist for v1

Written 2026-10-22, after commit `c292c69`. Every
item here needs the operator. The phases are in order, and each one depends
on the phases above it. `docs/operating.md` holds the full procedure behind
each step.

## Phase 1: Release signing key (once, on an offline machine)

- [ ] Clone `slothware-labs/factorio` onto the offline machine.
- [ ] Generate the key pair:

  ```sh
  go run ./internal/release/cmd/loop-release keygen -pub loop-release.pub -sec loop-release.sec
  ```

- [ ] Keep `loop-release.sec` offline, at mode 0600. It must never reach the
      devbox or CI.
- [ ] Copy the **second line** of `loop-release.pub` into
      `internal/release/trusted_keys.pub`, below the comment lines, and
      commit it. Until then, every binary refuses every upgrade with
      `no_trusted_key`.
- [ ] Optional: run `make release-dry` to rehearse the whole signing path
      with a throwaway key.

## Phase 2: GitHub repo for the loop itself

- [x] Create the repository: `slothware-labs/factorio` (private).
- [x] Push the branch, merge it to `main`, and make `main` the default
      branch.
- [ ] Decide on the Go module path. `go.mod` still declares
      `github.com/iantanwx/loop`. Builds and tests do not care, but
      `go install` from the repo URL needs
      `github.com/slothware-labs/factorio`.
- [ ] Check that CI runs both jobs in `.github/workflows/ci.yml`:
  - `gates`
  - `acceptance`, which provisions bd and dolt, then runs
    `make beads-contract` and `make acceptance`, in about 12 min
- [ ] Add a ruleset on `main` that requires both the `gates` and
      `acceptance` checks. `acceptance` is not required anywhere yet.

## Phase 3: Pins for arm64 and darwin (only if you run on those)

Only linux/amd64 has measured hashes. `loop install` refuses on linux/arm64
and darwin/arm64 until both hashes are recorded.

- [ ] For each of `bd` 1.3.1 and `dolt` 2.2.0, on `linux/arm64` and
      `darwin/arm64`, download the archive from the URL in
      `internal/release/provision.go`.
- [ ] Record the archive's sha256 and the extracted binary's sha256 in the
      pin table there.
- [ ] Mirror any new hashes into `ci.yml` if CI provisions that platform.
- [ ] Or approve an agent to download and measure them.

## Phase 4: The loop GitHub App

- [ ] Register a GitHub App. **No webhook**: the reactor polls, because the
      devbox has no public endpoint.
- [ ] Repository permissions:
  - **Read and write:** Contents, Pull requests, Commit statuses, Workflows
  - **Read:** Checks, Actions, Metadata
  - **Never:** Administration, Secrets, Members. `loop doctor` fails on any
    extra permission.
- [ ] Generate a private key (`.pem`) and note the **App ID**.
- [ ] Install the App on **selected repositories only**, never all
      repositories. Doctor fails on an all-repositories install.

## Phase 5: First release

- [ ] Tag the commit that has the key: `git tag v0.1.0 && git push origin v0.1.0`.
      The release workflow builds a **draft** release.
- [ ] Download it with `gh release download v0.1.0 -D dist/v0.1.0`, then move
      `dist/v0.1.0` to the offline machine.
- [ ] Sign it: `make release-sign VERSION=v0.1.0 RELEASE_KEY=/path/to/loop-release.sec`
- [ ] Upload the signature and publish:

  ```sh
  gh release upload v0.1.0 dist/v0.1.0/checksums.txt.minisig
  gh release edit v0.1.0 --draft=false
  ```

## Phase 6: Install on the host

- [ ] Check the release files, then install:

  ```sh
  minisign -Vm checksums.txt -P <public key>
  sha256sum -c --ignore-missing checksums.txt
  chmod +x loop-linux-amd64 && ./loop-linux-amd64 install
  ```

- [ ] Run `loginctl enable-linger $USER` once, so the service survives
      logout.
- [ ] Claude Code: add `"env": {"DISABLE_AUTOUPDATER": "1"}` to
      `~/.claude/settings.json`. Accept the trust dialog once for the agent
      host's checkout root.
- [ ] Codex: set `check_for_update_on_startup = false` in
      `~/.codex/config.toml`. Add `[projects."<checkout root>"]` with
      `trust_level = "trusted"`.
- [ ] In `~/.local/state/loop/host.yaml`, set the App's identity. The key
      itself goes in the next step.

  ```yaml
  credentials:
    github_app:
      app_id: <App ID>
      private_key_secret: GITHUB_APP_KEY
  ```

- [ ] Store the key in the secret store:
      `loop secret set GITHUB_APP_KEY --project _host < app-private-key.pem`.
      Then delete the `.pem` from disk.
- [ ] If you'll use `loop secret import`, list your 1Password vaults under
      `vaults:` in `host.yaml`.
- [ ] Run `loop install --check` and expect exit 0. Run `loop doctor` and
      expect exit 0, or follow each failed line's hint.
- [ ] Optional: add the nightly upgrade cron line from `docs/operating.md`.
      Each new release re-admits every ticket that failed twice, once.

## Phase 7: v1 end-to-end run (`docs/evidence/v1-e2e.md`)

This uses harness quota and makes real PRs and merges.

- [ ] Create a test repository: a new personal project with no tracker.
      Install the App on it.
- [ ] Add a ruleset on its default branch:
  - Require the CI check, plus `loop/eligible` with the loop App as its
    source
  - Require a PR, with 0 approvals
  - Put yourself on the bypass list, because you sign off a PRD or spec by
    merging it
- [ ] Commit `.loop/config.yaml`. Choose a merge mechanism (`direct` is
      simplest), and enable `implementer`, `code-reviewer`, `doc-reviewer`,
      `amender`, `spec-author`, `decomposer` and `fixer`.
- [ ] Run `loop project add <id> --repo <owner/name> --path <clone>`, then
      open the coordinator chat.
- [ ] Take one epic from intake to `done`:
  - Ask the coordinator for new work. It opens an intake room.
  - Write a PRD with `US-01` and `US-02`, and merge the PRD PR.
  - Let the spec be written and reviewed, then merge the spec PR.
  - Let the decomposer split it into tickets, then let the tickets be
    implemented, reviewed and merged.
- [ ] Give one ticket a fact only you know. Answer its escalation in chat
      and confirm the ticket returns to `todo`.
- [ ] Use the first real loop fault a run reports:
  - Fix it in a release, then run `loop upgrade`.
  - Confirm `loop fault show F-n` reads `fixed`.
- [ ] Record every result under Results in `docs/evidence/v1-e2e.md`.

## Phase 8: github-queue live evidence (`docs/evidence/github-queue.md`)

- [ ] On an **organisation** repo, install the App on that repo only.
- [ ] In its ruleset, require the CI check and `loop/eligible`, plus a merge
      queue with merge commits and a group size of at least 2. Make CI run on
      `merge_group:`.
- [ ] Set `merge: {mechanism: github-queue}`, then run `loop project add`:
  - Expect a pass.
  - Remove the queue rule, run it again, and expect a refusal that names
    `merge_queue`.
  - Put the rule back.
- [ ] Get two tickets to `ready_to_merge` at the same time. Record the
      merge-group branch names and SHAs, the `loop/eligible` statuses on the
      group commit, and one `merged` transition per ticket.

## Phase 9: darwin smoke check (`docs/evidence/darwin-smoke.md`; needs Phase 3)

- [ ] On an Apple-silicon Mac, check each in turn:
  - `loop-darwin-arm64 version` shows `darwin/arm64`
  - `install` loads `~/Library/LaunchAgents/dev.loop.loop.plist`
  - `install --check` exits 0
  - `loop status` answers
  - `loop doctor` runs
- [ ] Fill in the result table.

## Phase 10: Decisions for you (from the review fixes)

- [ ] **HTTP 500 now counts as a temporary error.** A direct merge that gets
      a 500 now reads as unknown and retries, instead of being dequeued.
      Keep it?
- [ ] **Retry counting is unchanged.** Rate-limit and timeout retries still
      count toward an effect's attempt limit; a failed eligibility post
      retries after an hour. Keep it?
- [ ] **`--body --project=x` stays a missing value**, because `--project=x`
      names a known flag. Keep it, or match only exact flag names?
- [ ] **Which open review items should be fixed next?**
  - Security: the server runs git in agent checkouts without the
    `core.fsmonitor`/hooks protections the mirror has, so an agent's
    checkout config could run commands as the server.
  - Security: the `/tmp` socket fallback isn't checked for its owner.
  - Restore: escalation and question ids can be reused, a snapshot that is
    too new isn't refused, and a stale pending intent can be marked applied.
  - If the server crashes between starting a session and recording it, the
    orphaned harness keeps running.
  - The coordinator start retries every 15 s with no backoff.
  - An escalation routed to a person after its item already moved on is
    never settled.

## Optional

- [ ] Rotate the token that leaked: the `orca-serve` journal logs the
      pairing URL with its device token.
- [ ] Approve one deliberate test kill of the orca daemon. It would show
      whether orca reuses the same session ids after a daemon death, which
      nobody has measured yet.
