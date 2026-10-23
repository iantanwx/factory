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

The `lessons` gate reads the three tables of this skill: the ladder, the scopes and the channels.
They ship with the loop, so no project can change them.

## 1. The ladder

Pick the strongest rung that holds the rule. Agents copy what the code around them already does,
so a weak guard becomes the template for the next one.

| Strength | Mechanism | Where it lives in a project |
| --- | --- | --- |
| 1 | A type that cannot hold the bad value | The code itself |
| 2 | A gate that fails CI | A gate that `.loop/config.yaml` names, or a gate of `loop gates` |
| 3 | A lint rule, with a baseline that only shrinks | The lint configuration of the project |
| 4 | A check script with an exit code | A script that a gate runs |
| 5 | A test that fails without the change | The tests of the project |
| 6 | One helper, so there is one way to do the thing | The shared code of the project |
| 7 | A sentence in a document | `AGENTS.md`, `CODING_STANDARDS.md` or a `.loop/policy/` file |

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
3. Look for a lesson in the ledger that covers it. Add to that lesson. Never open a second lesson
   for one rule.

| Scope | The correction binds | What the amender does |
| --- | --- | --- |
| `repository` | Every later document of the same kind | Records the correction, and names its lesson |
| `document` | The paragraph that the correction marks | Records the correction, and reaches no rung |

"This paragraph is unclear" binds one paragraph, because no author can apply "be clear". "Name the
resource, never the action" binds the repository.

When the correction touches what a package or a vendor owns, record that boundary with the lesson.
Our rule does not reach the names a package chose. A rule with no boundary reports faults nobody
can answer.

## 4. Record the correction in the ledger

Each project keeps its ledger at `docs/solutions/correction-ledger.md`. The ledger holds one row
for each correction that carries a lesson. `loop gates` runs the `lessons` gate over it. A project
with no ledger passes, so the first row also creates the file.

A correction arrives on one of two channels:

| Channel | Where the person wrote it | What locates the correction |
| --- | --- | --- |
| `review` | A comment on a pull request | The pull request, the file and the line |
| `chat` | A message in a conversation | The date, and the words the person wrote |

A review comment keeps a permalink. A chat keeps nothing, and no stage reads a conversation. So the
quote in the row is the only copy. Quote the words exactly on both channels.

No gate can fail when a correction reaches no row. A gate reads the checkout, and a conversation is
not in it. `../loop-reflect/SKILL.md` holds the only trigger.

### The row

The ledger table has these nine columns, in this order:

| Correction | Channel | Date | PR | Line | Lesson | Scope | Rung | Where it lives |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `<the words>` | `chat` | 2026-10-01 | - | - | `<lesson>` | `repository` | 2 | `lint` |

- `Correction` holds the words of the person, in backticks.
- `Channel` holds `review` or `chat`.
- `Date` holds the day the person wrote it, in UTC, as YYYY-MM-DD.
- `PR` holds the pull request of a `review` row as `#` and its number. A `chat` row writes `-`.
- `Line` holds the file and line that a review comment marks. A `chat` row writes `-`. No check
  reads this cell, because the path can stay on a branch that never merges.
- `Lesson` holds a slug. Two corrections that teach one rule share the slug, and give one answer:
  the same scope, rung and `Where it lives`.
- `Scope` holds `repository` or `document`.
- `Rung` holds a strength from the ladder, or `pending`. A `document` row writes `-`.
- `Where it lives` names each file or gate that holds the rung, each in backticks. A file is a
  path in the checkout. A gate is a gate that `.loop/config.yaml` names, or a gate of `loop gates`.
  A file of a check script must be reached by a gate or a test. The command of a gate names the
  file, or names a file or a directory that reaches it. The word `make` reaches the Makefile. The
  name of a package manager reaches the script lines of `package.json`. A Go package pattern such
  as `./...` reaches each Go file under its directory, so a test that names the file reaches it.
  A `pending` row names the one decision id that `loop decision file` printed. A `document` row
  writes `-`.

No two rows hold the same channel, date and words.

### The section of a lesson

Each lesson of a `repository` row has its own section, under a third-level heading that holds the
slug in backticks. Each section names a lesson that a row names.

A lesson at a numbered rung states each stronger rung, and why it does not fit. Start that paragraph
with `**Stronger rungs.**`, and name each rung as "rung 1", "rung 2" and so on. A lesson at rung 1
has no stronger rung.

```markdown
### `name-the-resource`

**Stronger rungs.** Rung 1 does not fit, because a path is a string in a document.
```

When you cannot build the mechanism now, record the row as `pending`. File a decision that forces
the answer, and name its id in `Where it lives`:

```bash
loop decision file --title "Which rung holds: <lesson>" --trigger "<the event that forces it>"
```

The section of a `pending` lesson starts a paragraph with `**Trigger.**`, and states the event
that forces the answer. A pending lesson with nothing that forces an answer stays pending. A
trigger names a change that does something. A trigger that only watches a path fires on the first
unrelated commit. Whoever answers the decision writes the rung, the files and the stronger rungs.

### The summary block

The ledger counts its own rows in one block between two markers:

```markdown
<!-- lessons:summary: the lessons gate derives these lines -->
<!-- /lessons:summary -->
```

The `lessons` gate derives the block from the rows. When the block is missing or wrong, the
finding prints the correct block. Paste it in. Write no count about the rows anywhere else in the
ledger. A count beside the table is the same rule in two places.

## 5. Where the mechanism lives

- A rule about the project lives in the project's repository: its gates, its tests and its
  `.loop/policy/` files.
- A rule about how the loop behaves lives in the loop. Report it as a loop fault with
  `loop fault report`, and name the rule you expected.

## 6. Failures

- You agree, and you write nothing down.
- You write it down, and you build nothing. A note that a lint should exist is not a lint.
- You correct the instance, and you leave the pattern.
