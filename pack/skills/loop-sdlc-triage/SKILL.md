---
name: loop-sdlc-triage
description: The rubric two judges answer about one escalation finding, and the rule that routes the finding to a person or back to the loop. Read this before you escalate, and before you triage an escalation.
---

# loop-sdlc-triage

An escalation stops the work and costs a person's attention. Most escalations hold at least one
finding that the repository can settle alone. This skill states the two questions a judge answers
about each finding, and the rule that routes it.

`loop escalation judge` reads this file as the rubric. It sends the whole file to each judge. It
also reads both thresholds from the table at the end, and it refuses to run when they differ from
the server's. So a level you change here changes what every judge reads.

## Who reads this rubric

**The escalating run answers both questions about its own finding.** It passes the level to
`loop escalation raise --score <level>`, and the choice to `--choice`. That is one reader, and it
is not two judges. A run argues for the choice it already made. So the server buys three smaller
things with the score. An escalation with no score is impossible. The server refuses a level that
the run itself puts in the loop half. Every escalation records a level that a later audit reads.

**The fixer asks two judges.** For each finding,
`loop escalation judge <escalation> --finding "<one sentence>"` asks two judges the two questions
below. A convergence reader then scores how far apart the two answers are.
`loop escalation route <escalation> --file <panel file>` sends the panels to the server, and the
server routes each finding. The judges say who answers. The fixer says what the
answer is.

**The escalating run's own score is evidence, and it is no judge's answer.** The server refuses
every level under the cut, so a run that escalates states 4 or 5. A fixer that read that number as
a judge's answer would send every escalation to a person.

## The rubric routes a finding, and it never answers one

A judge says which kind of answer a finding needs. A judge never says what the answer is.

Keep that line, because the two come apart. A review can report three true faults in one
document, and the owner can then delete the requirement under all three. The route was right, and
no rubric could predict the answer.

## What a judge reads

A judge reads the escalation body, the item, each document the body names, and this rubric. A
judge also reads each decision that the operator recorded on the item: the inbox answers on the
item, the coordinator's notes on it, and each answered question about it. A recorded decision is
evidence, as a document is. A judge runs no command and reads no dashboard. So every level below
names a kind of evidence. No level asks a judge for a fact it cannot read.

## One finding is the unit

A finding is one statement in an escalation that names something wrong or unknown, and asks for
an answer. Score each finding on its own.

These four are not findings:

- A pointer to a question that another document already holds. The stage of that document
  answers it.
- A note about what the escalating run read.
- A gap the author can close from the sources. A review rejects that gap instead.
- A count of review rounds. A command measured it.

**An escalation reaches a person when one of its findings reaches a person.** Route each finding
alone, so the loop can settle the rest and a person reads less.

## Question 1, a score with five levels

Ask: what answers this finding?

The levels run from the evidence that sits furthest from the owner to the evidence that only the
owner holds. Level 1 rests on our code. Level 3 rests on what we wrote before. Level 5 rests on
what the owner knows.

| Level | Name | What answers the finding | Route |
| --- | --- | --- | --- |
| 1 | `computed` | A command in this repository | loop |
| 2 | `measured` | A read-only command against the world outside | loop |
| 3 | `derived` | This repository's own documents and rules | loop |
| 4 | `preference` | The owner's taste, among answers that all fit | person |
| 5 | `owner-only` | A fact the owner holds and this repository does not | person |

The cut falls between level 3 and level 4.

### Level 1, computed

Choose this level when a command in this repository answers the finding. The output is the answer,
and the command leaves no judgement behind it. Name the command in your reason.

`loop secret check <name>` answers whether a secret is present. A search answers whether a string
is in the tree. A test run answers whether a criterion holds today.

Not this level when you can name no command. Not this level when the output still leaves a choice
between two answers.

### Level 2, measured

Choose this level when one fact from outside this repository answers the finding, and a read-only
command reads that fact. Name the command, and name the fact it reads.

A read-only command changes nothing outside this repository. A DNS lookup is one. A read against a
vendor's API is one. A read of a dependency that this repository installs is one.

Not this level when no command of ours reads the fact. A console that only a person reads is level
5. Not this level when the fact still leaves a choice between two sound answers. That is level 4.

### Level 3, derived

Choose this level when this repository already holds every fact, and a rule in this repository
picks one answer. Two of our documents disagree, and a third document or plain arithmetic says
which one is wrong. Name the rule, or state the arithmetic.

**Read one question before you choose this level.** Does a document in the chain assert a vendor's
hostname, record value or internal step? Such a finding is level 2, because a read-only command
reads the vendor's outcome instead.

Not this level when two answers both fit every document and no rule picks one. That is level 4.

Level 3 sits next to the cut on purpose. A derived answer rests on what we wrote before. Every
document of ours can agree with every other and none of them with the world. Nothing fails, so
nothing reports it. A derivation that reaches outside our documents for one fact is worth more
than a confident one that stays inside.

### Level 4, preference

Choose this level when every fact is in hand, more than one answer fits every document, and no rule
picks one. The answer states what the project wants.

One shape is common. The world does not meet a criterion of ours. One answer changes the world, and
another answer deletes the criterion. Both answers are sound.

Not this level when a rule does pick one answer. That is level 3. Read the project's agent
instructions, its decision records and its product sources before you choose level 4. A finding
about the words on a screen often has an answer in the project's copy source.

Not this level when the second answer only asks a person to supply or decide what a rule already
settles. A request to a person is no second answer. That is level 3, and Question 2 states why.

### Level 5, owner-only

Choose this level when the answer needs a fact that this repository does not hold, and no command
of ours reads it. Five kinds reach this level:

