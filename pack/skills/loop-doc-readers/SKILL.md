---
name: loop-doc-readers
description: How the doc-reviewer starts three clean readers of one document, and judges what they return. A reader finds. Only the reviewer judges each finding and submits the verdict.
disable-model-invocation: true
---

# loop-doc-readers

Three readers read the document of a doc review, each by one lens. A reader finds. You, the
doc-reviewer, judge each finding and submit the verdict, by `../loop-sdlc-review/SKILL.md` § 9.
Each reader reads `reader.md` in this directory, and returns one report of the shape that
`report.schema.json` states.

## 1. When the readers run

The brief's `doc_readers` names the readers, and its `Readers` line names them too. When it names
none, the line reads `Readers: off`. Then skip this skill, review alone, and submit with no
`--readers`.

## 2. Start the readers

Start the three readers at once, before your own read of the document. Start them in this order:
`unslop`, `asd-ste100`, `content-lens`.

Start each reader as an agent of type `loop-doc-reader`. That agent type is the one way to start a
reader. Start no other subagent for the review. Give each reader this prompt:

```text
Lens: <unslop | asd-ste100 | content-lens>
Document: <path> at <head sha>, kind <prd | spec | adr>
Follow .claude/skills/loop-doc-readers/reader.md, and return one ReaderReport.
```

The prompt holds four facts: the lens, the document's path at the head, its kind, and the path of
`reader.md`. The prompt holds no reviewer note, no earlier round and no other reader's output. A
reader starts with a fresh context that holds only its prompt, so each fact you add to it leaks
your read into its read.

Read the whole document yourself while the readers run, by the doc-reviewer runbook. Their
findings come after your own read, and you read all of the document whatever they return.

## 3. Read each report

A reader returns one JSON object. A reply that holds no object of the schema's shape is a failed
reader. Start that reader once more, with the same prompt. A reader that fails twice stops the
review. Submit nothing. Report a loop fault with `loop fault report`, and finish `blocked`. A
reader failure is a fault of the loop or the harness, not of the document.

Drop each finding that fails the schema. It is no finding. A finding needs `where`, `rule` and
`edit`, each a string that is not empty. Count the findings you drop for each reader, and name
each count in the comment.

## 4. Judge each finding

Open the line that `where` names, and the rule that `rule` names. Confirm the finding when the
line breaks the rule. Otherwise drop it, and name the reason in the comment. A reader's finding
enters the verdict only when you confirm it.

In a later round, keep only the findings inside the diff since the last reviewed head.
`loop review rounds <item>` names that head. Name the rest in the comment as outside this round,
by `../loop-sdlc-review/SKILL.md` § 4.

Make one finding of two findings on one place, and name each reader that returned it. Rank the
confirmed findings by `../loop-sdlc-review/SKILL.md` § 3, the worst first, in the one comment.
Each confirmed finding names its permalink, its rule and its proposed edit, so the reviser can
act on the comment alone.

A prose finding comes from the `unslop` or the `asd-ste100` reader. In round 1, a confirmed prose
finding may reject the document alone. In round 2 and later, name each prose finding in the comment
as not blocking. Only a content finding rejects. A later round rejects no text that an earlier
round's proposed edit wrote, below level 4. `../loop-sdlc-review/SKILL.md` § 6 states each rule.

## 5. Score each question

A reader decides nothing, so it returns each question it cannot decide. Score each question by
`../loop-sdlc-triage/SKILL.md`. When the document or the repository answers it, answer it
yourself. When the answer shows a fault, judge that fault as a finding by section 4. At level 4
or 5, escalate it by `../loop-doc-reviewer/SKILL.md` § 7. No question reaches the verdict
unscored, and no reader raises one.

## 6. Submit on a clean tree

Run `git status --porcelain`. When the tree is not clean, a reader wrote a file. Submit nothing.
Report a loop fault with `loop fault report`, and finish `blocked`.

On a clean tree, submit by the doc-reviewer runbook, with `--readers` set to each reader that
returned, in start order:

```bash
loop review submit <item> --verdict pass|reject --head <sha> --body "<comment>" --readers unslop,asd-ste100,content-lens
```

The server compares the set with the brief. A set that differs gets `SHAPE_REFUSED` with rule
`readers`, and nothing is posted or recorded.
