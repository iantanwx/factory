---
name: loop-qa
description: The QA stage's runbook. Drive the app of one reviewed ticket at its head as a user does, read its screens, and report one verdict with its evidence. The server starts this run and names the ticket in the brief.
---

<!--
Forked from two sources, and ours from here on.

The scope of the drive, the matrix of variations and the follow-through of each side effect come
from ce-dogfood:

  upstream: https://github.com/EveryInc/compound-engineering-plugin/tree/main/skills/ce-dogfood
  commit:   3449230705a508229a5a298671e9031e489f0278

The goal drive, the bound of 25 steps, the read of at most 8 screens at 1280x800 and 390x844, and
the detector come from tickertown's QA code, packages/qa/src/drive, audit and detect:

  upstream: https://github.com/slothware-labs/alpha/tree/main/packages/qa/src
  commit:   2fa858ca001eed9479ca60b4fe82dd1989afefe0

  forked:   2026-11-01

Recorded so that a later reader can diff against upstream and see what we changed on purpose.
This is not a dependency we track, and we do not owe it updates.

Changed on purpose (docs/plans/factorio-nhb5-qa-stage-spec.md, Decisions): the run writes no
commit and fixes nothing, where ce-dogfood fixes and commits. A fault that the merge base shows is
a bug, and not a fail. The server computes the plan and the bounds, and derives the verdict from
the report. The app comes from the project's own skill, and no path names one project.
-->

# QA

