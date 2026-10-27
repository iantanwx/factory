# Run integrity: technical spec

## Source

- **Epic:** factorio-jiv
- **Source:** `docs/adr/0006-run-integrity.md`, the one ADR whose `**Ticket:**` line names
  factorio-jiv. It is on the base branch at `7aad03b`.
- **Stories covered:** none. This is an architecture epic, and no story asks for it.

The ADR does not number its decisions or its acceptance criteria, so this spec does. The contracts
below cite these labels.

| Label | ADR text |
| --- | --- |
| D1 | Decision 1: check the model receipt of every stage run at start, and refuse only a recorded mismatch |
| D2 | Decision 2: name each stage model by the full id that the harness records |
| D3 | Decision 3: do not port `detach`, and let no job outlive its run |
| D4 | Decision 4: do not add a free-text alert verb for runs |
| D5 | Decision 5: give each twice-failed item one unattended recovery in the fixer stage |
| D6 | Decision 6: ship no harness pre-tool hook from the loop |
| AC-1 | `grep -rn 'coordinator send' pack/runbooks pack/skills` prints nothing |
| AC-2 | `ls pack/skills pack/runbooks \| grep -i -E 'detach\|shell-guard\|run-model'` prints nothing |
| AC-3 | `find . -path ./node_modules -prune -o -name settings.json -path '*pack*' -print` prints nothing |
| AC-4 | `grep -rn 'git worktree add' pack/runbooks/code-reviewer.md` prints at least one line |
| AC-5 | `loop gates --docs` passes |
| AC-6 | `grep -n 'RequireVerifiedModel' internal/sched/dispatch.go` prints the stage start |
| AC-7 | `go test ./internal/agenthost/...` proves that a recorded mismatch refuses the start, and that a missing receipt starts the session |
| AC-8 | `grep -n '"opus"' internal/sched/quota.go` prints nothing |
| AC-9 | Project config with a stage model `opus` fails validation, and a test proves it |
| AC-10 | `make acceptance` proves that a held item gets one fixer recovery, and that a second limit reaches the coordinator's inbox |
| K-1 | A later reader must not compare a requested model alias with a recorded id as a match |
| K-2 | A later reader must not refuse a stage start only because no transcript named a model yet |
| K-3 | A later reader must not reset the failure count of an item, or give it a second unattended recovery |

AC-1 to AC-5 already hold on the base. This run ran AC-1 to AC-4 at `7aad03b`: AC-1 and AC-2
printed nothing (exit 1), AC-3 printed nothing, and AC-4 counted 1 line. CI proves AC-5 on each pull
request of this epic, because `.github/workflows/ci.yml` runs `loop gates`. D3, D4 and D6 decide to
build nothing, so no work entry claims them (Decisions). D5, AC-10 and K-3 belong to epic
factorio-1okx (Out of scope).

## Scope check

**Components:**

1. **The stage start guard**: the model receipt in `internal/agenthost` (the interface, the orca
   host, the fake host and the contract suite), the stage start in `internal/sched/dispatch.go`,
   the default models in `internal/sched/quota.go`, and stage config validation in
   `internal/config/project.go`.

Every work entry changes this component alone.

**Not in this spec:** the model check of the coordinator and of rooms, which ADR 0006 does not
decide. A job that outlives its run, which ADR 0006 postpones to the tickertown cutover. The
twice-failed item recovery (D5), which belongs to epic factorio-1okx by the operator's split
(Open questions).

## Decisions

### The stage start guard

- **Start returns a recorded mismatch and a missing receipt as two errors.** We chose two
  sentinels: `ErrModelMismatch`, after which the host has closed the session, and
  `ErrNoModelReceipt`, which returns the live session. We rejected a receipt status on the returned
  `Session`. `Session` is the persisted run record (`hostRecord`), and the ADR's Consequences name
  two errors. We rejected a receipt check after the start, on the scheduler's tick. A run on the
  wrong model would then work, and push, until the tick stopped it, and D1 says that such a run
  never starts. `ErrModelUnverified` is removed. Today it names both facts, and that is the
  conflation that D1 removes.
- **A mismatch settles the run `refused` and uncounted, and raises one `model_mismatch` alert for
  each project and stage.** Each later mismatch counts up the open alert. We rejected an automatic
  pause of the harness. The cause is a config edit or a release, and a pause would also stop each
  stage on that harness whose model verifies. The coordinator already holds `loop stage pause`.
