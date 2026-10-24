# ADR 0006: Check the model of each stage run, recover a twice-failed item once, and port no run-side guard

**Status:** Proposed
**Date:** 2026-10-24
**Ticket:** factorio-jiv

## Context

The tickertown parity audit of 2026-10-23 found five gaps in the guards that keep a run honest and
recoverable. The operator asked to "identify all gaps, draft requirements for them, then push them
through the entire SDLC process", and asked for an ADR. Tickertown is the repository at
`/home/operator/projects/alpha`, read on 2026-10-24 at the head of its main branch. The loop was
read at the base `25e1b54`.

The epic body names the five gaps. `loop work show` refuses an implementer run that reads its epic
(loop fault F-8). So this run read the body from the `loop epic create` command in the
coordinator's transcript.

**Gap 1: stage runs start with no model check.** Measured in source:

- `internal/agenthost/orca/receipt.go:24` reads the model that the harness recorded. It compares
  that model with the requested one exactly.
- `internal/agenthost/orca/start.go:78` waits for a receipt that reads `Verified` only when the start sets
  `RequireVerifiedModel`. `internal/agenthost/host.go:189` reserves that field for "an
  escalated-model role" (plan R6).
- No caller sets the field. `grep -rn RequireVerifiedModel` finds only the contract suite, the
  fake host and the orca host. `internal/sched/dispatch.go:171` starts every stage run without it.
- `internal/sched/quota.go:25` sets the default Claude model to the alias `opus`. This run's brief
  names the model `opus`. Its transcript records `claude-opus-5-5` on all 48 assistant records. So
  the exact comparison would refuse every default run today.
- `awaitReceipt` (`start.go:139`) returns `ErrModelUnverified` for two different facts: the
  harness records another model, or no transcript names a model within 3 minutes.
- Tickertown runs `pnpm run-model verify <row>` from inside each timed run
  (`skills/sdlc-turn/SKILL.md` section 1). Exit 1, another model, stops the run. Exit 2, no model
  read, continues the run, because each transcript store is a vendor's mechanism. Its pins are
  full model ids (`scripts/sdlc/run-model.ts`).

**Gap 2: no job outlives its run.** Measured in source:

- Tickertown's `pnpm detach start` hands a long command to `systemd-run --user`
  (`scripts/sdlc/detach.ts`). The cause was orca: it reaped a session at the next dispatch of the
  session's timer, and Claude Code killed each background command with it. Runs 53 and 91 lost a
  recording night on 2026-10-19.
- The loop dispatches no orca timer. Plan R22 lists `detach`, `run-model` and `alert-check` among
  the commands the loop does not carry over.
- A session that ends with no `loop run finish` settles at `internal/sched/liveness.go:209`, and
  KTD8 classes it `crashed`. It counts once against the failure limit. A session with a tool call
  still running has not ended (KTD8). A run past its budget keeps running (KTD9).

**Gap 3: a run cannot send free text to the coordinator.** Measured in source:

- `loop schema` lists `coordinator send` for the `host` principal alone.
- Tickertown's `pnpm alert` (`scripts/sdlc/situation-room.ts`) carries three kinds of event
  (`skills/sdlc-turn/SKILL.md`): a failed `run-model verify`, an escalation, and a broken gate or
  tool.
- The loop's inbox is a derived view (KTD21, `internal/inbox/inbox.go`). A run reaches it through
  three typed verbs. `loop escalation raise` adds an escalation, and the server refuses levels 1
  to 3 with `ROUTED_TO_LOOP`. `loop fault report` adds a fault entry (KTD31). `loop run finish
  --outcome blocked` raises a `run_blocked` alert.

**Gap 4: a twice-failed item waits on a person.** Measured in source:

- `internal/sched/finish.go:104` raises a `retry_limit` alert when an item fails twice at the same
  inputs. Only changed inputs admit a new attempt (plan R10, `internal/admission/policy.go:340`).
- `pack/runbooks/coordinator.md:72` answers that alert with `loop work edit`, using "the
  operator's text". So each such item waits on the operator.