You drive the app of the one ticket that the brief names, at the head that code review passed.
You report what a user meets there, with the evidence. Nobody is present in this run. You change
no product code, and you write no commit.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-sdlc-triage/SKILL.md`, before you escalate anything.
4. `../loop-test-behavior-not-implementation/SKILL.md`, for what proves a behaviour.
5. `.loop/policy/code-reviewer.md` in the project, when it exists. It names the project's
   commands and standards. Where it and this runbook differ on a project rule, follow the policy.
   Where they differ on a loop rule, follow this runbook.

Two verbs of this runbook take `loop` in front of them: `qa plan` and `qa report`. A bug that this
run files takes the source `qa`.

## 1. Read the ticket and bring in its head

Read the item in the brief, then run `loop pr show` and `loop pr diff`. Run `loop branch sync`,
then the `fetch` command of its `item` part. Run `git switch -C <branch> <sha>` with the two values
it names. That head is the reviewed head. The report names its sha.

Read the diff as ce-dogfood does. Note each change that a user can see: a new screen, a changed
behaviour, a new route or command, a changed flow of data. Each one is a journey to drive.

## 2. Read the plan

Run `loop` with the verb `qa plan` in the checkout. It prints four things:

- `drive`: the apps to drive. The server picks them from the paths of each `verify-<app>` skill
  and the ticket's "May change" list.
- `visual`: whether you read the screens. It is true when the brief holds a design skill.
- `design_skill`: the design skill you read the screens with.
- `bounds`: the most steps, variations, minutes and screens of each app, and the two widths.

The plan is the server's. Drive each app it names, and no other. When the plan reports no
`verify-<app>` skill, send a report whose `verify` is `missing` and that holds no check, by
section 6. Then finish `done`.

## 3. Drive each app toward the goal

Take one app of `drive` at a time. Read its `.claude/skills/verify-<app>/SKILL.md` and its
feature map at the reviewed head. Then:

1. Make an evidence directory with `mktemp -d`. Never write evidence into the checkout.
2. Start the app by its `Launch` section, and run its `Doctor`. Never drive a copy that another
   run started. When the app starts at neither head, section 5 states what you do.
3. Derive the goal from the ticket's title, its contracts and its "Done when" line. Write it as
   one sentence that a user would say, such as "Save a strategy with two rules".
4. Drive toward the goal as a user does, by the `Drive` section and the feature map. Take one
   step at a time, and stop at the step bound of the plan. Keep the action and the state after it
   for each step.
5. Follow each side effect to its end state. A record that the app saved appears where a user
   looks for it. A message reaches the right person, and its link lands on the right place.
6. Then try the variations that the change reaches, each inside the variation bounds: an empty
   input, an invalid input, a reload in the middle of the flow, and a return by the back control.
7. Run the `Doctor` again after each surprise. Clean up each copy by the `Cleanup` section when
   the app is done.

A goal that you do not reach inside the bounds is a `fail` finding. So is a dead end, an error that
the screen shows, a value that differs after a reload, or a side effect that never arrives. Each
finding names its evidence files and the steps that show it.

## 4. Read the screens

Do this only when `visual` in the plan is true. For each app you drive, read each screen that the
change reaches, and no more than the screen bound of the plan. Read each one at both widths of the
plan. Read it with the design skill that the plan names, and run its detector when it ships one.
Keep a screenshot of each screen at each width.

A visual finding is `fail` in these cases:

- It breaks a rule of the design skill.
- Text is cut or covered.
- Two elements overlap.
- A control is out of reach.
- An element leaves the screen at one of the two widths.

Every other visual finding is a `note`.

## 5. Place each fault at the merge base

A fault fails this ticket only when its change made the fault. So try each `fail` finding at the
merge base before you report it:

```bash
W="$(mktemp -d)/base"
git worktree add --detach "$W" "$(git merge-base HEAD origin/<base>)" || exit 1
```

Start the app in that checkout by the same skill, and try the steps of the finding again. Name the
checkout by its path, as the turn skill states.

- **The merge base shows the fault too.** It is a bug, and it does not fail this ticket. File it
  with `loop work create --kind bug` and the source `qa`. Name the screen, the steps and the
  evidence in its body. Take the finding out of the check, and name the bug in the report.
- **The merge base does not show the fault.** It stays a `fail` finding.
- **You cannot tell.** It stays a `fail` finding. A regression that merges costs more than one
  rework.

One run files at most 3 bugs. When you find more, put the rest in `notes`.

When an app starts at neither the head nor the merge base, file one bug that names the failed
start and its output. Report that app's checks as `unstartable`, and name the bug. When the app
starts at the merge base and not at the head, report a `fail` finding.

Clean up each copy that you started at the merge base. Then delete the checkout with
`git worktree remove --force "$W"`.

## 6. Send the report

Write the report into a file in a directory from `mktemp -d`, outside the evidence directory. Write
one check for each app of `drive`, and one visual check for each such app when `visual` is true:

```json
{
  "head": "<the reviewed head sha>",
  "verify": "present",
  "checks": [
    {"kind": "fuzzy_uat", "app": "web", "result": "pass", "goal": "Save a strategy with two rules",
     "steps": 14, "minutes": 6, "screens": 0, "findings": []},
    {"kind": "visual", "app": "web", "result": "fail", "goal": "", "steps": 0, "minutes": 0, "screens": 3,
     "findings": [{"summary": "The save button leaves the screen at 390x844", "severity": "fail",
                   "evidence": ["web/save-390.png"], "steps": ["Open the strategy editor at 390x844"]}]}
  ],
  "bugs": [],
  "notes": ""
}
```

A check's `result` is `pass`, `fail` or `unstartable`. It is `fail` when one of its findings is
`fail`. Each evidence path is relative to the evidence directory. Keep no screenshot that shows a
key or a real person's data. The server scans each text file for a secret, and it cannot scan an
image.

Run `loop` with the verb `qa report <item> --head <sha> --file <report.json> --evidence <dir>`.
The server computes the plan again, checks the report against it, and derives the verdict. It
refuses a report that leaves out a planned check, adds an unplanned one, claims more than a bound,
or names a bug list other than the bugs this run filed. On a refusal, read `code` and `hint`, fix
the report, and send it again.

Then clean up each copy you started, and finish with `loop run finish --outcome done`.

## 7. Stop when a stop condition holds

- **You cannot finish a report.** Finish with `loop run finish --outcome failed`, and put what
  stopped you in the failure note. The ticket stays in `in_qa`, and the next run takes it.
- **A secret looks missing.** Run `loop secret check <name>` first. Put its output in the finding.
- **A question has no answer in the item, the documents or the skill.** It is a finding. Score it
  by `../loop-sdlc-triage/SKILL.md`, then escalate or decide.

## 8. Never

- Never change product code, never commit, and never push.
- Never fail a ticket for a fault that the merge base shows.
- Never weaken a goal so that the drive reaches it.
- Never drive a copy of the app that this run did not start.
- Never report a check that the plan does not name.
