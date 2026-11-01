---
name: loop-verify-create
description: Write the verify skill of one app in a project. The skill states how an agent starts the app, drives it as a user does, keeps the evidence and stops it. Use it when a project with an app has no verify skill.
disable-model-invocation: true
---

<!--
Forked from pstack's create-verification-skill, and ours from here on.

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/create-verification-skill
  commit:   4dd1d05818d19792e7cd6e818f1667f95f7e1dbe
  forked:   2026-11-01

Recorded so that a later reader can diff against upstream and see what we changed on purpose.
This is not a dependency we track, and we do not owe it updates.

Changed on purpose, 2026-11-01, for the QA stage (docs/adr/0014-qa-stage.md):
- The skill goes to .claude/skills/verify-<app>/ in place of .cursor/skills/verify-<app>/.
- The front matter may hold paths, which the QA plan reads to pick the apps of a ticket.
- Each app lets three runs drive it at once, because the QA stage runs three at once.
- The evidence goes to a directory that the run names, and never into the checkout.
- An unattended run asks nobody. It reports what it cannot observe.
- The feature map example is one fenced file, in place of the reference directory.
- The prose follows the pack's linters.
-->

# loop-verify-create

This skill writes the verify skill of one app. A verify skill is the scripted way to drive the
real app and prove what it does. An agent reads it cold, in the middle of a task, and has never
seen the app. So write for that agent, and not for a person.

The QA stage reads each verify skill. It starts the app at the reviewed head, drives it toward the
ticket's goal, and keeps the evidence. The implementer reads it too, to show a change working. A
project with no verify skill gets no QA of its app.

## 1. Read the repository first

Answer each question from the files. Ask a person only what no file shows. A run with nobody present
asks nobody: it names each fact it cannot observe in its report, and it stops there.

- **The surface.** What does a user touch? A web page, a command line, a terminal screen, a
  desktop app, a mobile app, an API, or a library. A repository can hold several apps. Write one
  skill for each app.
- **The start.** How does the app start on this host? Prefer the repository's own command: a
  package script, a `Makefile` target or the quick start of the `README.md`. Note the ports, the
  environment, the seed data and the sign-in.
- **The drive.** How can an agent act on the app? Use a harness that the repository already holds
  first, such as browser specs, terminal scripts or endpoints that `curl` reaches. Only then choose
  a generic recipe: a browser for a web or desktop app, a terminal session for a command line, and
  plain HTTP for a service.
- **The evidence.** What can a run keep? Screenshots, terminal transcripts, response bodies, logs,
  exit codes and stored rows.
- **Isolation.** Can three runs drive three copies of the app on one host at once? The QA stage
  runs three at once. Give each copy its own port, data directory and browser profile. When the
  app cannot run three copies, say so in the skill, and name the lock each run takes. A run that
  waits on a lock is better than a run that corrupts another run's copy.

When the checkout does not build or start, fix that first, or report it with the exact error. A
skill written against a broken base teaches wrong steps. When a missing file that no drive
needs blocks the start, the skill may create it. Mark it as scaffolding of the skill, and
delete it in cleanup.

## 2. Write the skill

Write `.claude/skills/verify-<app>/SKILL.md`. `<app>` is lower case: letters, digits and hyphens.
Each fact in it comes from what section 1 found. Leave no placeholder.

The front matter holds three fields:

- `name`: `verify-<app>`.
- `description`: the app, its surface, and when an agent reads the skill. With no front matter,
  no harness lists the skill.
- `paths`: optional. A list of globs of the files of this app, in the syntax of `ui.paths` in
  `.loop/config.yaml`. The QA stage drives this app on a ticket when one glob meets a file the
  ticket may change. With no `paths`, the QA stage drives the app on every ticket.

```yaml
---
name: verify-web
description: Drive the web app in a browser, as a signed-in user does. Read it to prove a change to a page.
paths: ["apps/web/**", "packages/ui/**"]
---
```

Then write these sections, in this order:

