---
name: loop-verify-maintain
description: Keep the verify skill of one app and its feature map true to the app. Read each feature from the source, drive each one live, and land only the fixes a drive proved. Use it when a change touches how the app starts or how a user reaches a feature.
disable-model-invocation: true
---

<!--
Forked from pstack's maintain-verification-skill, and ours from here on.

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/maintain-verification-skill
  commit:   4dd1d05818d19792e7cd6e818f1667f95f7e1dbe
  forked:   2026-11-01

Recorded so that a later reader can diff against upstream and see what we changed on purpose.
This is not a dependency we track, and we do not owe it updates.

Changed on purpose, 2026-11-01, for the QA stage (docs/adr/0014-qa-stage.md):
- The target is .claude/skills/verify-<app>/ in place of .cursor/skills/verify-<app>/.
- The fixes land in the pull request of the run's item, and not in a pull request of their own.
- A run with several targets and nobody present takes the one its change reaches.
- A product fault goes to the run's report, and never into the map.
- The prose follows the pack's linters.
-->

# loop-verify-maintain

A feature map goes stale as soon as the app changes. This skill keeps a verify skill true: the
skill that `../loop-verify-create/SKILL.md` writes, or any skill of the project with a start
section, a drive section and a feature map. The unit of rigor is the feature, not each sentence.
Read every feature file against the source, and drive every feature live. Do not run a command
for each bullet.

## The outcome

Pick one, and name it in your report:

- **clean.** Each feature got a source read and a live drive, and nothing needs a fix. Commit
  nothing.
- **changed.** The run commits the fixes that a drive proved.
- **blocked.** The pass could not cover every feature, or a proven fix could not land safely. Name
  exactly what blocked it.

## What you may edit

Edit only the directory of the verify skill: its `SKILL.md`, its `features/` and each helper
script it owns. Never edit product code in this pass. When the map describes something the app no
longer does, that is drift in the map or a fault in the product. Fix drift in the map. Report a
product fault, and never hide it in the map.

## The pass

0. **Find the target.** Find the verify skill to keep: a skill under `.claude/skills/verify-*/`
   with a start section, a drive section and a feature map. When there are several, take the one
   whose `paths` meet the files your change touches. When none fits, stop. Write a new one with
   `../loop-verify-create/SKILL.md` instead, and invent no target.
1. **Keep the index clean.** Read the README of the map, and list its sibling files. Fix each
   entry that is missing, extra, doubled or dead. Keep it light, and generate no inventory.
2. **Read each feature from the source.** For each feature file, answer one question from the
   source: how does this feature work for a user? Flag each likely drift with the file and line
   that shows it. Write one recipe that drives it live. You may give each feature to a subagent
   that only reads, by the subagent rules of your runbook. A subagent never drives the app, and it
   never edits a file.
3. **Merge the readings.** Each feature file needs one summary. Merge the recipes that overlap
   into as few app states as you can. Look again at each drift that a reading cited, and do not
   prove a clean claim again. Look over the recent changes for a surface that a user reaches and
   the map omits. Name a concrete source path before you call one missing.
4. **Drive every feature live.** Do this even when the source looks clean. You alone drive the
   app. Follow the start model of the skill. Drive a server or a screen as one copy, one feature
   after another. Give a short-lived command line a new session for each drive. Hold three rules
   for the whole pass, whatever fails:
   - Never drive a copy that surprised you until its doctor passes again. Run the doctor before
     the first drive, on each new session, and after each failed drive. When the doctor cannot
     see the fault, such as a stuck screen on a healthy process, reset the state or start a new
     copy.
   - Each piece of evidence so far outlives every cleanup. Look for it where the skill says it
     is, and do not assume it is there.
   - Nothing that a drive started outlives its use. Clean up after each failed attempt, whether
     the session is stuck, gone or shared. On a shared copy, clean up what you left, and not the
     copy.

   A doctor that fails because the skill drifted is drift. Fix it within what you may edit, and
   try once more. Start again only what the fix touched. Then call the pass blocked. A feature
   that a user cannot reach is unreachable only with the exact precondition and the route you
   tried, such as a sign-in, a plan, an operating system or outside state. When the map omits that
   precondition, that is drift. Drive each fix of the harness live again before it lands. The last
   cleanup comes after the last drive, so nothing outlives the run. The evidence stays.
5. **Sort each finding.** A wrong or missing description of what a user sees is drift in the map:
   fix it. A behavior that works and that the harness cannot drive is a gap in the harness: fix
   it. Each script that a fix adds is executable, and the skill shows the command that calls it.
   A behavior that is broken in the app is a fault in the product. Report it, and keep it out of
   the fixes.
6. **Land it, or stop.** On changed, read each changed file again, and commit the fixes in the
   change of your run's item. On clean or blocked, commit nothing. Report the outcome and what
   the pass covered.

Keep short notes of the pass outside the checkout, in a directory from `mktemp -d`: each feature
covered, each unreachable precondition, each drift you proved, and the outcome. Do not commit them.
