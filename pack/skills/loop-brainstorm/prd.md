# <Title of the epic>

> This is the PRD of one epic. Copy it to `docs/prd/<slug>.md`. You derive it from the
> operator's words. You never invent it. Each requirement traces to a story of this PRD, or to a
> file of the repository. The `citations` and `prd` gates of `loop gates` check that rule and the
> scope check. A PRD that fails them never merges.
>
> The examples below come from one imaginary epic, a backtest engine. They match the examples in
> the spec template. Read them as a set. Delete them, and this block, and write your own.

---

## Stories

Give each story a heading that starts with its label: US-01, US-02 and on. A label never changes
after you write it, and no two stories share one, because specs, tickets and the server cite it.
Under the heading, write the story, then quote the operator's own words that ask for it.

> **Good**
>
> ### US-01 Run a backtest
>
> As a trader, I can run a strategy against ten years of market history, so that I see what it
> would have earned.
>
> > "I want to see what the strategy would have done since 2015, before I put money in."

### US-01 <story title>

As a <user>, I can <act>, so that <outcome>.

> "<the operator's words>"

## Scope check

Do this section before you write anything else. There is no lower bound. A two-story epic is fine,
and costs one extra spec review. This section exists to catch an epic that is too big. The cost of
a big epic is not linear. The interactions between five touched modules are not the sum of five
changes, and the contract list does not name them.

Two questions decide the verdict. Size and story count are evidence. They are not the verdict.

### 1. Can each story land on its own?

When story 2 cannot merge without story 3, the two stories are one unit of work. That unit is
larger than the story count suggests. An epic whose stories must land together has no intermediate
state. Nothing in it is testable until all of it exists.

### 2. Can each promise be proven without building the rest?

This question matters most, because no person reads the code. The implementer writes the failing
test first. When a promise needs three other subsystems before anyone can prove it, there is no
test to fail. Red-green discipline is then impossible. A test that someone writes later asserts
whatever the finished code does.

Say "promise", not "contract". The spec author turns your acceptance criteria into numbered
contracts, one stage after this one. You cannot cite C1 here, because C1 does not exist yet. Name
the promise in plain words instead.

### Supporting signals

At about 8 stories and 8,000 lines, a reviewer can no longer keep the whole spec in mind at once.
Past that size, look again at the two questions above. But four stories at 2,000 lines each are
worse than eight stories at 400 lines each. Treat the numbers as a prompt, never as the answer.

| Shape | Rough size in lines |
| --- | --- |
| A screen with state, and its tests | 300 to 500 |
| An API route with input checks, and its tests | 150 to 250 |
| A database table, a migration and the queries | 150 to 300 |
| A pure module with property tests | 400 to 800 |
| An external service integration with retries and fixtures | 400 to 600 |

> **Good, and in range**
>
> Four stories, estimated 2,100 lines.
>
> **Lands independently?** Yes. The parser and the bar cursor each merge alone. The evaluator
> needs both, and the daily step needs the evaluator. Each story depends on the one before it, and
> each story still merges on its own.
>
> **Promises provable alone?** Yes. "No look-ahead" needs only the cursor. "Signals fill at the
> next open" needs the evaluator and a fixture, not a database. "A backtest matches the daily run"
> needs both drivers, and it is the last promise to prove.
>
> **Verdict: proceed.**
>
> **Good, and small**
>
> Two stories, estimated 900 lines. Both stories land alone, and each promise is provable alone.
> **Verdict: proceed.** Small is not a problem.
>
> **Good, and too big to proceed**
>
> Eleven stories, estimated 12,400 lines.
>
> **Lands independently?** No. US-03 writes the positions table. US-04 reads that table, and US-06
> ranks it. None of the three has meaning alone, so the first testable state comes after all three.
>
> **Promises provable alone?** No. "A rank reflects yesterday's close" needs the deploy flow, the
> daily job and the leaderboard. There is no failing test to write first.
>
> **Verdict: too large.** The recommended split is two epics. The first holds the positions table
> and the deploy flow. The second holds the daily results and the leaderboard, and depends on the
> first.
>
> **Bad, and why**
>
> - ~~"Scope looks reasonable."~~ Nobody can disagree with this.
> - ~~"Eight stories, within the limit, proceeding."~~ This counts the stories and asks neither
>   question. Eight stories that must land together are the epic this check exists to stop.
> - ~~Finding it too large, and writing the PRD anyway.~~ This wastes the work and hides the
>   finding. Stop, and say so.

Write the verdict and stop. Never divide the epic on your own judgement. A verdict other than
proceed fails the `prd` gate. The operator agrees to a split in the intake room, and the room
writes each PRD.

**Stories:** <n>
**Estimated total:** <n> lines
**Lands independently?** <yes / no, and which stories must land together>
**Promises provable alone?** <yes / no, and which promise needs what>
**Verdict:** <proceed / too large, and the recommended split>

## Goal

Write two or three sentences. State what a user can do at the end of this epic that they could not
do before. Name no implementation and no technology.

