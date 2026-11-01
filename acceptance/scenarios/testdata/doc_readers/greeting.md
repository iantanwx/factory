# Greeting file

## Stories

### US-01 Read the greeting

As a visitor to the repository, I can read a greeting in the file `greeting.txt`, so that I know
the project welcomes me.

> "Add a file greeting.txt that holds the one line hello, world."

## Scope check

One story, estimated 10 lines. The story lands alone, and its one promise needs nothing else.

**Stories:** 1
**Estimated total:** 10 lines
**Lands independently?** Yes. The epic holds one story.
**Promises provable alone?** Yes. A read of `greeting.txt` proves the greeting.
**Verdict:** proceed.

## Goal

A visitor who opens the repository finds a greeting in it. Before this epic, the repository holds
no greeting.

## Requirements

- The repository holds the file `greeting.txt`, and the file holds the one line `hello, world`.
  → § US-01

## Acceptance criteria

- **A1.** The file `greeting.txt` holds exactly the one line `hello, world`. Any other text, or no
  file, makes this false.

## Boundaries with other epics

- **Checked against:** the repository holds no other PRD under `docs/prd/`. No conflicts found.

## Out of scope

- A greeting in any other language. It belongs to no epic, because the operator asked for one
  line.

## Open questions

None.
