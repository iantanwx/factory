---
name: loop-bro
description: "Restate a message or a document in plain human language, with no jargon. Use the document form on a ticket draft before you create the ticket."
disable-model-invocation: true
---

<!--
Ported from skills/bro/SKILL.md in tickertown at commit
64e293f9ae2db7831d12462e76ffe1aa3deacd8c, the head of its main branch on 2026-10-23.

Tickertown recorded this fork line:

FORKED from cursor/plugins, and ours from here on.

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/bro
  commit:   99559f2f52047978602ef365589275831e76af07
  forked:   2026-09-28

Recorded so that a later reader can diff against upstream and see what we changed on purpose.
This is not a dependency we track, and we do not owe it updates.

Changed on purpose, 2026-09-30. Upstream acts on the last conversation turn only, so nobody could
apply it to a ticket body. The document form below is ours. The decomposer's runbook calls it.

Edit the copy in the loop's pack. The server writes `.claude/skills/` again at each start.
-->
# Bro

Say it plainly, like one human talking to another.

## A conversation turn

Restate your last message. Stop using jargon and speak coherently. State it more simply and
concisely, like one human talking to another.

## A document

Use this form on a draft you wrote and nobody read yet. A ticket body is the case the loop has.
A person reads a ticket at 5am. Every word that person does not need costs attention.

Work through the draft twice.

**First pass, one section at a time. Delete.** Read each sentence and ask one question. Does the
reader do the work without this sentence? Delete the sentence when the answer is yes.

Four kinds of sentence go, and each kind has a test you can apply.

- **A sentence another document already states.** Name the document the reader opens. A ticket
  names its spec section under `## Source`, and the implementer reads that section. Delete the
  paraphrase. Keep the slice boundary, which is the one fact the spec does not hold.
- **A sentence that explains a decision.** The reader follows the instruction. The reader does
  not review it. Keep the instruction. Delete the reason behind it.
- **A sentence that would fit another project unchanged.** It says nothing about this one.
- **A heading with nothing under it that the reader acts on.** Delete the section.

**Second pass, whole draft. Rewrite.** Say each surviving point in the plainest words you hold.
Write one idea per sentence. Name the actor in each sentence. Delete each adverb. Read
`../loop-unslop/SKILL.md` for the patterns to catch, and apply every one of them.

## What this pass never deletes

- **A number, a path, a command or an identifier.** Each one is the content.
- **A hedge.** `May have failed` must not become `failed`. Confidence is content.
- **A contract's identifier, its title and its proof.** The decomposer's runbook orders the
  decomposer to name each contract and to copy no contract text. So a contract entry is short
  already, and this pass has nothing to take out of one.