- **A missing receipt raises one `model_receipt_missing` alert for each project and stage, and the
  run keeps running.** A later start that verifies does not close the alert, and the coordinator
  answers it. We rejected one alert for each run, because one vendor change to its transcript store
  would then fill the inbox with one entry for each start.
- **A Claude model is valid only as `claude-` and then lower-case letters, digits and single
  dashes.** We chose this pattern over a list of the aliases to refuse. Claude Code adds aliases,
  such as `opusplan`, `default` and `opus[1m]`, and a list would miss the next one. The rule applies
  to a stage whose harness is `claude`, or names no harness. A Codex model is not checked, because
  the ADR names no Codex alias (Assumptions).
- **The default Claude model becomes `claude-opus-5-5`.** We chose the id that this host's
  transcripts record for the alias `opus` today (ADR 0006, gap 1), over a newer or older model. So
  the default keeps the model that every default run uses now. The coordinator and rooms read
  `DefaultModels` too, so they take the full id. Their sessions get no receipt check.
- **The full ids land before the check.** Work entry 1 lands before entry 3. Entry 3 alone would
  refuse every default Claude stage run, because each one requests `opus`. That includes the runs
  that build this loop.
- **A run that settles while Start waits for the receipt keeps its session record.** The receipt
  wait can last the whole 3-minute window while the session runs, and a run can finish inside it.
  `setRun` updates only a run that has not ended (`internal/sched/runs.go:120`). So today the
  record would lose the session, and no tick would close it. We chose to record the session on the
  ended run too, over a refusal of `loop run finish` during the start. A refused finish turns a
  correct run into a crashed run.

### The three decisions that build nothing

- **D3, D4 and D6 get no work entry and no guard test.** Each one decides that the loop does not
  build something, and AC-1 to AC-4 already hold on the base. We rejected a guard test of each,
  because the proof rule needs a test that fails before the change, and no change exists.

## Assumptions

- Claude Code records the exact id passed in `--model` when that id is a full id, such as
  `claude-opus-5-5`. This is wrong if the first stage run after work entry 3 lands raises a
  `model_mismatch` alert.
- The first assistant record reaches the transcript well inside the 3-minute receipt window
  (ADR 0006, "What I assumed"). This is wrong if `model_receipt_missing` alerts open for Claude
  stages that do run.
- Codex records the id that the loop requests, `gpt-5-codex` (ADR 0006, "What I assumed"). This is
  wrong if a Codex stage run raises a `model_mismatch` alert.
- No registered project's config names a Claude Code alias as a stage model. Measured: no
  `.loop/config.yaml` under `/home/operator/projects` names `model:`. A run cannot list the
  registered projects, because `loop project list` is a host verb. This is wrong if a
  `project_config_invalid` alert names `stages.<stage>.model` after work entry 1 lands.
- One receipt wait delays only the next start of the same stage in the same project. Measured: the
  scheduler starts each dispatch in its own goroutine, one for each project and stage
  (`internal/sched/scheduler.go`, `launch`). This is wrong if a start of another stage waits on it.

## Approach

### The stage start guard

**The full ids come first.** `DefaultModels[agenthost.Claude]` becomes `claude-opus-5-5`, and its
comment stops naming the alias, so AC-8 holds. `ProjectConfig.validate` refuses a stage whose
harness is `claude`, or empty, and whose model is set and fails `ClaudeModelID`. The message names
the field `stages.<stage>.model`.

**The host splits the receipt into two outcomes.** `orca.Host.awaitReceipt` polls
`ModelReceipt` as today. When the harness records another model, Start closes the session and
returns `ErrModelMismatch`. When the window passes and no transcript names a model, Start returns
the live session and `ErrNoModelReceipt`, and closes nothing. An error from the transcript read
counts as no model read. The fake host gains a second driver switch, beside the dropped model: a
silent model, under which its receipt names no model. The contract suite proves both outcomes on
every host whose driver offers each switch.

**Each stage start asks for the check.** `Scheduler.start` sets `RequireVerifiedModel` on the
`StartSpec` it sends. On `ErrModelMismatch`, it calls `fail` with `refused` set and the alert kind
`model_mismatch`. `abandon` settles the run not-ready, so the failure count does not change. On
`ErrNoModelReceipt`, it raises `model_receipt_missing` with the dedupe key
`model_receipt_missing:<project>/<stage>`, and continues as on success. After either success, it
writes the host record even when the run has ended, then sets the state `running` only when the
run has not ended. The coordinator and room starts do not change.