- The owner's intent for the product, including scope that no document states.
- A fact about another project, another account, or another product.
- Money, a vendor's plan limit, or a commitment to a vendor.
- An action that only a person takes in a vendor's console.
- How work divides between epics, where no document already divides it.

Not this level when a read-only command reads the missing fact. That is level 2.

## Question 2, a choice with two options

Ask: does the answer this finding needs direct an agent or a person to act outside this
repository?

Ask the question about the answer that your level picks, and about no other answer.

- At level 1, 2 or 3, a command, a measurement or a rule picks one answer. Judge that one answer.
- At level 4 or level 5, no rule picks one answer. Judge every answer that fits.

**A remedy that the escalation offers is not always an answer.** An escalating run often lists a
remedy that asks a person to supply, draw or decide something. A rule of ours can already pick an
answer inside this repository. Then that remedy is no answer. It makes the finding neither
`outside` nor level 4.

| Option | When to choose it |
| --- | --- |
| `inside` | The answer changes files in this repository, or reads without changing anything |
| `outside` | The answer sends a message, spends money, publishes, deploys, or creates, changes or deletes what a vendor or a production system holds |

A commit, a push, a pull request, a note on an item and a state change are `inside`. Every stage
already makes them under a merge gate. Every read is `inside`.

**No confidence overrides this question.** The cost of a wrong answer is not symmetric here. A
wrong document is recoverable, because a later reader reads the source again. A message that went
out is not recoverable. So `outside` routes the finding to a person whatever the score says.

This question still over-escalates at level 4 and level 5, and the loop accepts that trade. Those
two levels reach a person whatever the gate says, so the gate costs nothing there.

**The narrow question puts nothing outside the repository in the loop's hands.** The fixer's one
write for an escalation is a route and a note. So an answer the loop gives cannot send a message or
change a vendor.

## The route

The server routes a finding to a person when one of these holds:

- One judge answers `outside`.
- One judge scores the finding at level 4 or level 5.
- The two judges' levels fall on opposite sides of the cut.
- Fewer than two judges answered, or one judge answered twice.
- The convergence reader scored no convergence.
- The convergence falls under its threshold.
- The confidence in that convergence falls under its threshold.

It routes the finding to the loop when none of them holds.

**The first three rules run before any threshold.** No number in this document can reverse one of
them.

**A route to the loop is not silence.** The server writes a note on the item that names the route
and the finding. A refusal of `loop escalation raise` names the next step in its hint. A refusal
that left a run with nothing to do would cost more than the escalation it stopped.

**No judge states its own confidence.** A judge is a general model, and such a model states a
confidence that nobody calibrated. The convergence reader states one about the pair of judges.

### One carve-out routes a finding to the loop

The server routes a finding to the loop when both judges gave one answer. Both judges name one
level, and both answer the gate the same way. This rule reads no threshold.

This rule runs after every rule above that reaches a person, so it reverses none of them. A finding
either judge scores at level 4 or level 5 still reaches a person. A finding either judge answers
`outside` still reaches a person.

This rule leaves the thresholds one case to gate. Both judges answer `inside`, both levels sit
below the cut, and the two levels differ. The convergence reader reads the two reasons about that
panel, and the thresholds gate that reading.

## The convergence score

The two judges answer the two questions above. The convergence reader then reads both answers and
rates how far apart they are. It scores no finding, and it is not a third judge.

It reads the finding, each judge's level, each judge's gate answer, and each judge's reason. It
answers one score against this scale.

| Level | Name | What the two answers look like |
| --- | --- | --- |
| 0 | `contradict` | The two judges name different kinds of answer, or they answer the gate differently |
| 1 | `diverge` | The two judges name one kind of answer, and each reason rests on different evidence |
| 2 | `align` | The two judges name one kind of answer, and both reasons rest on the same evidence |
| 3 | `identical` | The two reasons name the same evidence, and neither one names evidence the other missed |

`align` and `identical` mean that the judges agree. Level 0 and level 1 mean that they do not.

**The reasons are what the reader adds.** Two judges can answer `derived` for different reasons.
One names a rule that exists, and one names a rule that does not. Code that compares two labels
reads both answers as agreement. The reader reads the reasons, so it reports that pair as
`diverge`.

### The two thresholds a person may change

Both numbers are fractions between 0 and 1. A finding reaches the loop when both readings are at or
above their threshold.

| Threshold | Value | What it gates |
| --- | --- | --- |
| `convergence` | 0.85 | The share of the reader's belief that sits on `align` or `identical` |
| `confidence` | 0.85 | The reader's own confidence in that score |

**Change a number here and in the server together.** `loop escalation judge` reads both numbers
from this table, and it refuses to run when they differ from the server's.

**Neither threshold reads the reader's point score.** A score answer carries a probability for each
level, and the point score is one average over them. An average can read as agreement while part
of the belief sits across the cut. So the route adds the belief on the agreeing levels.

**Both numbers are conservative, because no data calibrates them yet.** Record a convergence
reading with each later case. A number tuned against data becomes possible once enough cases hold
one.

## Why two judges, and why a score

An ordered score makes a disagreement a distance. Two judges that differ by one level inside the
loop half do not change the route. A choice with unordered options would read every disagreement
as the same size.

Neither judge reads the other's answer. The second judge costs one more call for each finding. It
buys independence from one model's reading of this text, which is the failure a single confident
judge cannot report.

## What this rubric does not catch

A judge reads the escalation, and no judge reads the world. An escalation whose observations are
each true and whose conclusion is false reads as sound. Two judges then agree on it with high
confidence, and the loop decides wrong. This rubric does not stop that. A wrong decision here is
recoverable in the usual case, because every decision lands in a note with its reasons, and a
later reviewer reads the source again.