> **Good**
>
> A trader can run any strategy against ten years of real market history. The result gives the
> final value, the return, and the value at each step. The numbers match what the strategy would
> have earned if the trader had deployed it on the first day.
>
> **Bad.** ~~"Implement the backtesting engine."~~ This describes work. It does not say what
> becomes possible.

## Requirements

Each bullet is one testable requirement. On the line under each requirement, add a `→` citation
that names its source. A requirement with no source is an invention. Delete it, or raise it under
Open questions.

Citation syntax:

```
→ § US-nn                      a story of this PRD
→ <path>                       a file of the repository
→ <path> § <heading>           a heading in that file
```

The heading match ignores case, and matches on the opening words. For example, `§ Universal
constants` matches `## Universal constants, inherited by every strategy`. A path is relative to the
top of the repository.

> **Good**
>
> - A signal computed at one day's close fills at the next day's open, never at the same close.
>   → § US-01
> - Every strategy runs on price, volume and dividend data only.
>   → docs/adr/0003-market-data.md § Decision
>
> **Bad, and why**
>
> - ~~"The engine should be fast."~~ Not testable, and cited to nothing.
> - ~~"Results are cached for performance."~~ This is an implementation decision in the form of a
>   requirement. It belongs in the spec, where a reviewer can reject it.

- <Requirement, stated as behaviour that a user can observe.>
  → § US-01

## Acceptance criteria

The spec author turns this section into contracts. Write each criterion so that the spec author
can copy it with almost no change. When the spec author must reinterpret, split or merge your
criteria, this section failed. The meaning then drifts at the one step that nobody reviews.

Rules:

- **Number them.** A1, A2 and on. The spec's contracts cite them, as "C3 proves A2". That citation
  makes the trace readable in both directions: story, criterion, contract, ticket, test.
- **One criterion states one promise.** When a bullet needs the word "and", it probably states two.
- **Make each criterion falsifiable.** Say what observation would make it false. "Works as
  expected" cannot fail.
- **Write in product language, not technical language.** You state what must be true for a user.
  The spec states what must be true of the code.
- **Include what the product must refuse.** Authors drop the refusal half. An agent then does not
  build it, and does not say so.

> **Good**
>
> - **A1.** Two runs of the same strategy over the same dates return identical numbers.
> - **A2.** A backtest never trades on information from a date later than the bar it reads.
> - **A3.** A strategy file that has no value for a required parameter never runs. The refusal
>   names the parameter, and no default value takes its place.
>
> Each of these becomes one contract with almost no translation. A2 is the look-ahead invariant.
> A3 is a refusal contract.
>
> **Bad, and why**
>
> - ~~"Backtests are accurate and fast."~~ Two promises, and neither one is falsifiable.
> - ~~"The engine handles dividends, splits, and delistings."~~ Three requirements in one bullet.
>   The spec author must split it, and will split it differently than you meant.
> - ~~"The evaluator returns a result with a final value field."~~ This is technical language. It
>   is a contract already, so the spec author has nothing left to decide.

- **A1.** <What must be true, stated so that it could be shown false.>

## Boundaries with other epics

Name where this epic touches another epic, and say who owns what. The most common contradiction
takes two forms. Two epics build the same thing, and neither one knows. Or each epic assumes that
the other one owns a shared piece.

Read the other PRDs in `docs/prd/` before you write this section. Name every overlap that you find,
even when the answer is obvious. A reader can tell an overlap that you found and dismissed from one
that you never saw.

> **Good**
>
> - **Bars and dividends tables.** The market-data epic owns the schema and the ingestion. This
>   epic reads them and changes neither. This epic adds no migration.
> - **Result rendering.** The results-screen epic owns every screen. This epic returns a result
>   and renders nothing.
> - **Checked against:** the market-data PRD and the results-screen PRD. No conflicts found.
>
> **Bad, and why**
>
> - ~~"No overlaps."~~ With which epics? Unread is not the same as checked.
> - ~~"We may need to change the bars table."~~ Then say who decides. Shared ownership that nobody
>   resolves appears later as a merge conflict that nobody reads.

- **<Shared thing>.** <Who owns it, and what this epic may do with it.>
- **Checked against:** <the other PRDs that you read>. <The conflicts found, or none.>

## Out of scope

List what a reader would expect in this epic and will not find. This list stops the spec author
from making the epic larger. Name the owner of each excluded item.

Never put the look of this epic's own screens out of scope. When the project keeps design
references, name the ones that each screen matches.

> **Good**
>
> - Market data ingestion. The market-data epic owns it.
> - Any screen. The results-screen epic owns it.
> - Intraday data. Never, because the operator fixed the product at daily bars.
>
> **Bad.** ~~"UI work."~~ Which UI, and who owns it?

- <Thing>. It belongs to <an epic, a story, or never>.

## Open questions

List what the operator's words do not answer. The spec author must settle or escalate each
question. An empty section is normal and good. A question that you can answer by reading the
repository is not an open question. It means that you did not finish the reading.

> **Good**
>
> - The operator said "ten years of history". A symbol that listed five years ago has half of
>   that. Does it run from its first day, or not at all? This blocks A1.
>
> **Bad.** ~~"How should the engine be structured?"~~ That question belongs to the spec.

- <Question> Blocks: <which acceptance criterion>.
