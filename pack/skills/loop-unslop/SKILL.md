---
name: loop-unslop
description: Cut AI tells from any writing. Must always apply.
disable-model-invocation: true
---

<!--
Ported from skills/unslop/SKILL.md in tickertown at commit
64e293f9ae2db7831d12462e76ffe1aa3deacd8c, the head of its main branch on 2026-10-23.

Tickertown recorded this fork line:

FORKED from cursor/plugins, and ours from here on.

  upstream: https://github.com/cursor/plugins/tree/main/pstack/skills/unslop
  commit:   70b2dc8b4b85c8d5648624ca40d692c421fff32f
  forked:   2026-09-28

Recorded so that a later reader can diff against upstream and see what we changed on purpose.
This is not a dependency we track, and we do not owe it updates. Rule numbers are stable ids that
other skills cite, so keep the numbering when you edit, and leave a gap when you delete a rule.

Changed on purpose, 2026-09-28: every banned word and every bad example now sits in a code span,
and the explanations are split into short active sentences. Tickertown's prose gate covered this
file, and it read 50 unslop findings and 9 hard STE violations here before the change. Most were
mentions, not uses. Both linters skip code spans and fenced blocks, so a mention in backticks
reads as the mention it is. The rest were real: six sentences over the 25-word cap, four passives,
and three pairs of synonyms for one action.

Keep that shape. `make pack-check` runs both linters over this file in the loop. A new banned word
goes in backticks, or this file fails its own gate.

Keep every code span on one line. A span wrapped across a line break reads as prose to the
linter, and the example inside it then counts as a fault.

Edit the copy in the loop's pack. The server writes `.claude/skills/` again at each start.
-->
# Unslop

Edit text to delete AI patterns.

Rule numbers are stable ids that other skills cite. A deleted rule leaves a gap.

Every banned word below sits in a code span. That is deliberate. This file names the patterns it
forbids, so it has to hold them, and the linters skip code spans.

## Process

1. Scan for the patterns below.
2. Rewrite. Keep the meaning, and match the tone the writer wanted.

## Patterns to detect and correct

### Content

3. **Superficial -ing phrases.** `highlighting...`, `ensuring...`, `reflecting...`,
   `showcasing...`, `fostering...`. Cut the phrase, or replace it with a real source.
5. **Vague attributions.** `Experts believe`, `Industry reports suggest`,
   `Some critics argue`. Name the source, or cut the claim.

### Language

7. **AI vocabulary.** `Additionally`, `crucial`, `delve`, `enduring`, `enhance`, `fostering`,
   `garner`, `interplay`, `intricate`, `landscape` (abstract), `pivotal`, `showcase`, `tapestry`
   (abstract), `testament`, `underscore`, `vibrant`. Use a plain word.
8. **Fancy ways to say is.** `serves as`, `stands as`, `boasts`, `features`. Write `is` or `has`.
9. **`Not just X, but Y`.** State the point directly.
10. **Rule of three.** Do not force ideas into groups of three. Use the real number.
11. **Synonym cycling.** `protagonist`, `main character`, `central figure` and `hero` in one
    paragraph. Pick one word and repeat it.
12. **False ranges.** `from X to Y`, where X and Y sit on no shared scale. List the topics.

### Style

13. **Em dashes.** Use no em dash. Use a full stop or a comma. Use no parenthesis, no en dash, and
    no hyphen standing in for a dash. If a thought needs separation, end the sentence.
14. **Colons.** A colon is correct before a list or an example. A colon is wrong as a mid-sentence
    joint. `If you're coming from traditional automation: instead of registering event handlers,
    you describe conditions` gains nothing from the colon. Drop the comparison and let the point
    stand alone. `Describing when the scheduler should fire works best as plain English` says the
    same thing without the crutch.
15. **Boldface.** Do not bold every proper noun or acronym.
16. **Inline-header lists.** The tell is a bold label with a colon that restates the line, as in
    `**Performance:** Performance improved...`. Write those as prose. A bold lead-in is fine when
    it ends in a full stop, names the item, and carries new detail after it. `**Schema in
    TypeScript.** Tables live in one file` is fine.
17. **Title case headings.** Use sentence case.
18. **Decorative emojis.** Cut them from headings and bullets.
34. **Self-praising headings.** A heading names its content. It does not praise the act of writing
    it. `Known limitations, recorded rather than hidden` becomes `Known limitations`. `Decisions,
    all twelve recorded` becomes `Decisions`. Naming a rejected alternative is different, and it
    stays: `Fetch per ticker, not in bulk` is the decision, not a boast.
