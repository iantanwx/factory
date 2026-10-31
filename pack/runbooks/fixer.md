---
name: loop-fixer
description: The fixer's runbook. Route one escalation through the triage panel, one finding at a time, so a person reads only what the loop cannot answer.
---

# Fixer

A run escalated an item, and the escalation waits for its panel. You split it into findings, ask
two judges about each finding, and send the panels to the server. The server routes each finding
to a person or back to the loop. Your route decides whether a person is asked at all. You score
nothing yourself. Nobody is present in this run.

Read these first, in this order:

1. `../loop-sdlc-turn/SKILL.md`, the rules of every run.
2. `../loop-sdlc-glossary/SKILL.md`, the words this runbook uses.
3. `../loop-sdlc-triage/SKILL.md`, the rubric the judges answer. `loop escalation judge` reads this
   file.

## 1. Take one escalation

Run `loop work peek --stage fixer`. Exit 1 means no escalation waits. Then finish `done`.

Take the oldest escalation it lists, and only that one. Read its finding, its level, its choice and
its item. Run `loop work show <item>`, and read each document the finding names. Your run holds no
item. The server lets you read, note and close the item of an escalation that waits for its panel,
or that you routed. It refuses every other item.

## 2. Split it into findings

Number each finding: F1, F2 and so on, in the order the escalation states them. A finding names one
thing that is wrong or unknown, and asks for an answer. Write each one as one sentence.

Leave out these four, and say in your report that you left them out:

- A pointer to a question that another document already holds.
- A note about what the escalating run read.
- A gap the author can close from the sources.
- A count of review rounds.

The escalating run's own score is evidence, not a judge. Never pass it off as a panel.

## 3. Ask the panel about each finding

Make one directory with `mktemp -d`. Ask the panel once for each finding, each into its own file:

```bash
loop escalation judge <escalation> --finding "<F1, in one sentence>" --out <dir>/f1.json
```

The command reads the decisions that the operator recorded on the escalation's item, and gives
them to both judges. These are the inbox answers on the item, the coordinator's notes on it, and
each answered question about it. `loop escalation decisions <escalation>` prints the same list.

Never type a judge's level or reason yourself. Never reword a finding and ask again. A panel that
sent a finding to a person did send it.

When the command could not decide, it still writes what it read. Send that file as it is. The
server routes a panel with fewer than two judges to a person.

## 4. Route the escalation

Send one file to the server. For one finding, send its panel file. For several, join the panel
files into one JSON array, unchanged. Each panel file holds an array of one panel, so `jq -s add`
joins the arrays in order:

```bash
jq -s add <dir>/f1.json <dir>/f2.json > <dir>/panels.json
loop escalation route <escalation> --file <dir>/panels.json
```

The server routes the escalation to a person when one finding reaches a person. The item then
waits in the operator's inbox. When every finding routes to the loop, the server writes a note on
the item that names the route and each finding. The next run of the item's stage reads it in its
brief.

`TRANSITION_NOT_ALLOWED` with the rule `already_routed` means another run routed it. Finish the
run.

### Release one path the ticket forbids

Some findings say that a contract needs a file that the ticket's own `Must not change` list
forbids. A note does not answer that finding. The implementer reads the body, obeys the list, and
escalates again. The body is the one place scope lives. So a route that sends every finding to the
loop may release one path into `May change`:

```bash
loop escalation route <escalation> --file <dir>/panels.json \
  --release <path> --forbids "<the entry>" --condition "<what the implementer may do there>"
```

- **Release only when the ticket's own list blocks work that one of its contracts needs.**
- **Copy `--forbids` out of the ticket exactly.** The path must sit under that entry. The server
  reads the path as it lands: `lib/../.github/ci.yml` is `.github/ci.yml`, and a path above the
  checkout is refused.
- **`--condition` is the new boundary.** State what the implementer may do there, and nothing
  wider. Write it in plain words on one line, and name no path in it. The loop reads each path in
  `May change` as a path the ticket may change, so the server refuses a condition that holds a
  backtick, a line break or a cited path, and a path that holds a backtick, a space or a brace.
- **Release one path, and widen only.** No command adds an entry to `Must not change`.

The server edits the body once, and the edit enters the item's retry identity. A panel that routes
to a person releases nothing. A path on the server's never-released list routes the escalation to
a person, whatever the judges scored. Any other refusal writes nothing, and the escalation still
waits. Read its `rule` and `hint`, correct the flag, and route again with the same panel file.

Record your answer on the item, so the next run of its stage reads it in its brief:

```bash
loop work note <item> --body "<each finding, its route and the reason the panel gave>"
```

Every finding can route to the loop, and one answer can show that the item has no work left. The
base branch, a done item or an ADR then already answers it. The close depends on the item's kind.
Close a ticket with that evidence:

```bash
loop work supersede <item> --commit <sha> --reason "<the finding and its answer>"
```

Use `--done <item>` or `--adr <path>` in place of `--commit` when that is the evidence. The server
checks the evidence. It refuses when a run holds the ticket, or when the ticket has an open pull
request.

An epic has no `work supersede`. Its close is `loop work cancel <epic> --reason "<why>"`, from any
state before decomposing. The server then closes the epic's open document pull request with no
merge. You cannot cancel an epic. Write the finding, its answer and that close in your note on the
epic. The next run that holds the epic, or the coordinator on the operator's word, runs it.

Finish with `loop run finish --outcome done`. Report each finding, its route and the file you sent.

## 5. Never

- Never route a finding by your own reading. When the judges disagree and you believe one of them,
  the server still routes the finding to a person. Your belief is not a third judge.
- Never change the item or its documents. The release, the note and the close in section 4 are
  the only writes you make on the item.
- Never take a second escalation.
