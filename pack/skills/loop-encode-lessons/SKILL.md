---
name: loop-encode-lessons
description: Turn a rule that must be remembered into something that fails. Read this when you write an instruction you wrote before, or correct something you corrected before.
---

# loop-encode-lessons

A rule that must be remembered will be forgotten. Prose asks the reader to notice it, remember it
and agree with it. A gate asks nothing. This matters more when agents run with no person present
to catch a miss.

Start this skill in either case:

- You write an instruction that you already wrote somewhere else.
- You correct something that you corrected before.

`../loop-reflect/SKILL.md` starts this skill when a person corrects an agent twice.

## 1. The ladder

Pick the strongest rung that holds the rule. Agents copy what the code around them already does,
so a weak guard becomes the template for the next one.

1. A type that cannot hold the bad value.
2. A gate that fails CI. The project's `.loop/config.yaml` lists its gates.
3. A lint rule, with a baseline that only shrinks.
4. A check script with an exit code.
5. A test that fails without the change.
6. One helper, so there is one way to do the thing.
7. A sentence in a document.

Rung 7 is the fallback, not the default. When a rule needs judgement, say so, and add an example of
the failure next to the sentence.

## 2. The steps

1. Write the correction down. A correction you only agree with is lost.
2. Ask which rung can hold it.
3. Build it at the highest rung that fits.
4. Delete the prose that the mechanism replaces. This is the step that gets skipped. A rule in two
   places goes stale in one of them.

## 3. Decide the scope

A correction binds the repository, or it binds one paragraph. Ask three questions, in order:

1. Restate the correction with no names: no document, no line and no feature. When no instruction
   remains, it binds one paragraph. Rewrite that paragraph and stop.
2. Name the next document the rule would refuse. When you cannot, it binds one paragraph.
3. Look for a lesson the project already records that covers it. Add to that lesson. Never open a
   second lesson for one rule.

"This paragraph is unclear" binds one paragraph, because no author can apply "be clear". "Name the
resource, never the action" binds the repository.

When the correction touches what a package or a vendor owns, record that boundary with the lesson.
Our rule does not reach the names a package chose. A rule with no boundary reports faults nobody
can answer.

## 4. Record the rung, and what you refused

Record each lesson where the project keeps them. Name the rung it reached, each file that holds
it, and each stronger rung with the reason it does not fit. Quote the correction word for word. A
chat keeps no copy, so the quote is the only one.

When you cannot build the mechanism now, record the lesson as pending. File a decision that forces
the answer:

```bash
loop decision file --title "Which rung holds: <lesson>" --trigger "<the event that forces it>"
```

A pending lesson with nothing that forces an answer stays pending. A trigger names a change that
does something. A trigger that only watches a path fires on the first unrelated commit.

## 5. Where the mechanism lives

- A rule about the project lives in the project's repository: its gates, its tests and its
  `.loop/policy/` files.
- A rule about how the loop behaves lives in the loop. Report it as a loop fault with
  `loop fault report`, and name the rule you expected.

## 6. Failures

- You agree, and you write nothing down.
- You write it down, and you build nothing. A note that a lint should exist is not a lint.
- You correct the instance, and you leave the pattern.
