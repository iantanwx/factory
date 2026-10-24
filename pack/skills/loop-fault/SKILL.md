---
name: loop-fault
description: Start this when the loop CLI, the loop server, a loop runbook or a loop gate misbehaves. It applies in any project and any session, such as a run, a room or the coordinator. It applies outside a run too, in a person's or an agent's own session in a client project. It states how to file a loop fault with `loop fault report`.
---

# loop-fault

A loop fault is a fault in the loop, not in the project. The loop's maintainers read each report,
and each fix ships in a release. A fault that nobody reports stays in the loop, and the next session
meets it again.

This skill applies in every session that can run `loop`: an unattended run, a room, the
coordinator's session, and your own session in a client project outside every run. A run also
follows section 7 of `../loop-sdlc-turn/SKILL.md`, which adds the steps that only a run does.

## 1. What counts as a loop fault

Report a loop fault in any of these cases:

- The CLI or the server breaks its own contract. For example, an envelope contradicts the schema,
  or a write that exited 0 did not change the state.
- A refusal names no verb that works. Each verb in `allowed_next` is refused too, or the list is
  empty, and no step of your runbook or task remains.
- A runbook or a task needs a verb that does not exist in `loop schema`.
- A loop runbook or a gate rule is wrong. For example, a runbook step contradicts the schema, or a
  gate refuses work that its own rule allows. Name the rule and what you expected the loop to do.

These are no loop fault:

- A test, a build or a gate of the project fails because of the project's code. Fix the code.
- A refusal that names a verb that works. Read `allowed_next`, and use that verb.
- A rule of the project that you want to change. The project's own files hold it.

## 2. Look for the fault first

Read the faults that the loop holds with `loop fault list` before you write a report:

```bash
loop fault list --status open
loop fault list --status acknowledged
loop fault show F-<n>
```

`loop fault show F-<n>` prints a fault's report, its resolution and the release this host runs.
When an open or an acknowledged fault names the same command and the same failure, use its id.
Report no second fault. When a fixed fault names it, compare the release that fixed it with the
release this host runs. A host that runs an older release needs the upgrade, not a new report.

## 3. Choose where you run the command

The CLI acts as the caller that it finds. It looks for `.loop/token` in the current directory and in
each directory above it. A run's checkout and a room's checkout hold one.

- **In a run or a room:** run the command in your own checkout. The report then names your run.
- **In your own session in a client project:** run the command in the project's clone, or in a
  shell outside every loop checkout. With no `.loop/token` above it, the CLI acts as a host caller.
- **Never run the command inside another run's checkout.** The CLI would then act as that run, with
  its token, and the report would name the wrong caller.

## 4. Write the report, then file it

Write the report to a file in a new directory from `mktemp -d`, never in a checkout. A file in a
checkout can land in a diff. Put these in the report:

- the exact command you ran, and the directory you ran it in
- the envelope it printed, in full
- what you expected, and the rule or the schema line that says so
- for a wrong runbook or gate rule: the file, the rule, and the correction that showed it

Never put a secret in a report. Write the name of a secret, never its value. The server refuses a
report that holds a secret with `SECRET_IN_OUTPUT`. Delete the value, and report again.

Then file it:

```bash
loop fault report --file <report file>
```

The answer names a fault id, `F-<n>`, that never changes. Read its status later with
`loop fault show F-<n>`.

## 5. Continue, and name the fault

Never work around a loop fault in silence. The next session meets the same fault, and only the id
tells it that the loop knows.

- When the fault stops your work, stop. A run finishes as section 7 of `../loop-sdlc-turn/SKILL.md`
  states. In your own session, tell the person you work with the fault id and the step that waits.
- When the fault does not stop your work, continue. Name the fault id where the work continues:
  in the pull request body, in a note, or in your answer to the person.

## 6. Failures

- You meet a loop fault, you work around it, and you report nothing.
- You file a second report for a fault that is already open.
- You run `loop fault report` inside another run's checkout.
- You put a secret, or a value from `.env`, in the report.
