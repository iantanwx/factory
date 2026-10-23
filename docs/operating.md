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
| `beads/` | The beads workspaces and the supervised Dolt server's data |
| `beads/dolt/root.password` | The Dolt root password, mode 0600. The server makes it at the first start |

The server runs from this directory. It never runs from a project
checkout, so a merge to a project's `main` never changes the running loop.

## Install

Run one command on the host. It needs `gh`, logged in to an account that
can read the loop repository:

```sh
gh release download -R slothware-labs/factorio -p install.sh -O - | sh
```

`install.sh` downloads the newest release for this platform, checks the
binary's sha256 against `checksums.txt`, and runs `loop install`. Pass
`loop install` flags after `sh -s --`. For example, `sh -s -- --github-owner
acme` makes the organisation `acme` own the App. On a host that already runs
the loop, the script runs `loop upgrade` instead, and the upgrade checks
the release's signature.

`loop install` does these steps. Each one leaves alone what is already
set, so running install again only repairs drift:

- It copies itself into `versions/<v>/` and points `current` at it.
- It downloads the pinned `bd` and `dolt`. It refuses a download whose
  sha256 differs from the hash built into the binary.
- It writes the default `host.yaml`, with every throughput entry, and the
  orca agent host with its checkouts under `~/orca/workspaces`. It never
  replaces an existing `host.yaml`.
- It creates the key file and the host-admin credential file. It never
  replaces either one.
- It links `~/.local/bin/loop` to `current/loop`, so `loop` is on your
  PATH and follows each upgrade.
- On Linux, it runs `loginctl enable-linger $USER`, so the service keeps
  running after you log out. A host without logind gets a line in the
  report naming the command instead.
- It sets up the harnesses, as the next section says.
- It creates the loop GitHub App, unless `host.yaml` already names one, or
  you pass `--no-github-app`. See "Create the GitHub App" below.
- It writes the service unit and starts the service. Linux uses a systemd
  user unit with `Type=notify` and `Restart=on-failure`. macOS uses a
  launchd agent. No unit and no environment file holds the credential.

The report's `setup` list names each change made outside the instance
directory, and each step left to you.

Run `loop install --check` at any time. It changes nothing. It exits 0 when
the instance matches what install writes. It exits 3 with code `DRIFT`, and
the hint lists each difference. Run `loop install` again to repair drift.

## Create the GitHub App

Install creates the App through GitHub's manifest flow. The App gets no
webhook, because the reactor polls, and it gets exactly the permissions
`loop doctor` allows: contents, pull requests, statuses, workflows and
actions (read and write), and checks and metadata (read).

1. Install prints a link to a page it serves on port 47123, which opens
   GitHub's form with every setting filled in. On a Tailscale host the page
   is on the host's tailnet address, so any browser on the tailnet reaches
   it. Elsewhere it is on `127.0.0.1`. Click **Create GitHub App**.
2. GitHub sends the browser back to that local address. Install stores
   the App's private key in the secret store as
   `_host/GITHUB_APP_KEY_<OWNER>`, and writes the App's owner and id into
   `host.yaml`.
3. The browser lands on the App's installation page. Pick **Only select
   repositories**, choose the project repositories, and click **Install**.
   `loop doctor` fails an installation on all repositories.

When the browser runs on another machine and the host has no Tailscale,
forward the port first
(`ssh -L 47123:127.0.0.1:47123 <host>`), or paste the `data:` link install
prints into the browser's address bar. GitHub then sends the browser to an
address it cannot reach. Copy that address from the address bar and paste
it into the terminal. Install waits 30 minutes, then exits 4 with code
`APP_NOT_CREATED`, and the next install tries again.

The App is private. A private App installs only on the repositories of its
owner, so each owner of a project gets its own loop App: your account, and
each organisation that holds a project. `--github-owner <org>` makes the
organisation own the first App. `loop register` makes the App of any other
owner, as the next section says.

## Prepare the harnesses

Runs start with no person present. A harness that shows an update prompt or
a trust dialog stops the run. Install sets these for you:

- Claude Code: `"env": {"DISABLE_AUTOUPDATER": "1"}` in
  `~/.claude/settings.json`, and the trust dialog accepted for the agent
  host's checkout root in `~/.claude.json`.
- Codex, when `~/.codex` exists: `check_for_update_on_startup = false` at
  the top of `~/.codex/config.toml`, and a `[projects."<checkout root>"]`
  table with `trust_level = "trusted"`.

Install keeps every other key in these files. They belong to the vendors,
and their keys can change. `loop doctor` reads them and says what it found.

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

Run `loop register` in the project's local clone:

```sh
cd ~/src/alpha && loop register
```

It uses your `gh` login and does these steps. Each one leaves alone what
is already right, so you can run it again:

- It reads the repository and its default branch from the clone. An empty
  repository is fine.
- It keeps a ruleset named `loop` on the default branch, as the next
  section says. The ruleset requires the checks that ran on the head of
  the default branch. GitHub offers no rulesets for a private repository
  on a free plan. Then the report says `unavailable`, and the loop reads
  CI itself before each direct merge.
- It writes the loop skills into the clone, under `.claude/skills` and
  `.agents/skills`, and lists them in `.git/info/exclude`.
- It registers the project with the server, with the repository name as
  its id. Give another id as `loop register <id>`.
- It copies the global `LOOP_RELEASE_TOKEN` secret into the repository's
  Actions secrets, so CI can download `loop`. The secret is a fine-grained
  token that reads the contents of `slothware-labs/factorio`. Store it once
  with `loop secret set LOOP_RELEASE_TOKEN --scope global`.

- It finds the loop App of the repository's owner. When the owner has no
  App, it prints a link that opens the App form, as install does. Click
  **Create GitHub App**. For an organisation, you must be an owner of it.