- Tickertown's fixer takes each such failure in a recovery mode (`docs/agents/fixer.md`, lines
  66-110, and `scripts/sdlc/fixer-recovery.ts`). It repairs the task inputs, waits on a named
  dependency, or settles `unresolved`. The queue prefers failures with fewer recovery attempts. It
  sets no limit on recovery attempts.

**Gap 5: the loop ships no harness pre-tool hook.** Measured in source and in run transcripts:

- Tickertown tracks `.claude/settings.json`, which runs `scripts/sdlc/shell-guard.ts` before each
  Bash call. It refuses five patterns: `worktree`, `pipe`, `colon`, `env` and `heredoc`.
- I ran those patterns over 3,348 Bash calls in the 127 loop run transcripts on this host
  (`~/.claude/projects/-home-operator-orca-workspaces-loop-loop-factorio-*`):
  - `git worktree add`: 50 runs, 32 code-reviewer and 18 implementer.
    `pack/runbooks/code-reviewer.md:60` requires a second worktree at the merge base.
  - A piped `loop gates` and a push in one command: 1 call, by an implementer.
  - An unquoted heredoc with a backtick or `$(` in a loop write command: 0 calls.
  - `git push`: 0 calls.
- `colon` guards `--force-with-lease`, and `env` guards an append to `.env`. A loop run does
  neither. The server pushes (KTD14), and the server writes `.env`.
- The reactor raises `foreign_head` for a pull request head that the server did not push
  (`internal/github/reactor.go:28`).
- The loop also starts Codex runs. `shell-guard.ts` is a Claude Code hook.

**What I assumed, and did not measure:**

- A first assistant record reaches the transcript well inside the 3-minute receipt window.
- Codex records the same id that the loop requests, `gpt-5-codex`. I read no Codex rollout.
- No loop runbook needs a command that runs longer than one tool call. Claude Code bounds one call
  at 10 minutes.
- Codex has no pre-tool hook that matches Claude Code's.
- A run's checkout has `origin` set to `git@github.com:slothware-labs/factorio.git`. I did not
  test whether a run's own `git push` succeeds.
- I did not count `retry_limit` alerts. Only the coordinator can read the inbox.

## Decision

1. **Check the model receipt of every stage run at start, and refuse only a recorded mismatch.**
   The server sets the receipt check on each start at `dispatch.go:171`. When the harness records
   another model, the start fails and does not count against the item. When no transcript names a
   model in the window, the run starts, and the server raises one alert for each stage. I rejected
   a refusal on both facts, because a vendor's change to its transcript store would stop every
   stage at once. I rejected an alert alone on a mismatch, because a run on the wrong model spends
   another model's quota, and its judgement is not the one the stage chose.

2. **Name each stage model by the full id that the harness records.** The default Claude model
   becomes a full id, and project config validation refuses a Claude Code alias. I rejected a
   comparison that accepts an alias. The harness resolves the alias, so a harness update could move
   every run to another model, and the receipt exists to catch exactly that.

3. **Do not port `detach`, and let no job outlive its run.** Plan R22 lists `detach` as a
   workaround of orca's timer reaping. The loop dispatches no orca timer. A run that ends its turn
   to wait settles `crashed` (KTD8), which is the honest class. I rejected a `loop run detach` verb
   that hands a job to the server. The server would then run project code outside any session, so
   no lease, run budget or transcript would cover that job.

4. **Do not add a free-text alert verb for runs.** Each tickertown use already has a typed path in
   the loop: decision 1 for the model, `loop escalation raise` for an escalation, and `loop fault
   report` or `loop run finish --outcome blocked` for a broken tool. I rejected a run-callable
   `coordinator send`. Free text skips the triage gate that refuses levels 1 to 3. The operator
   asked for that rubric to be "ported over and respected".

5. **Give each twice-failed item one unattended recovery in the fixer stage.** The fixer takes a
   held item, reads its failures, and does one of three things. It edits the item with `loop work
   edit`, closes it with `loop work supersede`, or escalates it with a score. A second
   `retry_limit` on the same item goes to the coordinator, as today. I rejected tickertown's
   unlimited recovery. Each recovery edit makes new inputs, so the failure limit (R13) would no
   longer bound the attempts on one item. I rejected a new recovery stage, because R13 keeps the
   stage set. I rejected the coordinator alone, because it brings each item to the operator.

