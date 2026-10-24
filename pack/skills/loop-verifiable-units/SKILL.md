---
name: loop-verifiable-units
description: "Apply to multi-step work such as a sweep or a migration, and to how you stack commits. Break work into units that each end in a state you can check. Check each one before you start the next."
disable-model-invocation: true
---

<!--
Ported from skills/verifiable-units/SKILL.md in tickertown at commit
64e293f9ae2db7831d12462e76ffe1aa3deacd8c, the head of its main branch on 2026-10-23.

Tickertown recorded this fork line:

FORKED from cursor/plugins, and ours from here on.

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/principle-sequence-verifiable-units
  commit:   70b2dc8b4b85c8d5648624ca40d692c421fff32f
  forked:   2026-09-23 upstream, forked 2026-09-28

Renamed from `principle-sequence-verifiable-units` to `verifiable-units`. The frontmatter `name`
matches the directory, because a skill resolves by that field.

Changed on purpose. Upstream closes by naming two sibling skills, `prove-it-works` and
`build-the-lever`, which this repo does not have. We cut those pointers rather than ship
references to nothing, and pointed at our own equivalents instead. Added the worked example from
tickertown's own build order, and the wide-refactor case from its decomposer runbook.

Edit the copy in the loop's pack. The server writes `.claude/skills/` again at each start.
-->
# Verifiable units

Order work as small units. Each one ends in a state you can check. Do not start the next until
the current one is green.

A break found at the unit that caused it is cheap. Ten units later, the same break hides under
the work you built on top of it.

## While you work

Start from a clean base first. Otherwise every check measures against a baseline that already
had the fault. Bring the base in with `loop branch sync`, and never rebase a branch that has a pull
request.

Then repeat one bracket: known-good state, one change, run the check, continue.

In a sweep or a migration the per-unit check costs almost nothing, because you run the same
command each time. Run it anyway. The temptation to batch ten edits and check once is exactly
how a fault gets buried.

## When you deliver

Stack the commits in the order that proves the work.

The usual shape is the failing test first, then the code that passes it. The implementer's runbook
holds that rule for the loop. Other shapes prove other things:

- A deletion before a reshape, so the reshape is smaller to read.
- A baseline captured before the change, so the difference is visible.
- The scaffold before the thing built on it.

Each commit must land on its own. Read in order, the sequence is the argument.

## The build order is this rule applied to one ticket

Sequence one ticket into five units, and take each from red to green: the domain, the business
rules against a fake store, the store, the wire, and last the integration test across all of them.

Integration last is the point. An integration test that fails tells you the path is broken. It
does not tell you which layer broke it. The layer tests do.

## The wide refactor is this rule applied across tickets

A mechanical change that reaches the whole codebase cannot land green in one unit. So it becomes
three stages, and the decomposer's runbook states them: add the new form beside the old,
migrate the callers in batches, then delete the old form. Each batch stays green, because the old
form still exists.

## Why the check has to be real

A unit that ends green tells you nothing if the check had no way to fail. Every gate here runs
on that idea, and `../loop-test-behavior-not-implementation/SKILL.md` holds the full
rule: ask whether the test would still pass if every function it imports returned `undefined`.
