---
name: loop-commit
description: "What a commit message in this repository says. Read it before you write one. The gate counts characters and paragraphs. This skill holds the judgement no gate can hold."
disable-model-invocation: true
---

<!--
Ported from skills/commit/SKILL.md in tickertown at commit
64e293f9ae2db7831d12462e76ffe1aa3deacd8c, the head of its main branch on 2026-10-23.

Tickertown recorded two sources, and what it took from each.

  1. ce-commit, a vendored Claude Code plugin skill.
     upstream: compound-engineering-plugin/skills/ce-commit/SKILL.md
     read:     2026-10-01
     Taken: step 5's test for a subject, "imperative and names the outcome, not the file list".
     Taken: write the message to a file, then run `git commit -F`.
     Left: every other step. It branches on a person's answer, and no person watches a timed run.

  2. technical-writing, from cursor/plugins.
     upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/technical-writing
     read:     2026-10-01
     Taken: one sentence. "The codebase is the word list."
     Left: the Diataxis modes and the four style layers. `../loop-unslop/SKILL.md`,
     `../loop-bro/SKILL.md` and the `prose` gate of `loop gates` already hold what the loop
     wants from them.

Edit the copy in the loop's pack. The server writes `.claude/skills/` again at each start.
-->
# Commit

A reader meets your commit in `git log --oneline`, one line among forty. That line is all most
readers get.

## The gate holds the counts

The `commits` gate of `loop gates` refuses a message that breaks a rule a machine can decide. It
holds the Conventional Commits prefix, the subject cap, the mood rule and the one-paragraph rule.
It prints each limit when it refuses. `loop gates --fast` runs it. Read the refusal for the
numbers, and never copy one into a document.

The mood rule refuses a description that opens with a noun phrase. Read the whole refusal before
you argue with it. It names the word that the rule read and the shape that it refused. The rule
reads only the first word, so a subject that it accepts can still fail every rule below.

This skill holds what the gate cannot decide. The gate reads the first word of a description. It
cannot read the rest, so it cannot tell you that a subject carries two arguments. It cannot tell a
body that earns its place from a body that restates the diff.

## Name the change, not the system

Ask one question. What did you change?

Answer it with a verb first. That answer is the subject. Aim for fewer than 50 characters, which is
git's own recommendation.

Test the subject by one reading. Put `This commit will` in front of it. A subject that fails that
reading is a report, and the gate refuses it.

- Write `run the sweeper hourly`. Never write `the sweeper runs hourly`.
- Write `add the login-code template`. Never write `changes to the email templates`.
- Write `refuse a package import under scripts/`. Never write `a package import is refused`.

Write `add`, not `added`.

Give the subject one verb. A subject with two verbs carries an argument. A reason belongs in the
body, and a design belongs in the pull request. The gate counts no verbs, so this paragraph is the
only rule that holds this one.

A person stated this rule twice in tickertown, the project this skill comes from. The first time
they asked for short subjects, and the gate then counted characters. The second time they read a
65-character subject and asked what it meant.

Three subjects name a file list and say nothing:

- `chore(sdlc): update sweep-poll.ts`
- `fix(sdlc): changes from review`
- `feat(sdlc): add a new check and some tests`

The codebase is the word list. Write the real file, the real flag and the real command name. A
reader greps for `sweep-poll.ts`. Nobody greps for `the polling module`.

Cut the words that do no work. `this commit`, `various`, `some` and `also` go first. A subject that
holds `and` twice holds two commits. Split it.

## The body is the exception

Write no body. That is the honest default, and most commits in this repository need none.

Write a body when one of these three is true.

- A reader would ask why, and the subject cannot answer.
- You rejected an obvious approach, and a later reader would try it again.
- The change rests on a measurement, and the number belongs with the change.

Never restate the diff in a body. A reader who wants the diff reads the diff.

Write one paragraph. The full argument belongs in the pull request body, in an ADR or in a plan.
Those documents hold a heading, a table and a link, and a commit message holds none of them.

## Pick the type

Conventional Commits defines `feat` and `fix`. The `commits` gate names the full list that it
accepts when it refuses a prefix.

Two types fit often. These four rules settle it.

- The change adds a capability nobody had. Write `feat`.
- The change repairs behaviour that was wrong. Write `fix`.
- The change edits a document alone. Write `docs`.
- The change moves code and changes no behaviour. Write `refactor`.

Pick `fix` when `feat` and `fix` both fit. A repair is the smaller claim, and the smaller claim is
the safer one.

A new gate is a `feat`, because the repository could not refuse that fault before.

## Pick the scope

A scope names the subsystem a reader would search for. It does not name a directory path.

Read `git log` for the scopes this repository already uses:

```bash
git log --format='%s' -200 | sed -nE 's/^[a-z]+\(([a-z-]+)\):.*/\1/p' | sort | uniq -c | sort -rn
```

Use a scope the command reports. Add a new one when your change reaches a subsystem that none of
them names. The gate checks the shape of a scope and never the word, because a closed list would
refuse a new package on the day somebody adds one.

A scope is optional. Leave it out when the change reaches the whole repository.

## Write the message to a file

```bash
dir=$(mktemp -d)
git commit -F "$dir/commit-message.txt"
```

A shell mangles a `$`, a quote and a backtick inside `-m`. Tickertown paid for that six times. The
loop's own write verbs read a file for the same reason: `loop epic decompose --file`,
`loop room apply --file` and `loop fault report --file`.

Write the file in a directory from `mktemp -d`, outside the checkout. An untracked file inside the
checkout reaches the next `git status`, and an agent commits it.

End the message with the `Co-Authored-By` trailer when your harness gives you one. A trailer is a
footer and not a paragraph, so it costs you nothing against the one-paragraph rule.

## Two commits from tickertown

**One that reads well.** 38 characters, no body.

```
docs(spec): answer spec review round 1
```

It names the outcome. A reader knows what changed and why, and the review thread holds the rest.

**One that does not.** 97 characters, six paragraphs.

```
feat(sdlc): the sweeper runs every hour, and a finding it cannot act on still costs one run a day
```

This subject opens with `the`, so the gate refuses it today. It also carries the whole argument.
The second clause is the reason, and a reason belongs in the body. The six paragraphs then restate
the design, the minute the timer took and the test that holds it.

The same change, inside the rules:

```
feat(sdlc): run the sweeper hourly, and report daily

`sweep-poll.ts` starts a run on any tick for work this stage may act on, and at
`REPORT_HOUR` for everything else. One tick a day left lost work invisible for
as many as 72 cycles, and a higher rate alone would have bought 24 runs a day
for one finding that waits on a person.
```

The subject is 51 characters, and it opens with a verb. The body is one paragraph, and it holds the
two numbers that the decision rests on. The timer minute, the test and the `boardAnswer` contract
move to the pull request body, where a reviewer reads them beside the diff.