- **`## Launch`.** The exact command that starts the app for a drive, and the sign that it is
  ready, such as a log line or a prompt. Start it from an empty data directory, with each
  migration and the seed data. Ready means a user can use it, not only that a process answers. A
  database can answer while its first scripts still run. Name the free port and the data
  directory of each copy. A short-lived command line has no server: build it once, then run each
  drive in its own terminal session.
- **`## Doctor`.** One command that writes nothing and answers one question: is this copy fit to
  drive? The process is up, the build is the right one, the port is ours and the sign-in holds. A
  run calls it first, and again whenever the app surprises it.
- **`## Drive`.** The recipe of the harness, with real selectors and commands from this
  repository. Prefer stable handles: roles and accessible names, data attributes, prompt strings
  and routes. Never use screen coordinates or tab order.
- **`## Evidence`.** What a run keeps, and where. A run passes the directory, so the skill writes
  only there, and never into the checkout. Hold these standards. Drive the real path of a user,
  and never an internal setter or an endpoint for tests. Keep the action and the state after it,
  not only the last screen. Verify each side effect next to what the screen shows: a file written,
  a row stored, a message sent. Use a mock only where a production boundary already cuts the
  outside system off. A dry run can still reach the network, so observe what it does.
- **`## Cleanup`.** How a run stops each copy it started. Never stop a process by its name. Stop
  only the process the run started. Cleanup deletes the copies and their scratch state, and never
  the evidence.

Each helper script that the skill ships is executable, and the skill shows the command that calls
it. A helper that the reader must reverse-engineer is no helper.

Seed only test data and test accounts. A screenshot goes into the QA report, and a person on
GitHub reads it. So no screen may show a real key, a real account or a real person's data.

## 3. Seed the feature map

Write `.claude/skills/verify-<app>/features/README.md`, and one file for each feature a user
reaches. Start with the three to five that the routes, commands, menus or documents name first.

The README holds the start state that every recipe assumes, the rules of driving, the standard of
proof, and one line for each feature file. Each feature file answers four questions from the
user's point of view: what the feature is, how a user reaches it, how the harness drives it, and
which end state proves that it works. Use exactly these four sections, in this order:

```markdown
# Create a note

Create note lets a user save a titled note from the browser, and see it again from the list.

## Sub-features

- `create-open` opens a blank editor from the toolbar.
- `create-save` keeps a title and a body.

## How to get to it (user POV)

- Choose the `New note` button in the toolbar.

## Driving it with verify-notes

Preconditions:

- The app is healthy at the URL that the Launch section printed.
- No note is titled `Release checklist`.

- **Open the editor.** Choose `New note`. A form named `Note editor` appears.
- **Save the note.** Fill `Title` with `Release checklist`, and choose `Save note`. A status named
  `Note saved` appears.
- **Prove it.** Open `All notes`, then `Release checklist`. Keep a screenshot of the editor with
  both values in the evidence directory.

## Gotchas

- A save status alone proves nothing. Open the note again from the list.
```

When the seed data lacks what a journey needs, the journey reports a failure. Put the reason in
that feature's `Gotchas`. Never weaken the journey until it passes. The map is the source that
each later drive reads. A proof that drives one entry point is not whole when the map lists more.

## 4. Prove the skill before you hand it over

Follow the skill once from end to end. Start the app, run the doctor, drive one feature of the
map, keep the evidence and clean up. One feature is enough. The map lets later runs cover the
rest. After cleanup, look for the evidence where the skill says it is. A cleanup that deletes the
proof fails this step. Fix each step that fails. Run the skill's cleanup after each failed
attempt too, so no failed attempt leaves a process or a port behind.

A skill that no agent ever ran is a draft, and not a result. When you proved it somewhere other
than the real target, such as a container in place of the host, list each step that the
stand-in did and the target will not. Those steps stay unproven until a run on the target covers
them.

## 5. Land it

Commit the skill and its map in the change that needs them, by the commit rules of your runbook.
Name `../loop-verify-maintain/SKILL.md` in the change's description as the way to keep the map
true as the app changes.