## Domain model

```
internal/agenthost (changed)

ErrModelMismatch      the harness recorded another model than the start requested
  invariant: Start returns it only after it tried to close the session.
ErrNoModelReceipt     no transcript named a model inside the receipt window
  invariant: Start returns it with a session that it did not close.
ErrModelUnverified    removed
```

## Interfaces

```go
// internal/agenthost

var (
	// ErrModelMismatch: the harness records another model than the start
	// requested. The host tried to close the session, and it did not start.
	ErrModelMismatch = errors.New("the harness records another model")
	// ErrNoModelReceipt: no transcript named a model inside the receipt
	// window. The session runs, and Start returns it.
	ErrNoModelReceipt = errors.New("no transcript named a model")
)

type StartSpec struct {
	// ...
	// RequireVerifiedModel makes Start read the model receipt before it
	// returns. A recorded mismatch returns ErrModelMismatch. No model in the
	// window returns the running session and ErrNoModelReceipt.
	RequireVerifiedModel bool `json:"require_verified_model,omitempty"`
}
```

```go
// internal/agenthost/agenthosttest

// ModelSilencer is a driver whose harness can record no model at all.
type ModelSilencer interface {
	SetSilentModel(silent bool)
}
```

```go
// internal/config

// ClaudeModelID reports whether m is a full Claude model id: "claude-", then
// lower-case letters and digits in groups that single dashes join.
func ClaudeModelID(m string) bool
```

```go
// internal/sched

const (
	AlertModelMismatch = "model_mismatch"        // dedupe key model_mismatch:<project>/<stage>
	AlertModelMissing  = "model_receipt_missing" // dedupe key model_receipt_missing:<project>/<stage>
)
```

## Data

No new table, and no migration.

**Project config:** no change to the schema. A stage model that fails `ClaudeModelID` now fails
validation, and the server keeps the project's last good config, as for any invalid config.

## Flows

### A stage start reads the model receipt

```mermaid
sequenceDiagram
    participant D as sched start
    participant H as agent host
    participant T as harness transcript
    participant S as runs table and alerts
    D->>H: Start(spec, RequireVerifiedModel)
    H->>H: await ready (60 s)
    loop every poll, up to 3 min
        H->>T: read the recorded model
    end
    alt the recorded model equals the request
        H-->>D: session, nil
        D->>S: record the session, set running
    else another model is recorded
        H->>H: close the session
        H-->>D: ErrModelMismatch
        D->>S: settle refused, uncounted; release the claim
        D->>S: raise model_mismatch per project and stage
    else no model inside the window
        H-->>D: session, ErrNoModelReceipt
        D->>S: raise model_receipt_missing per project and stage
        D->>S: record the session even if the run ended; set running only if not
    end
```

## Work

1. **Full Claude model ids.** Name the default Claude model by its full id, and refuse a Claude
   Code alias in stage config.
   - Files: `internal/sched/quota.go`, `internal/config/project.go`, and their tests.
   - Needs: nothing.
   - Proves: C7, C8.
   - Done when: a project config whose stage model is `opus` fails to parse with an error that
     names `stages.<stage>.model`, and `grep -n '"opus"' internal/sched/quota.go` prints nothing.
2. **Two receipt outcomes in the agent host.** Split `ErrModelUnverified` into `ErrModelMismatch`
   and `ErrNoModelReceipt`, and make the orca and fake hosts return each one. Add the silent-model
   switch to the fake, and one contract scenario for each outcome.
   - Files: `internal/agenthost/host.go`, `internal/agenthost/orca/start.go`,
     `internal/agenthost/fake/fake.go`, `internal/agenthost/agenthosttest/contract.go`, and their
     tests.
   - Needs: nothing.
   - Proves: C1, C2, C3.
   - Done when: `go test ./internal/agenthost/...` runs the mismatch, missing-receipt and verified
     scenarios against the fake host, and each passes.
3. **Every stage start requests the check.** Set `RequireVerifiedModel` at the stage start, settle
   a mismatch refused, raise the two alerts, and keep the session record of a run that ended during
   the wait.
   - Files: `internal/sched/dispatch.go`, `internal/sched/scheduler.go`, `internal/sched/runs.go`,
     and their tests.
   - Needs: entries 1 and 2. Entry 1 must land first, or every default Claude stage run is refused.
   - Proves: C4, C5, C6.
   - Done when: a scheduler test with a fake host that drops the model sees the run settle
     `refused`, the item's failure count unchanged, and one `model_mismatch` alert after two starts.