- It waits for the App to be on the repository. When it is not, it prints
  the App's install page. Pick the repository and click **Install**.
  Register waits 30 minutes, then fails with code `no_app`.

When the project has no CI yet, start Claude Code or Codex in the clone
and say "set up my loop". That starts the `loop-init` skill. It asks you
for the stack, then makes a scaffold with one passing test, a command for
each stack gate, a CI workflow with a `gates` job, and `.loop/config.yaml`.
It pushes them, waits for CI to pass, and runs `loop register` again, so
the ruleset requires the `gates` check.

## The gates

`loop gates` is the definition of done in a project. Agents run it before
each push, and the project's CI runs it on each pull request. The loop
ships the gates that do not depend on the stack: commit messages,
suppressions, the agent-facing documents, ADRs, PRDs and their citations,
the correction ledger, prose and secrets. Then it runs the command that
`.loop/config.yaml` names for each stack gate: `format`, `lint`, `audit`,
`typecheck`, `test` and `build`. `audit` runs only when the change touches
a lock file. `secrets` runs gitleaks, in CI only.

`loop gates --fast` skips `test` and `build`, `loop gates --docs` runs the
document gates alone, and `loop gates --only <gate>` runs one.

`loop project add alpha --repo owner/alpha --path /home/me/src/alpha` does
the registration step alone.

When you transfer a repository to another owner on GitHub, update the
clone's `origin`, install the App on the new owner, and run
`loop register` again. It finds the project by the clone's path and moves
it with `loop project move <id> --repo <new owner/name>`. The project keeps
its id, secrets and history.

## Set the default-branch ruleset

`loop register` makes this ruleset. To make it by hand, open Settings, then
Rules, then Rulesets in GitHub, and make a branch ruleset for the default
branch:

1. Require status checks to pass. Add the CI check. Add `loop/eligible`,
   and set its source to the loop App. The loop posts `loop/eligible` only
   after a review pass, because the App cannot approve its own pull
   request.
2. Require a pull request before merging, with no required approvals. This
   blocks direct pushes to the default branch.
3. Add yourself to the bypass list. You sign off a PRD or a spec by merging
   its pull request, and that merge carries no `loop/eligible` status.

Install the App on this repository too, from the App's settings on GitHub,
under **Install App**. Keep the installation on selected repositories.

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

## Secrets

A project lists the secrets its runs need in `.loop/secrets.yaml`, names
only. An agent adds a name in the pull request that needs it:

```yaml
secrets:
  - name: OPENAI_API_KEY
  - name: STRIPE_KEY
    source: op://Dev/stripe/key   # only for loop secret import
```

At each run start, the server writes each listed secret into the
checkout's `.env`. A run whose list names a secret with no stored value
does not start, and the inbox names the secret.

Store a value from stdin. The value never prints:

```sh
loop secret set STRIPE_KEY --project alpha < key.txt
loop secret set OPENAI_API_KEY --scope global < key.txt
```

`--scope project` is the default and needs `--project`. A global value
serves every project whose list names it, and a project's own value wins.
A project that does not list a name never gets the global value.

Platform credentials live under `_host`. Install stores the App's key
itself. Store another one the same way:

```sh
loop secret set MERGIFY_TOKEN --project _host < token.txt
```

Import a project's secrets from 1Password:

```sh
loop secret import --project alpha
```

The import reads the project's secret manifest on its default branch, and
each entry with a `source`. It reads only the vaults that `host.yaml` lists under `vaults`. It prints each
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

The upgrade never prompts, so the host's own cron can run it. The loop
repository is private, so the cron line fetches through `gh`, as install
does:

```cron
# Each night at 03:17, install the newest release.
17 3 * * * flock -n /tmp/loop-upgrade.lock sh -c 'gh release download -R slothware-labs/factorio -p install.sh -O - | sh' >> $HOME/.local/state/loop/log/upgrade.log 2>&1
```

Exit 1 means the newest release is already installed. Exit 3 means a check
refused it, and the log holds the reason. Exit 4 means the fetch failed, and
the next night tries again.

Know this effect first. The retry rule keys on the loop release (R10). Each
new release admits every twice-failed ticket again. A nightly upgrade that
installs a new release therefore retries each such ticket once.

## Releases

Releases need no step from you. Each push to `main` that passes the
`gates` and `acceptance` jobs becomes the next patch release: CI builds the
three binaries, signs `checksums.txt` with the `LOOP_RELEASE_KEY` Actions
secret, and publishes them with `install.sh`. Hosts install it with
`install.sh`, or overnight through the cron line above.

A release that bumps the `bd` or `dolt` pin, or that adds a migration an
older binary cannot read, is marked no-rollback by itself.

## Roll back

```sh
loop rollback
```

It points `current` at the installed version below the current one and
restarts the service. It restores nothing, so every write after the upgrade
survives. It exits 3 with code `NO_ROLLBACK` when the newer release is
marked no-rollback, bumped the `bd` or `dolt` pin, or wrote a schema the
older binary cannot read. No rollback past that release is possible: no verb
points `current` at an older release, and `loop upgrade` refuses a version
below the highest one installed. `loop restore` then recovers the database,
but it keeps the current release. That release migrates the restored
database forward again when it starts.

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

Another OS user on the host is outside this gap. Dolt 2.2.0 always listens
on a loopback TCP port beside its socket, and any local user can connect to
that port. So the Dolt root user has a password. The server makes it at the
first start and keeps it in `beads/dolt/root.password`, mode 0600. The
server gives it to Dolt and to `bd` in their environment, never in argv. The
file is outside Dolt's data, and the server never prints it. When the file
is lost, the next start makes a new password and sets it in Dolt.
