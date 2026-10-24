---
name: loop-boundaries
description: "Apply when writing validation, error handling, or framework wiring. Put the guards at the edge of the system, trust internal types, and keep the rules in pure functions."
disable-model-invocation: true
---

<!--
Ported from skills/boundaries/SKILL.md in tickertown at commit
64e293f9ae2db7831d12462e76ffe1aa3deacd8c, the head of its main branch on 2026-10-23.

Tickertown recorded this fork line:

FORKED from cursor/plugins, and ours from here on.

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/principle-boundary-discipline
  commit:   d7cde2b84eadbcd6fd890302c876f4436ccb6d82
  forked:   2026-09-08 upstream, forked 2026-09-28

Renamed from `principle-boundary-discipline` to `boundaries`. The frontmatter `name` matches the
directory, because a skill resolves by that field.

Changed on purpose: the examples were tickertown's own layers and its own narrowing helpers, so a
reader could open the file rather than translate an abstraction. We dropped upstream's "prompt
construction" and "scoring" applications, because nothing there did either.

The loop's port names no file of one project. A project's own layers and helpers are where a
reader finds this rule at work.

Edit the copy in the loop's pack. The server writes `.claude/skills/` again at each start.
-->
# Boundaries

Put validation, narrowing and error handling where data enters the process. Trust the types
inside. Keep the rules in pure functions, and keep the wiring thin.

Scattered validation is noise, and it gives false confidence. A check in the middle of a call
chain cannot say what it protects against, because the type already said the value was there.

## What counts as a boundary

Anything outside the process. A request body. A response from another service. A file. A command
line argument. An environment variable.

Data from a boundary arrives as `unknown`. Narrow it once, at the edge, into a domain type.

Use a parser that returns `unknown`, so no caller can skip the check by accident. Narrow one
field at a time.

## Inside, trust the types

No second check. No defensive fallback. If the type says a value is present, it is present,
because the boundary already proved it.

A repeated check deep in a call chain is a sign that the boundary did not narrow properly. Fix
the boundary instead of adding the check.

## Translate at the edge, both ways

Three layers apply this rule to a request.

| Layer | Speaks | Never |
| --- | --- | --- |
| API | The wire shape, and domain types | Applies a rule, or reaches the store |
| Service | Domain types only | Reads a request, or reaches the store |
| Repository | The stored shape, and domain types | Applies a rule |

A request shape must not reach a service. A stored record must not reach a service. Translate
even when the two shapes match today, because they match only until one side gains a field.

Do not re-export a transport type, a stored type or a framework type from a public surface. That
is the same leak, pointing outward.

## Keep the rules pure

A rule that needs no framework can be tested with no framework. That is the point. It is also why
you write the service before the store and before the wire.

## Where code lives

The rules above say which types cross a line. This says where the line is on disk.

Count the apps that use the logic. The count decides the answer.

| Apps that use it | Where it goes |
| --- | --- |
| One | Inside that app |
| Two or more | A shared package |

A store that two apps read shows the rule at work. Both apps read the store, so the schema and the
repositories belong outside both apps.

A client of a data provider shows the other side. When only one app calls the provider, the
provider client stays inside that app.

A package with one consumer is a guess about the future. The guess is not free. It costs a
manifest, a build reference and a dependency edge. It also makes the code harder to change,
because a package reads as a contract to the next agent. A directory inside an app does not.

Count the apps transitively. A package reached through another package is still shared.

## Two questions

**Is this data crossing a boundary right now?** If not, the check is redundant. Delete it.

**Can this be a pure function that the wiring calls?** If yes, pull it out.
