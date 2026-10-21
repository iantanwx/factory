#!/usr/bin/env python3
"""Checks the mechanical rules of the unslop skill.

The unslop skill lists 30 rules. Most need judgement: an aphorism, a forced rule of
three, a sentence that names a feeling instead of a mechanism. Those need a reader.

A minority are pure pattern matches, and those belong in a gate rather than in anyone's memory.
This checks only those. A clean run means the mechanical rules hold. It says nothing about the
rest, in the same way ste-lint.py says nothing about word choice.

Rule numbers below are the skill's own, which are stable ids. Keep them in step.

Usage:
    unslop-lint.py FILE [FILE ...]
    unslop-lint.py --selftest
"""
import re
import sys

# Fenced blocks and inline code are skipped: a command is not prose, and an em dash inside a
# quoted bad example is the lesson rather than our mistake.
CODE_FENCE = re.compile(r"^\s*```")
INLINE_CODE = re.compile(r"`[^`\n]+`")

RULES = [
    ("em-dash", re.compile(r"[—–]"),
     "Rule 13: no em or en dashes. End the sentence, or use a comma."),
    ("curly-quote", re.compile(r"[‘’“”]"),
     "Rule 19: use straight quotes."),
    ("ai-vocabulary", re.compile(
        r"\b(additionally|crucial|delve|enduring|enhance|fostering|garner|interplay|intricate|"
        r"pivotal|showcase|tapestry|testament|underscore|vibrant)\b", re.I),
     "Rule 7: AI vocabulary. Use a plain word."),
    ("fancy-is", re.compile(r"\b(serves as|stands as|boasts|features)\b", re.I),
     "Rule 8: a fancy way to say is or has. Say is or has."),
    ("not-just", re.compile(r"\bnot just\b[^.!?]{0,60}?,\s*but\b", re.I),
     "Rule 9: 'not just X, but Y'. State the point directly."),
    ("filler", re.compile(
        r"\b(in order to|due to the fact that|it is important to note that|"
        r"it should be noted that|needless to say)\b", re.I),
     "Rule 23: filler. Delete it, or use the short form."),
    # `leverage` is matched only as a verb. As a noun it can be a precise term, as in finance.
    # Rule 31 objects to using it in place of "use", which is the verb, so the pattern requires an
    # object after it.
    ("fancy-word", re.compile(
        r"\b(utili[sz]e[sd]?|leverag(?:es|ing)\b|leverage\s+(?:the|our|a|an|this|that|these|those|existing)|"
        r"facilitate[sd]?|numerous|in the event that)\b", re.I),
     "Rule 31: use the plain word."),
    ("metaphor-noun", re.compile(
        r"\b(substrate|wedge|locus|vantage|nexus|bedrock|modality|paradigm|"
        r"gold-plating|flywheel|north star|endgame)\b", re.I),
     "Rule 26: abstract metaphor noun. Use the concrete word."),
    ("chatbot", re.compile(
        r"(I hope this helps|Let me know if|Of course!|Certainly!|Great question|"
        r"You'?re absolutely right)", re.I),
     "Rule 20 and 22: chatbot or sycophantic phrasing. Remove it."),
]

HEADING = re.compile(r"^(#{1,6})\s+(.*)$")
# Only the phrases that carry no content in a heading. The general rule needs judgement: an
# "X, not Y" heading often names a rejected alternative, so a pattern for that shape would be
# wrong more often than right.
SELF_PRAISE = re.compile(
    r"(rather than hidden|not hidden|recorded rather than|all \w+ recorded|and why it matters|"
    r"done right|as it should be|the right way)", re.I)
EMOJI = re.compile("[\U0001F300-\U0001FAFF☀-➿️⬀-⯿]")
# Title Case: three or more capitalised words in a row, ignoring code and short joining words.
TITLE_CASE = re.compile(r"^(?:[A-Z][a-z]+\s+){2,}[A-Z][a-z]+")


def lint(text, filename):
    findings = []
    in_fence = False
    for n, raw in enumerate(text.split("\n"), 1):
        if CODE_FENCE.match(raw):
            in_fence = not in_fence
            continue
        if in_fence:
            continue
        # A line indented four spaces or more is an indented code block, and a comment inside a
        # command listing is not prose. List continuations indent by two, so four is a safe
        # threshold.
        if raw.startswith("    ") and raw.strip():
            continue
        line = INLINE_CODE.sub(" CODE ", raw)

        head = HEADING.match(line)
        if head:
            title = head.group(2)
            if EMOJI.search(title):
                findings.append((n, "emoji-heading", "Rule 18: no decorative emoji in a heading."))
            if TITLE_CASE.match(title.strip()):
                findings.append((n, "title-case", "Rule 17: use sentence case in a heading."))
            m = SELF_PRAISE.search(title)
            if m:
                findings.append((n, "self-praise", "Rule 34: a heading names its content, not the "
                                 f"virtue of writing it down. Cut the clause. [{m.group(0)}]"))

        for name, pattern, message in RULES:
            for m in pattern.finditer(line):
                findings.append((n, name, f"{message} [{m.group(0)}]"))
    return [(filename, n, k, msg) for n, k, msg in findings]


def selftest():
    cases = [
        ("a sentence — with a dash", "em-dash"),
        ("it is crucial to delve in", "ai-vocabulary"),
        ("the module serves as a bridge", "fancy-is"),
        ("in order to run it", "filler"),
        ("we utilize the parser", "fancy-word"),
        ("the substrate below", "metaphor-noun"),
        ("## Title Case Heading Here", "title-case"),
        ("## Known limitations, recorded rather than hidden", "self-praise"),
        ("## Decisions, all twelve recorded", "self-praise"),
        ("## Warning ⚠️", "emoji-heading"),
    ]
    bad = []
    for text, want in cases:
        got = {k for _, _, k, _ in lint(text, "t")}
        if want not in got:
            bad.append(f"{want!r} not found in {text!r} (got {got})")
    clean = lint("The parser rejects a bad date. It exits with code 2.", "t")
    if clean:
        bad.append(f"false positive on clean prose: {clean}")
    if bad:
        print("selftest FAILED")
        for b in bad:
            print(" ", b)
        return 1
    print("selftest OK")
    return 0


def main(argv):
    if "--selftest" in argv:
        return selftest()
    files = [a for a in argv if not a.startswith("--")]
    findings = []
    for f in files:
        findings += lint(open(f, encoding="utf-8").read(), f)
    for name, n, kind, message in findings:
        print(f"{name}:{n} {kind}: {message}")
    print(f"\n{len(findings)} unslop finding(s)")
    return 1 if findings else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
