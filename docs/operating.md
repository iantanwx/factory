# Operating the loop

This guide is for the operator. Each section is one task. Every command
prints one JSON envelope and exits with a code from `loop schema`:
0 done, 1 nothing to do, 2 could not decide, 3 refused, 4 try again later.
No host verb asks a question.

## The instance directory

One instance lives in one directory. The default is `~/.local/state/loop`.
Set `LOOP_INSTANCE` to use another directory. It names a place, never a
credential.

| Path | What it holds |
| --- | --- |
| `versions/<v>/loop` | Each installed release, with its `release.json` and its signed checksum file |
| `current` | A symlink to the release the service runs |
| `bin/bd`, `bin/dolt` | The pinned beads and Dolt binaries |
| `loop.db` | The state database |
| `keys/kek` | The key that encrypts every secret. It is never in a snapshot |
| `admin.credential` | The host-admin credential, mode 0600 |
| `host.yaml` | Host config |
| `snapshots/` | Database snapshots. Each upgrade takes one |
| `release-state.json` | The highest version this instance ever installed |
| `previous_version` | The release the current one replaced |

The server runs from this directory. It never runs from a project
checkout, so a merge to a project's `main` never changes the running loop.

## Install

1. Download the release for your platform: `loop-linux-amd64`,
   `loop-linux-arm64` or `loop-darwin-arm64`, with `checksums.txt` and
   `checksums.txt.minisig`.
2. Check the files before the first run. Later upgrades check them for you.

   ```sh
   minisign -Vm checksums.txt -P <the loop release public key>
   sha256sum -c --ignore-missing checksums.txt
   ```

3. Run the install:

   ```sh
   chmod +x loop-linux-amd64
   ./loop-linux-amd64 install
   ```

`loop install` does these steps:

- It copies itself into `versions/<v>/` and points `current` at it.
- It downloads the pinned `bd` and `dolt`. It refuses a download whose
  sha256 differs from the hash built into the binary.
- It writes the default `host.yaml`, with every throughput entry. It never
  replaces an existing `host.yaml`.
- It creates the key file and the host-admin credential file. It never
  replaces either one.
- It writes the service unit and starts the service. Linux uses a systemd
  user unit with `Type=notify` and `Restart=on-failure`. macOS uses a
  launchd agent. No unit and no environment file holds the credential.

Run `loop install --check` at any time. It changes nothing. It exits 0 when
the instance matches what install writes. It exits 3 with code `DRIFT`, and
the hint lists each difference. Run `loop install` again to repair drift.

On Linux, run `loginctl enable-linger $USER` once. Without it, systemd stops
the user service when you log out.

Today only linux/amd64 has measured hashes for `bd` and `dolt`. On
linux/arm64 and darwin/arm64, install refuses until the operator records
both the archive and the binary sha256 in `internal/release/provision.go`
and ships a release with them.

## Prepare the harnesses

Runs start with no person present. A harness that shows an update prompt or
a trust dialog stops the run. Set these once on each host:

- Claude Code: set `"env": {"DISABLE_AUTOUPDATER": "1"}` in
  `~/.claude/settings.json`. Accept the trust dialog once for the agent
  host's checkout root, or for a directory above it.
- Codex: put `check_for_update_on_startup = false` at the top of
  `~/.codex/config.toml`. Add a `[projects."<checkout root>"]` table with
  `trust_level = "trusted"`.

These files belong to the vendors, and their keys can change. `loop doctor`
reads them and says what it found.

## Check the host

Run `loop doctor`. It exits 0 when every check passes. It exits 3 with code
`DOCTOR_FAILED`, and the hint names each failed check and what to change.

It checks the install, the harness settings above, and that the agent host
knows each project's repository. The GitHub checks run on the server, so the
App's private key never leaves it. They cover the App's installation and
permissions, the default-branch rules, and project config on the default
branch. `loop doctor` asks the server for them. When the server is down, it
reports them as one failed `github` line marked unchecked.

