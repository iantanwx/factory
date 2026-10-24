---
name: loop-reader-load
description: "Apply when reviewing or shaping code that is hard to follow. Count the layers between a question and its answer, and the hidden state a reader must hold. Collapse wrappers with one caller, and shrink mutable scope."
disable-model-invocation: true
---

<!--
Ported from skills/reader-load/SKILL.md in tickertown at commit
64e293f9ae2db7831d12462e76ffe1aa3deacd8c, the head of its main branch on 2026-10-23.

Tickertown recorded this fork line:

FORKED from cursor/plugins, and ours from here on.

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/principle-minimize-reader-load
  commit:   d7cde2b84eadbcd6fd890302c876f4436ccb6d82
  forked:   2026-09-08 upstream, forked 2026-09-28

Renamed from `principle-minimize-reader-load` to `reader-load`. The frontmatter `name` matches the
directory, because a skill resolves by that field.

Changed on purpose. Upstream links `principle-guard-the-context-window/SKILL.md`, which tickertown
does not have, so we cut the link rather than ship a pointer to nothing. Added why this matters
more here than in most repositories: every reader after the author is an agent starting cold.

Edit the copy in the loop's pack. The server writes `.claude/skills/` again at each start.
-->
# Reader load

Maintainability is the work a reader does to understand the code. Two things measure it, and they
move on their own.

1. **Layers to trace.** How many hops sit between the question and the answer.
2. **State to hold.** How much hidden or changing context the reader carries while reading.

Guard both. A flat file with fifty module-level variables is as hard as a six-layer stack.

Line count, cyclomatic complexity and tidy architecture are proxies for this. This is the thing
they stand in for.

## Why the loop cares more than most

Every reader after you is an agent that starts with no memory. The code reviewer opens your diff
cold. The implementer opens a ticket cold. Neither one asks you a question, because you are not
there. A layer that you would have explained in a sentence gets traced instead, or guessed.

## Collapse layers that do not pay

Inline a wrapper with one caller. Inline an adapter with one implementation. Inline indirection
added for a second case that never arrived.

A layer that repeats the methods and arguments of the layer beneath it adds hops and compresses
nothing. Collapse it.

A wide interface that hides little makes the reader learn the surface and the implementation.
Prefer a boundary that hides a real decision.

## Shrink the scope of state

In this order, best first:

1. A pure function that returns a value
2. A local
3. A field
4. Module state
5. A global

Derive a value where you need it rather than storing a copy and keeping the two in step. Two
values that must agree are a rule that lives in every place that writes either one.

## Name the invariant once

State the rule at the boundary, not in every caller. The reader learns it in one place.

## Before you add a layer or a field

Ask whether it lowers reader load somewhere else by at least as much as it adds. If you cannot
name the place it helps, do not add it.

## The test

Can a new reader answer both of these in under thirty seconds?

- Where does this value come from?
- What can change it?

If not, cut a layer or cut some state.