## Out of scope

- The model check of the coordinator and of rooms. ADR 0006 decides stage runs only, so it
  belongs to no epic until a decision record asks for it.
- A job that outlives its run (D3). ADR 0006 postpones it, and the tickertown cutover forces it
  (plan, Scope Boundaries, "Deferred for later").
- A free-text alert verb for runs (D4). It belongs to no epic. The ADR rejects it.
- A harness pre-tool hook (D6). It belongs to no epic. A project keeps its own hook in its own
  settings.
- The twice-failed item recovery (D5, AC-10, K-3). It belongs to epic factorio-1okx (Twice-failed
  item recovery), by the operator's split of 2026-10-27 (Open questions).
- Push access from a run's checkout. ADR 0006 postpones it. A test of that push forces the
  decision.

## Contracts

**C1. Refusal: a recorded mismatch refuses the start.** Proves AC-7, D1. A Start with
`RequireVerifiedModel`, on a harness that records another model than the request, returns an
error that `errors.Is` matches to `ErrModelMismatch`. A returned identity then never reads `Live`.
*A contract scenario with the dropped-model switch, run against the fake host.*

**C2. Behaviour: a missing receipt starts the session.** Proves AC-7, D1, K-2. A Start with
`RequireVerifiedModel`, on a harness that records no model, returns after the receipt window. It
returns an error that `errors.Is` matches to `ErrNoModelReceipt`, and a session that reads `Live`.
*A contract scenario with the silent-model switch and a short receipt window.*

**C3. Behaviour: a matching receipt starts the session with no error.** Proves D1. A Start with
`RequireVerifiedModel`, whose harness records the requested model, returns a nil error and a live
session. Exact equality decides: a request `opus` against a record `claude-opus-5-5` is a mismatch
(K-1).
*A contract scenario, and an orca unit test of `awaitReceipt` with a transcript that records
`claude-opus-5-5` for a request `opus`.*

**C4. Behaviour: every stage start requests the check.** Proves AC-6, D1. Each `StartSpec` that
`Scheduler.start` sends for a stage run has `RequireVerifiedModel` set, whatever the stage.
*A scheduler test that dispatches each stage on the fake host, and reads each recorded launch.*

**C5. Refusal: a mismatch start counts nothing, and raises one alert for each stage.** Proves D1.
After a mismatch, the run's `outcome_class` is `refused`, the item's counted failures at its inputs
are unchanged, the item is back in the state it was claimed from, and its lease is free. Two
mismatched starts of one stage leave one open `model_mismatch` alert with count 2. A mismatch at
another stage opens a second alert.
*A scheduler test on the fake host with the dropped-model switch.*

**C6. Behaviour: a missing receipt runs the session, and keeps its record.** Proves D1, K-2. After
`ErrNoModelReceipt`, the run is `running`, and one `model_receipt_missing` alert is open for its
project and stage. A run that finished during the wait keeps its settlement and its session record,
so the next tick closes the session.
*A scheduler test whose fake host returns `ErrNoModelReceipt`, and one in which the run finishes
before Start returns.*

**C7. Refusal: stage config refuses a Claude Code alias.** Proves AC-9, D2, K-1. `ParseProjectConfig`
refuses a stage with harness `claude`, or no harness, whose model is `opus`, `sonnet`, `haiku`,
`default`, `opusplan` or `opus[1m]`. The error names `stages.<stage>.model`. It accepts
`claude-opus-5-5` and `claude-haiku-4-5-20251001`, and a stage with harness `codex` and model
`gpt-5-codex`.
*A table test, one row for each value.*

**C8. Invariant: each default model is a full id.** Proves AC-8, D2. `DefaultModels[agenthost.Claude]`
passes `ClaudeModelID`, and `internal/sched/quota.go` holds no `"opus"`.
*A unit test, and AC-8 as a command.*

## Observable behaviour

None, because this epic draws no screen. Its visible results are inbox entries: the
`model_mismatch` and `model_receipt_missing` alerts. The contracts above prove each one.

## Open questions

None. The one question asked whether factorio-jiv is one epic or two. The operator answered on
2026-10-27: split. This spec holds the stage start guard only (D1, D2, work entries 1 to 3). The
twice-failed item recovery (D5) belongs to epic factorio-1okx.
