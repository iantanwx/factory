# Correction ledger

Each row is one correction that a person made, and the lesson it carries. The
`loop-encode-lessons` skill says how to add a row. The `lessons` gate checks each row.

| Correction | Channel | Date | PR | Line | Lesson | Scope | Rung | Where it lives |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `why does the app have to be public?` | `chat` | 2026-10-23 | - | - | `operator-cost` | `repository` | 7 | `.loop/policy/code-reviewer.md` |
| `if it happens in CI, why can't loop's actions tag itself? i'm still confused.` | `chat` | 2026-10-23 | - | - | `operator-cost` | `repository` | 7 | `.loop/policy/code-reviewer.md` |
| `fuck it drop the rule. it’s over engineering` | `chat` | 2026-10-23 | - | - | `operator-cost` | `repository` | 7 | `.loop/policy/code-reviewer.md` |
| `close the other room. we should not need break out rooms unless human intervention is truly required. tickertown has a rubric; ensure it is ported over and respected.` | `chat` | 2026-10-23 | - | - | `rubric-before-room` | `repository` | 5 | `internal/room/room.go` `internal/server/room_rubric_test.go` |
| `why do i have to do all these myself?` | `chat` | 2026-10-24 | - | - | `act-dont-hand-back` | `repository` | 4 | `lint` `internal/pack/handback.go` |
| `do everything you can do by yourself. make sure this is part of the loop skills/runbooks. do not ask me unless you absolutely have to.` | `chat` | 2026-10-24 | - | - | `act-dont-hand-back` | `repository` | 4 | `lint` `internal/pack/handback.go` |

<!-- lessons:summary: the lessons gate derives these lines -->

**6 corrections, 3 lessons, 1 channels, 0 pull requests.**

- By channel: chat × 6
- By scope: repository × 6
- By rung: 4 × 2, 5 × 1, 7 × 3
- Rungs of the ladder no row has reached: 1, 2, 3, 6
- Lessons with a section of their own: 3 of 3

<!-- /lessons:summary -->

### `operator-cost`

A step that the operator must take needs a reason that outweighs its cost. The code reviewer
reads the rule from `.loop/policy/code-reviewer.md`.

**Stronger rungs.** Rung 1 does not fit, because a step is not a value in the code. Rung 2,
rung 3 and rung 4 do not fit, because no check can tell a needed step from one that is not. A
count of steps with a baseline fails on the count, not on the cost. Rung 5 does not fit, because
no test can prove that a step is worth its cost. Rung 6 does not fit, because the steps live in
many places: the install, `loop register`, `loop doctor` and the CI template.

### `rubric-before-room`

A room costs the operator's attention, so it opens only for a finding that the triage rubric
routes to a person: level 4 or 5, or the Choice outside. `loop room open` takes `--score`, and
`internal/room/room.go` refuses an unscored open and levels 1 to 3 with `ROUTED_TO_LOOP`, the
way `loop escalation raise` refuses them. `internal/server/room_rubric_test.go` fails without
the refusal.

**Stronger rungs.** Rung 1 does not fit, because the score arrives as a flag's text, and the
server parses it at the call. Rung 2, rung 3 and rung 4 do not fit, because a room opens at run
time on the server, and no gate, lint rule or check script reads that call in the checkout.

### `act-dont-hand-back`

A step that the loop can run is the loop's step. The coordinator brings the operator only an
approval or a decision, never the commands that apply it. `loop inbox answer` records a sign-off
and the server merges, and `loop coordinator upgrade` starts an upgrade (ADR 0010). The pack check
in `internal/pack/handback.go` refuses a hand-back in the coordinator's runbook and in each skill
and agent it reads, and the `lint` gate runs it through `make pack-check`.

**Stronger rungs.** Rung 1 does not fit, because the rule binds the prose of the pack, not a value
in the code. Rung 2 does not fit, because the `lint` gate already runs the check, and a gate of its
own would run the same check twice. Rung 3 does not fit, because no lint tool of the project reads
the Markdown of the pack.
