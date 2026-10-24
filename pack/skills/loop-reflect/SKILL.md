---
name: loop-reflect
description: Start this when a person corrects you a second time about a rule the project already holds, or about a rule no file holds. Turn the correction into one mechanism and one pull request.
---

# loop-reflect

A correction that a person states twice is a fault in the project, not in one session. Your context
ends with your session. The next session reads the same files and makes the same choice. The person
then does the work of a mechanism.

This skill applies where a person is present: the coordinator's session and every room. No
unattended run starts it, because no person corrects an unattended run.

## 1. When to start

Any one of these is enough:

- A person corrects you, and the project already holds the rule they cite.
- A person states the same correction a second time in one conversation.
- A person corrects you, and you cannot name the file that holds the rule. This case is the most
  common one. A rule that no file holds is a rule that only the person remembers.

Start without being asked. A person who asks has already counted the repeats.

These are no repeat:

- One instruction that nothing in the project contradicts.
- A correction of one fact in one document. Rewrite the document and stop.
- A correction that an existing rule covers, and that you followed. The rule held.

Read the files before you decide. A new correction often states an old rule in other words.

## 2. Ask two readers

You chose the behaviour that the person corrected, so your own account argues for it. Ask two
subagents that do not see each other's answer. Two answers make a disagreement visible. One
confident answer reports nothing.

Give each reader the same three things: each correcting message, quoted, each file the correction
names, and `../loop-encode-lessons/SKILL.md`. Ask each reader two questions:

1. What is the rule, in one sentence that a command could check?
2. Which file holds it today, and which file should hold it?

Read only conversations of this project. Another project's conversation holds work that nobody
here may read.

## 3. Build the mechanism

Choose the rung from `../loop-encode-lessons/SKILL.md`, at the highest rung that fits.

- Never choose the prose rung because it is quick. Prose already failed. The person corrected you
  against a document that existed.
- When the readers disagree, take the stronger rung. A gate stronger than needed costs one
  command. A weaker one costs another correction.
- When no rung above prose fits, say so. Name the judgement the rule needs, and add an example of
  the failure.

Record the correction in the project's ledger, `docs/solutions/correction-ledger.md`, in the same
change. Quote the person's words, the date, the scope, the rung and the files that hold it. Record
it at the prose rung too. Section 4 of `../loop-encode-lessons/SKILL.md` states each column, and
the `lessons` gate checks the row. Nothing reads a conversation later, so the record is the only
count of repeats.

Delete the prose that the mechanism replaces.

## 4. Land it

- **A rule of the project:** commit the mechanism and the record, run the project's gates, and open
  one pull request. The operator's approval lands it. In a room, push with `loop branch push`.
  In the coordinator's session, give the change to the operator as a link to the pull request.
- **A rule of the loop itself:** report a loop fault with `loop fault report`. Name the rule, the
  correction and what you expected the loop to do.

Name every finding you did not land in the pull request body, with the reason it waits. A person
decides what becomes new work.

## 5. Failures

- You report the correction, and you build nothing.
- You write prose, because prose is quick.
- You change the one instance, and you leave the pattern.