## Add a project

```sh
loop project add alpha --repo owner/alpha --path /home/me/src/alpha
```

The path is the project's local clone, an absolute path. The command
refuses when the default branch lacks a rule the loop needs. Set the rules
first, as the next section says.

The project needs `.loop/config.yaml` on its default branch. It names the
merge mechanism, the gates and the stages. For `mergify`, the loop proposes
`.mergify.yml` through its own pull request when the repository has none.

## Set the default-branch ruleset

Do this once for each project repository. In GitHub, open Settings, then
Rules, then Rulesets, and make a branch ruleset for the default branch:

1. Require status checks to pass. Add the CI check. Add `loop/eligible`,
   and set its source to the loop App. The loop posts `loop/eligible` only
   after a review pass, because the App cannot approve its own pull
   request.
2. Require a pull request before merging, with no required approvals. This
   blocks direct pushes to the default branch.
3. Add yourself to the bypass list. You sign off a PRD or a spec by merging
   its pull request, and that merge carries no `loop/eligible` status.

Install the App on the selected repositories only. Grant it contents, pull
requests, statuses and workflows (read and write), checks and actions
(read), and metadata. Never grant administration, secrets or members.
`loop doctor` fails on any other permission, and on an installation set to
all repositories.

## Choose a merge mechanism

Set `merge.mechanism` in `.loop/config.yaml`:

| Mechanism | Use it when | What it needs |
| --- | --- | --- |
| `mergify` | You have a Mergify plan for the repository | The Mergify App, and the `.mergify.yml` the loop proposes |
| `github-queue` | The repository can use GitHub's merge queue | A merge-queue rule in the ruleset, and CI that runs on merge groups |
| `direct` | The repository has few merges | Nothing more. Each merge waits for the one before it, and a branch that fell behind is reviewed again |

For `github-queue`, add "Require merge queue" to the ruleset, with the merge
method set to merge commits. Make CI run on the merge group too:

```yaml
on:
  pull_request:
  merge_group:
```

`project add` refuses `github-queue` when the default branch has no
merge-queue rule.

## Import secrets

Platform credentials live in the encrypted store, not in files. Store one
by hand from stdin:

```sh
loop secret set GITHUB_APP_KEY --project _host < app-private-key.pem
```

Import a project's secrets from 1Password:

```sh
loop secret import --project alpha
```

The import reads the project's secret manifest on its default branch. It
reads only the vaults that `host.yaml` lists under `vaults`. It prints each
new reference since the last import, so read that list before you trust it.

## Upgrade by hand

```sh
loop upgrade v1.2.3
loop upgrade          # the newest published release
```

The upgrade checks everything before it changes anything:

- the signature of `checksums.txt`, against the keys built into the
  running binary;
- the version in the signed trusted comment, so an old signed release
  cannot pose as a new one;
- the sha256 of the binary and of `release.json`;
- that the version is not below the highest one ever installed;
- the minimum from-version and the database's minimum reader schema;
- the hashes of any new pinned `bd` or `dolt`.

A refused upgrade exits 3 and changes nothing. A release it cannot fetch
exits 4. Then it drains: the server admits no new run and waits for the runs
in flight, or for the drain timeout. It stops the service, snapshots the
database, installs the release into `versions/<v>/`, repoints `current` and
starts the service. A run still alive after the timeout keeps running. Its
CLI waits through the restart, and the new server answers it. The report
lists `no_rollback` reasons, if any.

## Upgrade by cron

The upgrade never prompts, so the host's own cron can run it:

```cron
# Each night at 03:17, install the newest release.
17 3 * * * flock -n /tmp/loop-upgrade.lock $HOME/.local/state/loop/current/loop upgrade >> $HOME/.local/state/loop/log/upgrade.log 2>&1
```

Exit 1 means the newest release is already installed. Exit 3 means a check
refused it, and the log holds the reason. Exit 4 means the fetch failed, and
the next night tries again.

