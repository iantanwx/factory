---
name: loop-least-code
description: "Apply when refactoring, judging the size of a diff, or reaching for an abstraction, a layer, or a new field threaded through several files. Prefer deletion and the smallest change that solves the problem."
disable-model-invocation: true
---

<!--
Ported from skills/least-code/SKILL.md in tickertown at commit
64e293f9ae2db7831d12462e76ffe1aa3deacd8c, the head of its main branch on 2026-10-23.

Tickertown recorded this fork line:

FORKED from cursor/plugins, and ours from here on.

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/principle-laziness-protocol
  commit:   e8d856f0273b42ebafe0ec3546bd645709e7c1b0
  forked:   2026-09-28

Renamed from `principle-laziness-protocol` to `least-code`. "Laziness protocol" names a mood, and
the skill is about diff size. The frontmatter `name` matches the directory, because a skill
resolves by that field.

Changed on purpose: upstream ends on "a human developer would find it exhausting to maintain",
which is a feeling rather than a check. Replaced with three questions a reviewer can answer from
a diff. Added the tie to the coding standards' limits, which are the enforced half of this.

Edit the copy in the loop's pack. The server writes `.claude/skills/` again at each start.
-->
# Least code

Get the most result from the least code.

## Delete before you add

When a ticket says refactor or improve, look for what you can delete first. A change that
deletes more than it adds is usually the right one.

## Keep the call chain flat

If answering one question means reading more than three files, flatten it.

A wide interface that hides real work is not a deep chain. A wrapper with one caller is. So is an
adapter with one implementation. Inline both.

## Decide once

Do not make the same choice in two places. Put the decision in one place, and pass the result.

A set of states that a poller reads is the example. When each site writes the states as its own
string literal, the choice lives at each site. Make it one list, and refuse anything else.

## Keep the diff small

Make the smallest change that solves the problem. Fewer lines beat tidy scaffolding.

If a ticket asks you to thread a new field through types, schemas and several layers, stop. Look
for a shorter path first. Threading is the change that looks small in each file and is large in
total.

## Fix the small leaks

Delete a pass-through, a leaked internal shape, or a duplicated choice as soon as you see it.
Each one is cheap now and expensive once three callers depend on it.

## Three questions a reviewer can answer

Upstream ends on whether a developer would find the code exhausting. That is a feeling, and a
reviewer cannot check it. These come from the diff:

1. Does this change delete anything? If it only adds, say why nothing could go.
2. How many files must a reader open to answer the question this code answers?
3. Does any new layer delete a branch, a duplicated rule, or an invalid state? A layer that
   deletes none of those is cost with no return.

A project's coding standards hold the part a linter enforces, such as lines per function,
complexity, nesting depth, parameters and nested callbacks. Those are limits, not targets. This
skill is the part a linter cannot check.
