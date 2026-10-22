# Operator checklist

`docs/operating.md` has the detail behind each step.

## Install

You need `gh`, logged in to an account that can read `slothware-labs/factorio`.

- [ ] Run:

  ```sh
  gh release download -R slothware-labs/factorio -p install.sh -O - | sh
  ```

  For an App owned by an organisation, end the command with
  `sh -s -- --github-owner <org>` instead. Without it, your personal account
  owns the App, and a private App installs only on its owner's
  repositories.
- [ ] In the browser, click **Create GitHub App**, then pick the project
      repositories and click **Install**. On a Tailscale host, any browser
      on the tailnet can open the link the installer prints.
- [ ] Run `loop doctor` and fix any line it fails.

The same command upgrades a host that already runs the loop.

## Add a project

- [ ] Make sure the repository's CI has run at least once on its default
      branch.
- [ ] In the project's clone, run `loop init`.

## Open items

- [ ] Run the evidence procedures when you are ready to spend harness quota:
      `docs/evidence/v1-e2e.md`, `github-queue.md`, `darwin-smoke.md`.
- [ ] Decide on the review items in `docs/open-decisions.md`.