6. **Ship no harness pre-tool hook from the loop.** `worktree` would refuse a step that the
   code-reviewer runbook requires, in 50 of 127 runs. `colon` and `env` guard acts that only the
   server does. `heredoc` matched 0 calls. The one piped gate pushed to a pull request, and CI and
   the merge gate still refuse a red build. I rejected an injected `.claude/settings.json`. The
   loop refuses a run whose project tracks an injected path (`CONFIG_CONFLICT`), and tickertown
   tracks that file. The hook would also leave Codex runs unguarded. A project keeps its own hook
   in its own settings, as tickertown does.

## Consequences

Files, commands and stages that change, for the spec of epic factorio-jiv:

- `internal/sched/dispatch.go`: each stage start requests the model receipt check.
- `internal/agenthost/orca/start.go` and `internal/agenthost/host.go`: the start returns a
  mismatch and a missing receipt as two errors. A mismatch refuses the start. A missing receipt
  starts the session. The `RequireVerifiedModel` comment no longer names a role.
- `internal/agenthost/fake/fake.go` and `internal/agenthost/agenthosttest/contract.go`: the fake
  and the contract suite prove both cases.
- `internal/sched`: two alert kinds, one for a mismatch and one for a missing receipt. Neither
  start counts against the item.
- `internal/sched/quota.go`: `DefaultModels` names a full Claude model id.
- `internal/config`: stage config validation refuses a Claude Code model alias.
- `pack/runbooks/fixer.md`: a recovery section. `loop work peek --stage fixer` lists a held item
  that has had no recovery. The server admits the fixer onto that item, and the item records the
  recovery. The spec names the verbs, the fact and the work query.
- `internal/sched/finish.go`: the `retry_limit` alert waits for the coordinator only after a
  recovery left the item held, or on the item's second limit.
- `pack/runbooks/coordinator.md:72`: the coordinator answers a `retry_limit` alert only after the
  fixer's recovery.
- Stages: the fixer gains the recovery mode. This adds no stage and removes no stage.

What this makes easy: a run on the wrong model never starts, and most twice-failed items reach a
person only after the loop tried once.

What this makes hard:

- A new model release needs a config edit or a new loop release, because config validation refuses an alias.
- A run cannot run a job longer than one tool call. A project that needs one waits for a decision
  that this ADR postpones.
- A shell mistake in a run is caught later: by CI, by the code-reviewer, or by `foreign_head`.

Postponed on purpose:

- A job that outlives its run. The tickertown cutover forces it, because tickertown runs recording
  nights. The plan defers that cutover (plan, Scope Boundaries, "Deferred for later").
- Push access from a run's checkout. If a run's own `git push` succeeds, it skips the push scan.
  `foreign_head` reports it after the push. A test of that push forces the decision.

A later reader must not:

- Compare a requested model alias with a recorded id as a match.
- Refuse a stage start only because no transcript named a model yet.
- Reset the failure count of an item, or give it a second unattended recovery.
- Add a verb that puts a run's free text in the inbox outside the triage gate.
- Inject a harness settings file into a project checkout.

## Acceptance criteria

Each line runs from the repository root, and each one can fail on the day this ADR lands.

- `grep -rn 'coordinator send' pack/runbooks pack/skills` prints nothing.
- `ls pack/skills pack/runbooks | grep -i -E 'detach|shell-guard|run-model'` prints nothing.
- `find . -path ./node_modules -prune -o -name settings.json -path '*pack*' -print` prints
  nothing.
- `grep -rn 'git worktree add' pack/runbooks/code-reviewer.md` prints at least one line.
- `loop gates --docs` passes.

These criteria wait on later work, and epic factorio-jiv owns each one:

- `grep -n 'RequireVerifiedModel' internal/sched/dispatch.go` prints the stage start.
- `go test ./internal/agenthost/...` proves that a recorded mismatch refuses the start, and that a
  missing receipt starts the session.
- `grep -n '"opus"' internal/sched/quota.go` prints nothing.
- Project config with a stage model `opus` fails validation, and a test proves it.
- `make acceptance` proves that a held item gets one fixer recovery, and that a second limit
  reaches the coordinator's inbox.
