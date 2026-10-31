---
name: loop-retro
description: The retro's runbook. Read the record of one epic that reached done or one bug that closed. Write what the loop must learn from it, and route each learning through two judges.
---

# Retro

An epic reached `done`, or a bug closed, and its retro waits for a run. You read the item's record,
write each learning it shows, ask two judges about each learning, and send the panels to the
server. The server routes each learning: it lands it, holds it or sends it to a person. You land
nothing yourself. Nobody is present in this run.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-retro-rubric/SKILL.md`, the three questions the judges answer. `loop retro judge` reads
   this file.

## 1. Take the retro

```bash
loop retro record
```

The first call binds the oldest pending retro of your project to your run, and prints its record.
A later call prints the same retro. Exit 1 means no retro waits. Then finish `done`.

The record holds the item and, for an epic, each of its tickets. Each item lists its review rounds,
runs, escalations, loop faults, pull request comments, notes and signals. Each element is a case,
and each case carries a reference in `ref`, such as `run:r-1a2b` or `round:factorio-x1y:2`. The
record is the whole of what you read. Your run holds no item, and the server refuses every other
read of one.

## 2. Read the held learnings

```bash
loop retro held
```

It lists each learning that an earlier retro of this project held, because one case showed it. Each
held learning has an id, such as `L-4`, and its cases.

## 3. Write the learnings

Make one directory with `mktemp -d`. Write the learnings to `<dir>/learnings.json`:

```json
{"learnings": [
  {"statement": "The implementer runs the tests of each changed directory before it pushes.",
   "cases": ["run:r-1a2b", "round:factorio-x1y:2"]},
  {"statement": "The code-reviewer reads the ticket's Must not change list before it rejects a scope change.",
   "cases": ["escalation:7"], "extends": "L-4"}
]}
```

- **A learning states what a stage, a skill or a policy must do differently.** Write it as one
  sentence about the loop, not about the item. "The ticket was hard" is no learning.
- **Name each case by its reference, exactly as the record prints it.** The server refuses a case
  that is not in the record.
- **One rule is one learning.** Split a statement that holds two rules.
- **When a learning repeats a held one, name the held id in `extends`.** The server adds the held
  learning's cases to the new one.
- **Never write where the fix lives, how many cases show it or how large it is.** The judges answer
  those three questions.

When the record shows no learning, write none. Go to section 5.

## 4. Ask the judges

```bash
loop retro judge --file <dir>/learnings.json --out <dir>/panels.json
```

It asks two judges about each learning, each alone, then the convergence reader about the pair. It
writes every panel to `<dir>/panels.json`. When a judge or the reader gives no answer, the command
still writes the panel, and `notes` says why. Send that file as it is. The server routes a panel
with fewer than two answers or with no reading to a person.

Never edit the panel file. Never reword a learning and ask again. A panel that sends a learning to
a person did send it.

`USAGE` with the rule `rubric_drift` means the rubric's thresholds differ from the server's. That
is a loop fault. Report it by `../loop-sdlc-turn/SKILL.md` section 7, and finish `blocked` with the
fault. The server returns the retro to the queue.

## 5. Route the retro

When you wrote no learning, write an empty panel file first:

```bash
printf '%s\n' '{"panels": []}' > <dir>/panels.json
```

Send the panel file:

```bash
loop retro route --file <dir>/panels.json
```

The server checks each case, decides each route with its own thresholds, and lands, holds or
escalates each learning. It puts one notice for the retro in the operator's inbox, and marks the
retro `done`. Every retro ends with this route, also a retro with no learning.

A refusal writes nothing, and the retro stays yours. Read its `rule`:

- `USAGE`, `case_not_in_record`: the hint names the case. Remove that case from the learning, or
  remove the learning when it has no case left. Judge again, then route again.
- `USAGE`, `extends_not_held`: the learning extends a learning that is not held. Remove `extends`,
  judge again, then route again.
- `TRANSITION_NOT_ALLOWED`, `retro_done`: a route already ended this retro. Finish the run.
- `TRANSITION_NOT_ALLOWED`, `retro_not_bound`: your run holds no retro. Run `loop retro record`
  first.

## 6. Finish

```bash
loop run finish --outcome done
```

Report each learning, its route and the id the server made for it. A run that ends with no route
returns the retro to the queue, and it counts one attempt. Two such runs fail the retro, and an
alert reaches the operator.

## 7. Never

- Never open a pull request, create an item or file a learning as a loop fault yourself. The route
  lands each learning that passes.
- Never route a learning by your own reading. Your belief is not a third judge.
- Never take a second retro.
