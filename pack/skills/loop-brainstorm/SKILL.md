---
name: loop-brainstorm
description: How a brainstorm room and the operator write one PRD together, from a vague idea to user stories with labels and acceptance criteria. Read this in a brainstorm room before your first message.
---

# loop-brainstorm

A brainstorm room turns the operator's idea into a PRD. The operator decides. You ask, you draft and
you check. The PRD then enters the loop, and no person reads it again until the doc-reviewer
passes it and the operator signs it off by merge. So every question the PRD leaves open reaches an
agent that cannot ask it.

## 1. The session

Ask one question at a time. Give two to four options for each, with what each option costs and
what it rules out. Recommend one option, and give its evidence. Then wait for the answer.

Work in this order. Each step needs the answers of the step before it.

1. **The problem.** Who has it, what they do today, and what goes wrong. Ask for an example.
2. **The users.** Name each kind of user the work serves.
3. **The stories.** Write each story as "As a <user>, I can <act>, so that <outcome>". Give each
   story a stable label: US-01, US-02 and so on. A label never changes after you write it, because
   specs and tickets cite it. In a project whose `.loop/config.yaml` holds a `ui` key, ask the
   operator whether each story changes what a user sees, and write the answer under its heading:
   `**UI:** yes` or `**UI:** no`. The `prd` gate refuses a story with no UI line. A `UI: yes`
   story sends the epic to the design stage.
4. **The scope check** (section 2).
5. **The acceptance criteria** of each story.
6. **The boundaries and what stays out.**
7. **The open questions.** Answer each one in the room when you can.

Read the repository before you ask a question that it answers. Read the PRDs the project already
holds, its decision records and its code. A question that a file answers wastes the operator's
time.

## 2. Extract, never invent

Every requirement traces to a story label, or to the operator's own words, quoted. A line with no
source is a guess. Delete it, or move it to Open questions.

Quote the operator's words under the story that they ask for. Then each requirement cites its
story on the line under it, as `→ § US-01`. A requirement can also cite a file of the repository,
such as an ADR. The `citations` gate refuses a requirement with no citation, and a citation whose
file or heading does not exist.

When the operator states a preference as a fact, write it as the operator's statement. Do not
upgrade it to a finding.

## 3. Check the scope first

Ask two questions before you write requirements:

1. Can each story land on its own?
2. Can each promise be proven without building the rest?

Stories that must land together are one unit. An epic that fails either question is two PRDs.
`../loop-sdlc-split/SKILL.md` states where the seam falls. Write each PRD in turn, and submit each
one.

A decision with no product source is no requirement, so keep it out of the PRD. Run the test of
`../loop-sdlc-documents/SKILL.md` § 6, When a decision needs an ADR, on it. Tell the operator the
place that the section gives it: an ADR when it passes, or the one place for a decision that fails.

## 4. What a PRD holds

Start from the template, `../loop-brainstorm/prd.md`. It holds these sections, in this order, with
a good and a bad example of each.

1. **Stories**: each story under a heading that starts with its label, with the operator's words.
2. **Scope check**: the two questions, their answers, and the sizing block. The `prd` gate
   refuses a block with a missing line, a placeholder, or a verdict other than proceed.
3. **Goal**: what a user can do afterwards. Name no technology.
4. **Requirements**: one testable line each, each one with a `→` citation under it.
5. **Acceptance criteria**: numbered A1, A2 and so on. One promise each, in product language. Each
   one can fail. Include what the product must refuse.
6. **Boundaries with other epics**: each shared thing and its owner, and the PRDs you checked.
7. **Out of scope**: each item with the owner that holds it.
8. **Open questions**: only what the session could not answer.

A requirement that names an implementation belongs in the spec. Move it there as a note in Open
questions, or delete it.

## 5. Conflicts go to a decision

A story can conflict with a decision record or with the code. Never settle the conflict yourself.
Show both sides to the operator, with each line. When the operator decides, write the decision
into the PRD with the operator's words. When the operator does not decide now, file the question:

```bash
loop decision file --title "<the question>" --trigger "<the event that forces the answer>" --body "<both sides>" \
  --waits-for-a-person "<who decides>"
```

When the trigger names a date, pass `--by <YYYY-MM-DD>` in place of `--waits-for-a-person`. The
server refuses a decision with neither or both.

Name that decision in Open questions, as a blocker.

## 6. Submit

When the operator says the PRD is ready:

1. Write the PRD at `docs/prd/<slug>.md`. `../loop-sdlc-documents/SKILL.md` states the
   layout of each document.
2. Run `loop gates --docs`. Correct each finding.
3. Commit it.
4. Run `loop prd submit --path <path> --title "<title>"`.

The server pushes the commit, opens the pull request and creates the epic in `prd_review`. The
doc-reviewer reads it next.

When the operator leaves before the scope is settled, submit nothing. Tell the coordinator what is
open. File each open question as a decision with its trigger.

## 7. Never

- Never decide scope, a conflict or a priority for the operator.
- Never write a requirement with no source.
- Never submit a PRD that the operator did not call ready.
