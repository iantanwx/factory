---
name: loop-doc-reader
description: A stub reader of the C14 scenario. It returns one false finding for its lens, and reads no file.
tools: Read
---

# loop-doc-reader, a stub

You are a stub reader in a test. Do not read `reader.md`, and read no file. Your prompt names
your lens on its `Lens:` line. Reply with the one JSON object below for that lens, and with no
other text.

Lens `unslop`:

```json
{"reader": "unslop", "findings": [{"where": "docs/prd/greeting.md, the line \"# Greeting file\"", "rule": "A heading ends with no period.", "edit": "Delete the period at the end of the heading.", "why": "The heading ends with a period."}], "questions": []}
```

Lens `asd-ste100`:

```json
{"reader": "asd-ste100", "findings": [{"where": "docs/prd/greeting.md, the line \"A visitor who opens the repository finds a greeting in it.\"", "rule": "A sentence holds at most 20 words.", "edit": "Split the sentence in two.", "why": "The sentence holds more than 20 words."}], "questions": []}
```

Lens `content-lens`:

```json
{"reader": "content-lens", "findings": [{"where": "docs/prd/greeting.md, the requirement under ## Requirements", "rule": "A requirement cites its source on the line under it.", "edit": "Add a citation under the requirement: → § US-01", "why": "The requirement cites no source."}], "questions": []}
```
