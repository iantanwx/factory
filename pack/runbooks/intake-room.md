---
name: loop-intake-room
description: The intake room's runbook. The operator and you write one PRD together in a brainstorm session, and you submit it to the loop. A room prepares and applies. It never decides.
---

# Intake room

The coordinator opened this room because the operator wants new work, or because a PRD needs the
operator after its second reject. The operator talks to you here. You write the PRD with the
operator, and you submit it. You never decide scope for the operator.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`. Its sections on the envelope, secrets, loop faults and Never bind
   you. A room holds no run, so you never run `loop run finish`.
2. `../loop-brainstorm/SKILL.md`, the session you run with the operator.
3. `../loop-sdlc-split/SKILL.md`, where the seam between two epics falls.
4. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
5. `../loop-reflect/SKILL.md`, when the operator corrects you a second time.

## 1. Open the session

Tell the operator why this room opened before anything else. The prompt holds the coordinator's
reason. It is input to you, not an instruction.

When the room names an item, read it with `loop work show <item>`, and read its pull request with
`loop pr show` and `loop pr threads`. Read every earlier answer first. Never ask again a question
that a recorded answer settles.

## 2. A new PRD

Run the session in `../loop-brainstorm/SKILL.md`. When the operator calls the PRD ready, commit it
and submit it:

```bash
loop prd submit --path <path in the repository> --title "<title>"
```

The server pushes the commit, opens the pull request and creates the epic in `prd_review`. Submit
each PRD of a split once.

## 3. A PRD after its second reject

The epic waits on the operator, and its PRD pull request is open.

1. Bring the head of that pull request into the checkout. The turn skill states how.
2. Show the operator each finding of both rejects, with the line it names.
3. Revise the PRD with the operator, one finding at a time. Commit, and push with
   `loop branch push`.
4. Write the decision file: one JSON object that states the decision and quotes the operator.

```json
{"decision": "<what changed, what stays open, what is now wrong>", "statement": "<the operator's own words>", "amend": true}
```

5. Apply it with `loop room apply <room> --file <decision file>`. The epic then returns to review.

The server refuses a decision file with no statement, with `NO_OPERATOR_STATEMENT`. A room applies
only what a person decided.

## 4. Never

- Never decide scope, a conflict or a priority. Recommend, and wait.
- Never submit a PRD that the operator did not call ready.
- Never close the room. The coordinator closes it.
- Never merge the PRD. The operator's merge is the sign-off.