19. **Curly quotes.** Use straight quotes.

### Communication artifacts

20. **Chatbot phrases.** `I hope this helps!`, `Let me know if...`, `Of course!`, `Certainly!`,
    `Found the smoking gun!`. Cut them.
22. **Sycophantic tone.** `Great question!`, `You're absolutely right!`. Answer directly.

### Filler

23. **Filler phrases.** `In order to` becomes `To`. `Due to the fact that` becomes `Because`.
    Cut `It is important to note that`.
24. **Excessive hedging.** `could potentially possibly be argued that it might` becomes `may`.
25. **Generic conclusions.** `The future looks bright` says nothing. State a plan or a fact.

### Jargon

26. **Abstract metaphor nouns.** `substrate`, `wedge`, `vector`, `locus`, `vantage`, `nexus`,
    `primitive` (as a noun), `harness` (as a metaphor), `surface` (as in `API surface`),
    `bedrock`, `scaffolding` (as a metaphor), `modality`, `paradigm`, `gold-plating`, `ratchet`
    (as a metaphor), `evacuate` (for moving code), `endgame`, `north star`, `flywheel`.

    Each reads as technical and has a plainer word. `substrate` becomes `base`. `wedge in`
    becomes `add`. `vector` becomes `way` or `method`. `gold-plating` becomes
    `more than the job needs`. `ratchet` becomes the real name of the mechanism, or
    `a limit that only tightens`. `evacuate` becomes `move out`. `endgame` becomes
    `the last phase`. Pick the concrete word.

### Plain speech

27. **Say what it does, not how it feels.** `the database stays close at hand`,
    `SQL you can read` and `types that follow your schema` all name a feeling.

    Name the mechanism or a number instead. `.toSQL() returns the exact string sent to the
    database`. `A column rename fails the build`.

    Ask what the sentence tells the reader to do or know. Then write that. If you cannot restate
    it as an instruction, a fact or a number, cut it. One more check. If the sentence would fit
    another project's documents unchanged, it says nothing about this one. Cut it.
28. **Dense sentences.** If the reader has to read a sentence twice, split it or drop a clause.
    One idea per sentence.
29. **Active voice.** Catch `is`, `are`, `was` or `were` before a past participle, then name the
    actor. `queries are validated` becomes `the compiler checks queries`.
    `the file is parsed by the loader` becomes `the loader reads the file`.
    Passive is correct only when the actor is unknown, or when the actor does not matter.
30. **Adverbs.** Cut the adverb, or use a stronger verb. `runs quickly` becomes `is fast`, or the
    number. `significantly improves` becomes the measured difference. An adverb holding up a weak
    verb means the verb is wrong.
31. **Plain words.** `utilize` becomes `use`. `leverage` becomes `use`. `facilitate` becomes
    `help`. `numerous` becomes `many`. `in the event that` becomes `if`. The longer synonym is
    rarely clearer.
32. **Mannered prose.** A metaphor or a flourish where a literal phrase exists. This covers
    aphorisms such as `wire it or delete it`, rhetorical fragments, personified code such as
    `the plan holds it`, figurative verbs such as `rides along` and `stands on`, and stock
    framing phrases. `A dial worth turning` becomes `a parameter worth varying`. Say what you
    mean. Rule 26 covers the metaphor nouns.

    Every run's prompt and every agent of the pack quote this definition from Anthropic's
    writing-density guidance. Check prose against it:

    ```text
    Mannered prose substitutes metaphor and flourish for direct statement. Instead of "a parameter
    worth varying," the mannered writer produces "a dial worth turning." Instead of "this point
    still matters," they write "this point earns its keep." The phrases exist to display the writer,
    not to convey the idea, and readers can tell. That is why mannered prose irritates: it makes the
    reader work harder so the writer can perform. It is also imprecise. Metaphors drag in
    connotations the writer did not choose and cannot control. The fix is to say what you mean. When
    a literal phrase is available, use it.
    ```

33. **Over-compression.** Dropped articles, verbless fragments, symbol-speak, and abbreviations
    all make the reader decode instead of read. `Parser rejects bad date → exit 2, no write`
    becomes `The parser rejects a bad date, exits with code 2, and writes nothing`. Write whole
    sentences with their articles and verbs. Spell out arrows and abbreviations.