Know this effect first. The retry rule keys on the loop release (R10). Each
new release admits every twice-failed ticket again. A nightly upgrade that
installs a new release therefore retries each such ticket once.

## Sign a release

The signing key never touches the devbox or CI.

Once, on an offline machine, make the key:

```sh
go run ./internal/release/cmd/loop-release keygen -pub loop-release.pub -sec loop-release.sec
```

Keep `loop-release.sec` offline, at mode 0600. Put the second line of
`loop-release.pub` into `internal/release/trusted_keys.pub` and merge it.
Binaries built after that merge trust the key. The first binary with the
key goes on by `loop install`, because a binary with no key refuses every
upgrade.

For each release:

1. Tag it: `git tag v1.2.3 && git push origin v1.2.3`. The release workflow
   builds the three binaries, `release.json` and `checksums.txt` into a draft
   release. It holds no key and publishes nothing.
2. Download the draft's assets into `dist/v1.2.3/` with
   `gh release download v1.2.3 -D dist/v1.2.3`, and move them to the offline
   machine.
3. Sign: `make release-sign VERSION=v1.2.3 RELEASE_KEY=/path/to/loop-release.sec`.
   It checks every asset's sha256, signs `checksums.txt` with the version in
   the trusted comment, and verifies the result against
   `internal/release/trusted_keys.pub`.
4. Upload the signature and publish:

   ```sh
   gh release upload v1.2.3 dist/v1.2.3/checksums.txt.minisig
   gh release edit v1.2.3 --draft=false
   ```

Set `NO_ROLLBACK=1` on the `release-dist` run to mark a release by hand. A
release that bumps the `bd` or `dolt` pin, or that adds a migration an older
binary cannot read, is no-rollback without the mark.

To rotate the key, add the next key's line to `trusted_keys.pub` and ship
that release signed with the old key. Sign the releases after it with the
new key.

`make release-dry` runs the whole path with a throwaway key in a temporary
directory.

## Roll back

```sh
loop rollback
```

It points `current` at the installed version below the current one and
restarts the service. It restores nothing, so every write after the upgrade
survives. It exits 3 with code `NO_ROLLBACK` when the newer release is
marked no-rollback, bumped the `bd` or `dolt` pin, or wrote a schema the
older binary cannot read. Use `loop restore` then.

## Restore the database

```sh
loop restore
```

It restores the newest snapshot in `snapshots/`, with no manual step. It
needs the host-admin credential file. It works when the server cannot start.

- When the server answers, it drains first. When the server is down, it
  skips the drain and never waits for the socket.
- It stops the service.
- It copies the current secret table into the snapshot's copy. A secret you
  rotated after the snapshot keeps its new value, and the current key file
  decrypts every secret.
- It raises each lease epoch that grew after the snapshot to one above the
  replaced database's value, and it keeps every token revocation. So no
  epoch is granted twice, and no settled run's token works again.
- It keeps the replaced database as `loop.db.before-restore`.
- It swaps the files atomically and starts the service. The start-up
  reconcile raises each epoch to the bead's.

When the current database cannot be read, the restored database keeps the
snapshot's secret table. The JSON then says `"secrets_source": "snapshot"`.
Check every secret you rotated after the snapshot.

Choosing a snapshot by name needs a `snapshot` argument on the verb, which
the CLI surface does not have yet.

## The same-user gap

The server, the CLI and every agent run as one OS user. An agent can
therefore read the database, the key file, the host-admin credential file
and the host's SSH key. Run tokens, the server's push path and the audit
records make a bypass visible. They cannot stop one.

A value that a run printed can also stay where the loop cannot reach it:

- Claude Code transcripts in `~/.claude/projects/`;
- Codex sessions in `~/.codex/sessions/`;
- the agent host's terminal scrollback, for example orca's.

Removing a checkout's `.env` does not erase those copies. Rotate a secret
that you think a run printed.
