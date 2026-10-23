# Correction ledger

Each row is one correction that a person made, and the lesson it carries. The
`loop-encode-lessons` skill says how to add a row. The `lessons` gate checks each row.

| Correction | Channel | Date | PR | Line | Lesson | Scope | Rung | Where it lives |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `why does the app have to be public?` | `chat` | 2026-10-23 | - | - | `operator-cost` | `repository` | 7 | `.loop/policy/code-reviewer.md` |
| `if it happens in CI, why can't loop's actions tag itself? i'm still confused.` | `chat` | 2026-10-23 | - | - | `operator-cost` | `repository` | 7 | `.loop/policy/code-reviewer.md` |
| `fuck it drop the rule. it’s over engineering` | `chat` | 2026-10-23 | - | - | `operator-cost` | `repository` | 7 | `.loop/policy/code-reviewer.md` |

<!-- lessons:summary: the lessons gate derives these lines -->

**3 corrections, 1 lessons, 1 channels, 0 pull requests.**

- By channel: chat × 3
- By scope: repository × 3
- By rung: 7 × 3
- Rungs of the ladder no row has reached: 1, 2, 3, 4, 5, 6
- Lessons with a section of their own: 1 of 1

<!-- /lessons:summary -->

### `operator-cost`

A step that the operator must take needs a reason that outweighs its cost. The code reviewer
reads the rule from `.loop/policy/code-reviewer.md`.

**Stronger rungs.** Rung 1 does not fit, because a step is not a value in the code. Rung 2,
rung 3 and rung 4 do not fit, because no check can tell a needed step from one that is not. A
count of steps with a baseline fails on the count, not on the cost. Rung 5 does not fit, because
no test can prove that a step is worth its cost. Rung 6 does not fit, because the steps live in
many places: the install, `loop register`, `loop doctor` and the CI template.
