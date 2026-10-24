# Code-quality ratchets and audits: technical spec

## Source

- **Epic:** factorio-2u6
- **Source:** `docs/adr/0003-quality-ratchets-and-audits.md`. Its `**Ticket:**` line names this
  epic.
- **Stories covered:** none. This is an architecture epic.

The ADR's acceptance criteria carry no labels, so this spec numbers the ones the epic owns. Each
contract below cites one of them, or the ADR paragraph it proves.

| Label | ADR acceptance criterion |
| --- | --- |
| A1 | `go test ./internal/gates/...` proves that a gate with a `baseline` fails on a count above the baseline and on a count below it |
| A2 | A test proves that `--update` lowers a count and never raises one |
| A3 | A test proves that an audit baseline entry with an empty reason fails the gate |
| A4 | `grep -c 'loop gates --only audit --full' .github/workflows/audit.yml` prints `1` |
| A5 | A test proves that a first-parent commit with no `merged_sha` raises one audit alert, and that the next tick raises no second alert |
| A6 | A test proves that a runbook naming a `make` target that no `Makefile` holds fails the `runbooks` gate |

The ADR's own document checks (the gates, the Ticket line, the number, nine verdict rows, no
`worktree.size` in `internal/gates`, no `lessons` noun) passed when the ADR merged. This spec owns
none of them. Verdicts G6, G8 and G9 port nothing, so no work entry below builds them.

Round 1 read the loop at the base `7a6bdad`. Round 2 merged the base `58a9d93`. Between the two,
no file that this spec cites changed, except `internal/machine/history.go`, which gained
`Transition.Fact`.

## Scope check

**Components:**

1. **The ratchet gates.** `internal/gates` (the shared ratchet, the stack gates, the `runbooks`
   gate), the `baseline` key of `internal/config`, the flags of `loop gates`, the scheduled audit
   workflow, and the pack lines that tell an agent how to use them.
2. **The base audit.** One step of the reactor tick in `internal/github`, and the merge record it
   reads from `internal/machine`. Why not its own epic: it could be one. It shares no type and no
   file with component 1. ADR 0003 decided it with the other gaps, under this one epic. Open
   question Q1 proposes the split.

Work entries 1 to 6 build component 1. Each one changes `internal/gates`, the config key or the
verb that drives them, and the pack lines that name them. Entries 7 and 8 build component 2. No
entry of one component needs an entry of the other.

**Not in this spec:** the overwatch `audit` part, which reads the newest scheduled audit run, and
the config key `health.audit_workflow` that names the workflow. Both belong to factorio-a7r, work
entry 9 of `docs/plans/factorio-a7r-overwatch-sweep-spec.md`.

## Decisions

ADR 0003 decided each verdict. Each line below is a choice the ADR left to this spec, or a choice
that the code forces.

- **A ratcheted gate tells a silent tool from a clean one by an end line, and never by the exit
  code.** The command must print the line `loop:end` as the last non-blank line of its stdout. A
  stdout with no end line fails the gate with `no_end_line`, whatever the exit code. We rejected a
  rule that the command exits 0, because oxlint exits 1 and govulncheck exits 3 when they find
  something. Such a command would need `|| true`, and that hides a crash too. We rejected a rule
  that empty stdout fails, because a clean project prints no key. The ADR asks that the ratchet
  never read silence as zero ("What this makes hard"). This rule meets it with no parser for any
  tool.
- **Every ratcheted `jq` line takes one form: `jq -rn '[inputs] | select(length > 0) | (.[] | <keys>), "loop:end"'`.**
  The form reads the tool's stdout as a stream of JSON documents. It serves a tool that prints
  one document, such as oxlint, golangci-lint, ruff or `pnpm audit --json`, as a stream of one.
  It serves a tool that prints a stream, such as `govulncheck -json`, as it is. So no project
  chooses a form by tool. With jq-1.7 on the loop host, the form prints nothing on empty input,
  nothing on input that does not parse, and one end line after the keys of a two-document stream.
  We rejected the per-document form `jq -r '(<keys>), "loop:end"'`, because it prints one end line
  for each document, so a stream fails with a key after the end line. We rejected `jq -rs` and
  `jq -rn '[inputs] | ...'` with no `select(length > 0)`, because each prints the end line on
  empty input, so a crashed tool reads as clean. The key filter never holds `?` or `// empty` on
  the path to the findings. A tool whose output changed shape then makes `jq` fail before the end
  line, and the gate fails. With `?`, a renamed field would read as zero findings. C4 and C30 run
  the form itself, not only a stand-in command.
- **A gate's stdout carries keys only, and its stderr still streams to the log.** We chose this
  over parsing mixed output, because a key line and a progress line cannot be told apart. A gate
  with no `baseline` keeps today's behaviour: its stdout and stderr both stream, and only the exit
  code counts.
- **A lint baseline uses the shape of `.loop/suppressions.json`: `{"allowed": {"<key>": <count>}}`.**
  We chose one shape for the suppressions and every lint baseline over a new shape, because one
  reader, one writer and one `ratchet` then serve both.
- **The audit baseline has its own shape: `{"accepted": {"<id>": {"reason": "...", "date": "YYYY-MM-DD"}}}`.
  The audit gate counts each advisory id once.** We chose a set of ids over counts, because an
  audit tool prints one advisory once for each path to the package. That count changes with the
  dependency graph, not with the risk. We rejected a reason on every lint key, because 65 lint
  findings in 36 keys would need 36 reasons, and tickertown's lint baseline holds none. The
  `audit` gate reads this shape. Every other ratcheted gate reads the lint shape.
- **The severity floor stays in the command.** The ADR postponed it to this spec. A project that
  wants a floor selects by severity in its own `jq` line, such as `select(.severity != "low")`. We
  rejected a `floor` config key, because the command already decides which findings it prints,
  and each audit tool names its severities differently.
- **`--update` with no baseline file writes the found set for a lint gate or for `standards`, and
  writes nothing for `audit`.** An accepted advisory needs a reason that a person writes. A
  written entry with an empty reason would fail the gate at once. We rejected a placeholder
  reason, because a placeholder passes the gate and states nothing.
- **`--update` on a gate with a count above its baseline still lowers every other key, keeps the
  old count of that key, and fails.** We chose a partial write over writing nothing, because an
  agent that fixed ten keys and broke one keeps the ten. The raised key stays a finding, so the run
  still fails.
- **`--full` needs `--only audit`.** We chose one gate over `--full` on the whole list, because a
  scheduled run of every gate repeats CI on a commit that CI already passed.
- **A config `baseline` is refused on `format`, `typecheck`, `test`, `build` and every policy
  hook. Two gates may not name one path, and no gate may name `.loop/suppressions.json`.** The
  ADR named the four stack gates. `Command` also types `policy_hooks`, so the same field there
  needs the same refusal. Two writers of one file would lower each other's keys.
- **The `runbooks` gate reads one Makefile and one `package.json`, both at the repository root.**
  A call with `make -C`, `make -f`, `pnpm -C`, `pnpm --filter`, `pnpm -r` or `--dir` names another
  file, so the gate does not check it. We rejected a search for the nearest Makefile, because a
  runbook gives no working directory for its commands.
- **The `runbooks` gate reads a `make` or package command only at command position.** That is the
  start of a fenced line or code span, after an optional `$ `, or after `&&`, `||`, `;`, `|` or
  `(`. We chose this over every occurrence of the word, because `.loop/policy/coordinator.md:23`
  quotes the operator inside a code span: "`... by yourself. make sure this is part ...`". Read
  as a command, that line names a target `sure`.
- **The `runbooks` gate expands a Makefile variable in a target name when the Makefile assigns it
  once, with `=`, `:=` or `::=`, to plain words.** `$(PLATFORMS)` in this repository's `Makefile`
  expands to its three platforms. A target with a `%` matches by the stem rule. A target that still
  holds a `$` after expansion, or an `include` line, makes the Makefile open: a call that matches
  no target then gives no finding. We chose an open Makefile over a false finding, because a false
  finding blocks every run whose runbook names the target.
- **A bare `pnpm <x>` or `yarn <x>` is a script call only when `<x>` is no built-in command of that
  tool.** The gate holds a fixed list of each tool's built-ins, such as `install`, `add`, `exec`
  and `dlx`. `npm` and `bun` call a script only through `run`. We rejected a check of `npm <x>`,
  because `npm test` and `npm start` are built-ins that also run scripts.
- **The base audit reads its merge records from the `transitions` table.** We chose the history
  over the current facts of an item, because an epic's `reviewed_head` holds the spec's head once
  the spec passes review. The PRD's reviewed head survives only in history. We rejected a new table
  of merges, because each merge already writes one transition row in the transaction that applies
  it (`internal/machine/history.go`).
- **A spec merge records its merge commit in a new fact, `spec_merged`.** Today `doc_merged` from
  `spec_signoff` sets no fact (`internal/machine/epic.go:312`), so no record names a spec's merge
  commit. A PRD merge sets `prd_merged`, and a ticket merge sets `merged_sha`. We rejected reusing
  `merged_sha` for the spec, because `merged_sha` means a ticket's merge in `loop work show` and
  in the merge pipeline.
- **A document merge that outran its review fails the audit, whatever its parents.** The record
  says so in `MergeRecord.Outran`: the item's history holds a `doc_merged` row from that
  document's review or amending state (`prd_review` or `prd_amending` for a PRD, `spec_review` or
  `spec_amending` for a spec) before the merge row. We chose history over the reviewed head,
  because an epic holds one `reviewed_head` for both documents, and no epic row clears it
  (`internal/machine/epic.go:287`, `:310`, `:333` are its only writes). So a spec that a person
  squash-merges during its review still finds the PRD's head there. A rule that read only the head
  would pass that merge. We rejected a check that the reviewed head was set in the same document
  round, because that needs the round boundaries replayed, and the outran row already marks the
  one path that skips the review.
- **Any other recorded merge passes at its reviewed head. A merge commit passes when its second
  parent is that head. A commit with one parent passes when that head is not empty.** The
  `direct` mechanism merges with a merge commit at the reviewed head
  (`internal/github/merge_api.go:90`), so its second parent is exact. Mergify and a GitHub queue
  may squash, which leaves one parent and no head in git. We rejected a rule that every base
  commit has two parents, because the loop does not read a project's Mergify method, and that rule
  would raise an alert for every squash. A document that did not outrun its review reached
  sign-off through a pass, an unchanged amendment from sign-off, or `signoff_head`. Each of the
  three sets, or keeps, the reviewed head of that same document (`internal/machine/epic.go`). So a
  non-empty head on a non-outran document merge is that document's pinned head. A ticket clears
  its reviewed head on a reject, a head change and a dequeue (`internal/machine/ticket.go:166-174`).
- **The audit runs on every tick. On a tick whose `pollPulls` returned an error, a commit with no
  merge record stops the walk, and the cursor stays before it.** A merge that `pollPulls` failed
  to read has no record yet, and the audit would report it. A commit with a record is judged on
  that tick, because a record never changes after its merge. We chose this over skipping the
  whole step on such a tick, because `pollPulls` fails when any one pull request fails
  (`internal/github/reactor.go:281-291`). A merge refusal other than not-found fails on each tick
  (`:500-515`), so one stuck pull request would stop the audit for good, and raise nothing.
  When `pollPulls` returned no error, a commit with no record fails at once, because `SyncBase`
  fetches the base before `pollPulls` lists pull requests. So every commit that the fetch brought
  was merged before the list was read.
- **A commit that stops the walk for 15 minutes raises one `base_audit_stalled` alert.** The
  first tick that stops at a commit writes the commit to the cursor stream `base_audit_stall`.
  The row's `updated_at` keeps the time of that first stop. A tick that stops at the same commit
  15 minutes or more after that time raises the alert. Its subject is `<project>@<sha>`, and its
  body names the commit, the time stopped and the first line of the `pollPulls` error. The walk
  deletes the row when it passes the commit. We chose a duration over a count of ticks, because
  the host's schedule sets the tick rate (`internal/config/throughput.go:99`, every minute by
  default), and a slower schedule would delay a count. We rejected an alert at the first stop,
  because one failed GitHub read would raise it. ADR 0003 promises that a merge outside the loop
  reaches the inbox on the next tick. A stuck pull request delays that, and this alert reports
  the delay.
- **A base that no longer holds the cursor raises one `base_rewritten` alert, and the cursor moves
  to the new head.** The ADR does not cover a forced push. We rejected an audit of the new range
  from the merge base, because a rewritten base breaks every record before it, and one alert
  names the event that a person must read.
- **An unreviewed document merge raises an alert.** The outran rows let a person merge a PRD or a
  spec during its review, and that merge counts as sign-off. The ADR passes "a person's merge of a
  reviewed PRD or spec". An outran merge was not reviewed, so its commit raises
  `unreviewed_merge`, by the outran rule above. We rejected a pass for every document merge,
  because the alert is the only trace that a document skipped its review.

## Assumptions

- Each lint or audit tool that a project ratchets prints JSON that `jq` reads. This holds for
  oxlint, golangci-lint, ruff, govulncheck and `pnpm audit`. This is wrong if a project names a
  tool with no machine output.
- With `-n`, `[inputs] | select(length > 0)` makes `jq` print nothing when its input is empty or
  does not parse. jq-1.7 on the loop host does. Without `select(length > 0)`, `-n` and `-s` both
  run the filter once on empty input, and print the end line. This is wrong if a `jq` release
  prints the end line for the form on empty input. C4 and C30 run the form on the `jq` of CI, so
  that release fails a test.
- A ratcheted tool prints at least one JSON document on a clean run. oxlint, golangci-lint, ruff
  (`[]`), govulncheck and `pnpm audit --json` do. This is wrong if a clean run of a project's tool
  fails with `no_end_line`. That failure is loud, and it never reads as clean.
- The CI runner holds `jq` on its `PATH`, as the Ubuntu runner images do. This is wrong if C4 or
  C30 fails with `jq` not found. The tests fail then, and never skip.
- GitHub lists a merged pull request as merged as soon as the base fetch sees its commit. This is
  wrong if an `unreviewed_merge` alert names a commit that an item records as its merge one tick
  later.
- No merge mechanism rebases a pull request onto the base. A rebase merge puts each commit of the
  pull request on the first-parent line, and only the last one is recorded. This is wrong if one
  merge raises more than one `unreviewed_merge` alert.
- The coordinator reads each open alert by its existing rules, and no runbook line is needed for a
  new kind. This is wrong if the coordinator runbook routes alerts by kind.
- One scan of a project's `transitions` rows for each audited commit costs well under a second.
  This is wrong if a project holds more than about a million transition rows.
- The base audit sees few commits on each tick. A loop merges one pull request at a time. This is
  wrong if a tick after a long outage walks so many commits that the tick overruns its interval.

## Approach

**The shared ratchet.** `ratchet` and the suppressions reader move from
`internal/gates/standards.go` into `internal/gates/baseline.go`. The file holds the type of a
count baseline, its reader, its writer, `ratchet` and `lower`. `ratchet` takes the rule name for a
count above the baseline, so the standards gate keeps `new_suppression` and a stack gate reports
`new_finding`. Both report `stale_baseline` for a count below. The writer prints sorted keys,
two-space indent and a final newline, so two writes of one set give the same bytes.

**The config key.** `Command` gains `Baseline`. Validation refuses it on the four stack gates, on
every policy hook, on a path that is not a clean repository path, on a path that two gates share,
and on `.loop/suppressions.json`.

**A ratcheted stack gate.** When the gate's command has a `baseline`, `stackGate` captures stdout
and streams stderr to `r.Out`. It reads keys from stdout (`parseKeys`): one key for each non-blank
line before the end line. A key is the trimmed line, at most 512 bytes, with no control character.
It then reads the baseline file and calls `ratchet`. A missing baseline file reads as an empty
baseline, and the finding names `loop gates --only <gate> --update` as the way to adopt.

**The audit gate.** The lock-file scope runs first, as today, unless `r.Full` is set. With a
`baseline`, the gate dedupes the keys, reads the accepted set, checks each entry's reason and
date, and calls `ratchet` with a count of one for each id. The rules are `new_advisory` and
`stale_advisory`, so a reader tells an advisory from a lint key.

**`--update`.** `Repo` gains `Update` and `Full`. With `Update`, a ratcheted gate computes the
found counts, writes `lower(found, baseline)`, and then ratchets against what it wrote. The only
findings left are counts above the baseline and keys outside it. The standards gate does the same
for `.loop/suppressions.json`. The audit gate deletes stale accepted ids and keeps each other
entry as it is. `cmd/loop/gates.go` refuses the flag combinations of C13 before any gate runs.

**The scheduled audit.** `.github/workflows/audit.yml` runs on a daily schedule and on
`workflow_dispatch`. It builds `loop` with the local action and runs
`loop gates --only audit --full`. The overwatch part of factorio-a7r reads its newest run.

**The `runbooks` gate.** A new file, `internal/gates/runbooks_project.go`, reads the root
`Makefile` and the root `package.json` once for each gate run. It walks each code line with
`forEachCode`, after `shellOnly`, and finds each command at command position. It checks the name
of the target or script only, never a flag or an argument after it. A name that holds `<`, `>`,
`$`, `{` or `}` is a placeholder and is not checked.

**The merge record.** `internal/machine` sets `spec_merged` on the `doc_merged` row from
`spec_signoff`. `Machine.MergeOf` finds the transition row of one project whose `set` holds the
commit as `merged_sha`, `prd_merged` or `spec_merged`. It then replays that item's history up to
the row, and returns the reviewed head that the item held at that moment. For a PRD or a spec, it
sets `Outran` when a `doc_merged` row from that document's review or amending state comes before
the merge row.

**The base audit.** `Reactor.auditBase` runs in `tickProject` after `pollPulls` on every tick,
and it receives the error that `pollPulls` returned. It reads the cursor from `github_cursors`
under the stream `base_audit`. With no cursor, it writes the base head and audits nothing.
Otherwise it checks that the base head reaches the cursor (`Mirror.Reaches`). It lists the
first-parent commits after the cursor, oldest first, and judges each one by C22. It raises one
alert for each commit that fails, and moves the cursor past each commit once that commit's alert
is written. A failed write stops the walk, and the next tick starts at the first commit it did not
finish. When `pollPulls` failed, a commit with no merge record also stops the walk, and C31 times
that stop. The step reads the mirror and the store, and it makes no GitHub request.

## Domain model

```
internal/gates (changed)

Counts              map from a finding key to its count
  invariant: every count is at least 1. A key with a count of 0 is absent, and the writer drops it.

Advisory            one accepted advisory: Reason, Date
  invariant: a valid entry has a non-blank Reason and a Date that is a calendar day, YYYY-MM-DD.
             The gate reports each invalid entry. It never drops one.

The end line        the literal "loop:end"
  invariant: a ratcheted gate reads keys only from a stdout whose last non-blank line it is.

internal/machine (changed)

MergeRecord         Item, Kind, Verb, Fact, ReviewedHead, Outran, Seq
  invariant: Fact is one of merged_sha, prd_merged, spec_merged, and the row named by Seq set that
             fact to the commit. ReviewedHead is "" when the item held no reviewed head then.
             Outran is false for merged_sha. For prd_merged it is true when a doc_merged row from
             prd_review or prd_amending precedes Seq; for spec_merged, one from spec_review or
             spec_amending.
```

## Interfaces

```go
// internal/config/project.go
type Command struct {
	Name     string `yaml:"name"`
	Run      string `yaml:"run"`
	Baseline string `yaml:"baseline,omitempty"` // repository path of the gate's ratchet file; gates only
}

// internal/gates/gates.go
type Repo struct {
	// ... existing fields ...
	Update bool // lower each baseline to what the gate found (--update)
	Full   bool // skip the lock-file scope of the audit gate (--full)
}

// internal/gates/baseline.go
const EndLine = "loop:end"

type Counts map[string]int

// parseKeys reads the keys of a ratcheted command's stdout. ok is false when
// the end line is not the last non-blank line.
func parseKeys(stdout string) (keys []string, ok bool, bad []Finding)

func readCounts(root, rel string) (c Counts, exists bool, err error)
func writeCounts(root, rel string, c Counts) error

// ratchet holds found to baseline in both directions. over names the rule of a
// count above the baseline; a count below is stale_baseline.
func ratchet(found, baseline Counts, file, over, noun string) []Finding

// lower returns baseline with each count cut to the found count and each key
// that found lacks deleted. It never raises a count and never adds a key.
func lower(found, baseline Counts) Counts

type Advisory struct {
	Reason string `json:"reason"`
	Date   string `json:"date"`
}

func readAccepted(root, rel string) (a map[string]Advisory, exists bool, err error)
func writeAccepted(root, rel string, a map[string]Advisory) error

// internal/cli/surface.go, the gates verb gains two flags
Flag{Name: "update", Type: FlagBool, Description: "lower the baseline of the one gate --only names to what it found; never raise a count"}
Flag{Name: "full", Type: FlagBool, Description: "with --only audit, audit every dependency, not only a changed lock file"}

// cmd/loop/gates.go refusals, all CodeUsage
"update_needs_only"  // --update with no --only
"no_baseline"        // --update on a gate that is not standards and has no baseline
"full_needs_audit"   // --full with any selection but --only audit

// internal/machine/rows.go
const FactSpecMerged = "spec_merged" // the merge commit of the epic's spec

// internal/machine/history.go
type MergeRecord struct {
	Item         string
	Kind         Kind
	Verb         string // merged or doc_merged
	Fact         string // merged_sha, prd_merged or spec_merged
	ReviewedHead string // the item's reviewed_head at that transition, or ""
	Outran       bool   // the document merged during its review or amendment
	Seq          int64
}

func (m *Machine) MergeOf(ctx context.Context, project, sha string) (MergeRecord, bool, error)

// internal/github/git.go
func (m *Mirror) FirstParents(ctx context.Context, from, to string) ([]string, error) // after from, through to, oldest first
func (m *Mirror) Commit(ctx context.Context, sha string) (Commit, error)

type Commit struct {
	SHA     string
	Parents []string
	Subject string
	Author  string
}

// internal/github/reactor.go
const (
	StreamBaseAudit       = "base_audit"
	StreamBaseAuditStall  = "base_audit_stall"   // cursor: the commit that stops the walk
	AlertUnreviewedMerge  = "unreviewed_merge"   // subject "<project>@<sha>"
	AlertBaseRewritten    = "base_rewritten"     // subject "<project>@<old cursor>"
	AlertBaseAuditStalled = "base_audit_stalled" // subject "<project>@<sha>"
)

const baseAuditStallAfter = 15 * time.Minute

// polled is the error pollPulls returned on this tick, or nil.
func (r *Reactor) auditBase(ctx context.Context, p config.Project, sha string, polled error) error
```

## Data

```
.loop/config.yaml (changed)
  gates[n]
    + baseline    a repository path, optional. Refused on format, typecheck, test, build and on
                  every policy hook.

<lint baseline>.json (new, a path the config names)
  {"allowed": {"<key>": <count>}}     count is an integer of at least 1

<audit baseline>.json (new, a path the config names)
  {"accepted": {"<advisory id>": {"reason": "<text>", "date": "YYYY-MM-DD"}}}

github_cursors (no schema change)
  + rows with stream = "base_audit"; cursor holds the full sha of the last audited commit
  + rows with stream = "base_audit_stall"; cursor holds the full sha of the commit that stops the
    walk, and updated_at the time of the first stop there. Absent when the walk is not stopped.

bead metadata (no schema change)
  + loop.spec_merged on an epic, set by doc_merged from spec_signoff

alerts (no schema change)
  + kinds unreviewed_merge, base_rewritten and base_audit_stalled
```

**Existing records:** no project names a `baseline` today, and this repository holds no
`.loop/suppressions.json`. No epic holds `spec_merged`. That costs nothing, because the base audit
starts at the base head and never reads an earlier spec merge. No row holds the stream
`base_audit` until the first tick after work entry 8 lands.

## Flows

A ratcheted gate run, with and without `--update`:

```mermaid
sequenceDiagram
    participant V as loop gates
    participant G as stackGate
    participant C as project command
    participant F as baseline file
    V->>V: refuse --update with no --only, --full with no --only audit
    V->>G: Repo{Update, Full}
    G->>G: audit only: no lock file changed and not Full, then pass
    G->>C: sh -c run
    C-->>G: stdout keys, stderr to the log
    G->>G: no end line, then fail no_end_line
    G->>F: read the baseline (absent reads as empty)
    alt Update
        G->>F: write lower(found, baseline)
    end
    G-->>V: ratchet(found, baseline): new_finding, stale_baseline, new_advisory, stale_advisory
```

The base audit on one reactor tick:

```mermaid
sequenceDiagram
    participant T as tickProject
    participant M as Mirror
    participant S as Store
    participant H as Machine.MergeOf
    T->>M: SyncBase, head sha
    T->>T: pollPulls, keep its error as polled
    T->>S: read cursor base_audit
    alt no cursor
        T->>S: write cursor = head, raise nothing
    else head does not reach cursor
        T->>S: raise base_rewritten, write cursor = head
    else
        T->>M: FirstParents(cursor, head)
        loop each commit, oldest first
            T->>M: Commit(sha): parents
            T->>H: MergeOf(project, sha)
            alt no record and polled is an error
                T->>S: write stall row = sha, unless it holds sha
                opt stall row 15 minutes old or more
                    T->>S: raise base_audit_stalled
                end
                T->>T: stop the walk, keep the cursor
            else no record, outran, or reviewed head fails C22
                T->>S: raise unreviewed_merge
            end
            T->>S: write cursor = sha, delete the stall row; on a failed write, stop the walk
        end
    end
```

## Work

1. **The shared ratchet and the `baseline` key.** Move `ratchet` and the suppressions reader into
   `internal/gates/baseline.go`, and add `Counts`, `readCounts`, `writeCounts`, `lower` and
   `parseKeys`. Add `Command.Baseline` and its validation.
   - Files: `internal/gates/baseline.go`, `internal/gates/baseline_test.go`,
     `internal/gates/standards.go`, `internal/config/project.go`, `internal/config/*_test.go`
   - Needs: nothing
   - Proves: C1, C2
   - Done when: the standards tests pass unchanged, and config load refuses each case of C1.
2. **The ratcheted stack gate.** A gate with a `baseline` captures stdout, reads keys after the end
   line check, and ratchets against its baseline. A gate with none behaves as today.
   - Files: `internal/gates/stack.go`, `internal/gates/stack_test.go`
   - Needs: 1
   - Proves: C3, C4, C5
   - Done when: a fixture command that prints keys fails above and below its baseline, a
     command that prints nothing fails with `no_end_line`, and the `jq` form of the Decisions
     fails on empty input and reads a two-document stream.
3. **The audit baseline.** The `audit` gate with a `baseline` reads the accepted set, checks each
   reason and date, and ratchets advisory ids. The lock-file scope stays in front of it.
   - Files: `internal/gates/stack.go`, `internal/gates/baseline.go`, `internal/gates/stack_test.go`
   - Needs: 2
   - Proves: C6, C7, C8
   - Done when: an entry with an empty reason fails the gate, and a change with no lock file
     passes with no command run.
4. **`--update`.** Add the flag to the surface and `Repo`, the refusals to `cmd/loop/gates.go`,
   and the write to the standards, stack and audit gates. Regenerate `gen/` with `make proto`. In
   the same entry, state in `pack/runbooks/implementer.md` and `pack/runbooks/code-reviewer.md`
   that a new baseline file or a raised count needs a reason in the pull request body. The
   code-reviewer rejects a pull request that has neither.
   - Files: `internal/cli/surface.go`, `gen/**`, `cmd/loop/gates.go`, `cmd/loop/gates_test.go`,
     `internal/gates/gates.go`, `internal/gates/standards.go`, `internal/gates/stack.go`,
     `internal/gates/*_test.go`, `pack/runbooks/implementer.md`, `pack/runbooks/code-reviewer.md`
   - Needs: 3
   - Proves: C9, C10, C11, C12, C13, C28
   - Done when: `loop gates --only lint --update` lowers a fixture baseline and keeps a raised
     key's old count, and `make pack-check` passes.
5. **`--full` and the scheduled audit.** Add the flag, the scope bypass and its refusal. Add
   `.github/workflows/audit.yml` to this repository. Teach `pack/skills/loop-init/SKILL.md` to
   scaffold the workflow beside `ci.yml`, to run one `--update` for a project that adopts the
   loop with lint debt, and to write each ratcheted `jq` line in the one form of the Decisions.
   - Files: `internal/cli/surface.go`, `gen/**`, `cmd/loop/gates.go`, `internal/gates/gates.go`,
     `internal/gates/stack.go`, `internal/gates/*_test.go`, `.github/workflows/audit.yml`,
     `pack/skills/loop-init/SKILL.md`, `internal/pack/loopinit_jq_test.go`
   - Needs: 4, because both change the flags of one verb and `gen/`
   - Proves: C14, C15, C29, C30
   - Done when: `loop gates --only audit --full` runs the audit command in a checkout with no
     changed lock file, and the workflow test and the `jq` line test pass.
6. **Project commands in the `runbooks` gate.** Check each `make` target and each package script
   that a runbook names at command position.
   - Files: `internal/gates/runbooks_project.go`, `internal/gates/runbooks.go`,
     `internal/gates/runbooks_test.go`, `internal/gates/testdata/**`
   - Needs: nothing
   - Proves: C16, C17, C18
   - Done when: a fixture runbook naming `make nosuch` fails, and `loop gates --only runbooks`
     passes on this repository.
7. **The merge record.** Set `spec_merged` on the spec's `doc_merged` row, and add
   `Machine.MergeOf`.
   - Files: `internal/machine/epic.go`, `internal/machine/rows.go`, `internal/machine/history.go`,
     `internal/machine/*_test.go`
   - Needs: nothing
   - Proves: C19, C20
   - Done when: `MergeOf` returns a PRD's reviewed head after the epic's spec passed review, and
     sets `Outran` on a spec squash-merged during its review after the PRD passed.
8. **The base audit.** Add `Mirror.FirstParents`, `Mirror.Commit`, the two cursor streams, the
   three alert kinds and `Reactor.auditBase`, and call it from `tickProject` on every tick, with
   the error of `pollPulls`.
   - Files: `internal/github/git.go`, `internal/github/reactor.go`,
     `internal/github/baseaudit.go`, `internal/github/baseaudit_test.go`
   - Needs: 7
   - Proves: C21, C22, C23, C24, C25, C26, C27, C31
   - Done when: a test repository with one direct push on the base raises one
     `unreviewed_merge` alert, the next tick raises none, and a pull list that fails on every
     tick raises one `base_audit_stalled` alert once 15 minutes pass.

## Out of scope

- The overwatch `audit` part, and `health.audit_workflow` in this repository's
  `.loop/config.yaml`. They belong to factorio-a7r, work entry 9. Config load refuses an unknown
  key, so this epic cannot name the workflow there first.
- A parser for any lint or audit tool. ADR 0003 forbids it. Each project writes its own `jq` line.
- `worktree-size`, `env:check`, the excuse lists, `READINGS` and a ledger writer. ADR 0003 ports
  none of them (G6 to G9). A port needs a new ADR.
- A check of a flag or an argument of a project command. ADR 0003 § Decision, G7.
- Branch protection for this repository. GitHub refuses it on the current plan. That is the
  operator's choice of plan, and the base audit is the detective answer the ADR chose.
- A lint baseline for this repository. Its `lint` command passes at zero findings today, so it
  names no `baseline`.

## Contracts

**C1. Refusal: config load refuses a misplaced or shared baseline.** Proves ADR 0003 §
Consequences, "Project config". Config load fails, and names the field, for a `baseline` on
`format`, `typecheck`, `test` or `build`, on any policy hook, on a path that is absolute, holds
`..` or is not clean, on a path that two gates name, and on `.loop/suppressions.json`. A
`baseline` on `lint`, `audit` or an extra gate loads.
*An example test for each case.*

**C2. Equivalence: the standards gate's findings do not change.** Proves ADR 0003 §
Consequences, "The ratchet". For every fixture in `internal/gates/standards_test.go`, the gate
returns the same rules and messages after the move as before it.
*The existing standards tests pass with no edit.*

**C3. Behaviour: a ratcheted gate holds each key to its baseline in both directions.** Proves A1.
With baseline `{"a.ts::r": 2}`, a command that prints `a.ts::r` three times fails with
`new_finding`, once fails with `stale_baseline`, and twice passes. A key outside the baseline
fails with `new_finding`. The command's exit code changes none of these results.
*Example tests with a fixture command that prints keys and the end line, run with exit 0 and
exit 1.*

**C4. Refusal: a silent command never reads as clean.** Proves ADR 0003 § Consequences, "What
this makes hard". A ratcheted gate fails with `no_end_line` when stdout is empty, when it holds
keys and no end line, or when a key follows the end line. This holds with exit 0 and with any other
exit code. A command that prints only the end line passes on an empty baseline. The `jq` form of
the Decisions, run by the gate, fails with `no_end_line` on empty input and on a truncated
document. It reads `x` and `y` from the two-document stream `{"a":["x"]}` `{"a":["y"]}`, and
passes on a baseline that holds both.
*Example tests: `true`, `printf 'a::r\n'`, `printf 'loop:end\na::r\n'`, `printf 'loop:end\n'`,
and the `jq` form piped from `printf ''`, from a truncated document and from the two-document
stream. The `jq` cases run the `jq` on the test's `PATH`, and fail when it is absent.*

**C5. Behaviour: a gate with no baseline keeps the exit-code rule.** Proves ADR 0003 § Decision,
G1. A gate with no `baseline` passes on exit 0 whatever it prints, fails with `command_failed`
on any other exit, and streams its stdout to the log.
*Example tests with `echo a::r` and `false`.*

**C6. Behaviour: the audit gate ratchets each advisory id once.** Proves ADR 0003 § Decision, G3.
A command that prints `GHSA-1` three times reads as one id. An id outside the accepted set fails
with `new_advisory`. An accepted id that the command no longer prints fails with
`stale_advisory`. Equal sets pass.
*Example tests.*

**C7. Refusal: an accepted advisory needs a reason and a date.** Proves A3. An entry whose reason
is empty or only white space fails with `no_reason`. An entry whose date is not a calendar day in
the form `YYYY-MM-DD`, such as `2026-13-01` or `24 Oct`, fails with `bad_date`. Both findings
name the id.
*An example test for each value.*

**C8. Boundary: a change with no lock file runs no audit, with or without a baseline.** Proves
ADR 0003 § Consequences, "A later reader must not". With a `baseline` and no changed lock file,
the audit gate passes, and its command does not run.
*An example test whose command writes a marker file. The test asserts that the file is absent.*

**C9. Behaviour: `--update` lowers and never raises.** Proves A2. With baseline
`{"a::r": 3, "b::r": 1, "c::r": 1}` and found `a::r` twice and `c::r` twice, `--update` writes
`{"a::r": 2, "c::r": 1}` and fails with `new_finding` on `c::r`. A found key outside the baseline
is not written, and fails. No sequence of runs raises a count or adds a key to an existing file.
*An example test, and a property test over generated found and baseline sets that asserts each
written count is at most both the old count and the found count.*

**C10. Boundary: `--update` adopts once, and repeats to the same bytes.** Proves ADR 0003 §
Decision, G2. With no baseline file, `--update` on a lint gate writes the found counts, with sorted
keys, and the gate passes. A second `--update` writes the same bytes. With zero keys found, it
writes `{"allowed": {}}`.
*An example test that compares the file bytes after each run.*

**C11. Behaviour: `--only standards --update` lowers the suppressions by the same rule.** Proves
ADR 0003 § Decision, G2. It lowers `.loop/suppressions.json` as C9 and C10 state, and the
`new_suppression` rule stays.
*An example test.*

**C12. Behaviour: `--only audit --update` only deletes.** Proves ADR 0003 § Decision, G3. It
deletes each accepted id that the command no longer prints, and keeps the reason and date of
every other entry byte for byte. A new id is not written, and fails with `new_advisory`. With no
baseline file, it writes no file and prints that each accepted id needs a reason written by hand.
*Example tests.*

**C13. Refusal: the verb refuses a flag that cannot apply.** Proves ADR 0003 § Consequences, "The
verb". Each refusal exits 2 with code `USAGE` and the rule named here. `loop gates --update` with
no `--only` refuses with `update_needs_only`.
`loop gates --only test --update` refuses with `no_baseline`. `loop gates --full` and
`loop gates --only lint --full` refuse with `full_needs_audit`. No gate runs, and no file is
written.
*Example tests on the verb handler.*

**C14. Behaviour: `--full` audits with no changed lock file.** Proves ADR 0003 § Decision, G4.
`loop gates --only audit --full` runs the audit command in a checkout whose change touches no lock
file. With a baseline, it ratchets as C6 and C7 state.
*An example test whose command writes a marker file.*

**C15. Behaviour: the scheduled workflow audits the base and nothing else.** Proves A4.
`.github/workflows/audit.yml` holds a `schedule` trigger and a `workflow_dispatch` trigger, and no
`pull_request` or `push` trigger. One step runs `loop gates --only audit --full`.
*A Go test that parses the workflow file. The A4 `grep` is the command proof.*

**C16. Behaviour: a runbook's `make` target must exist.** Proves A6. A fenced line or code span
`make nosuch` fails with `make_target`, and the finding names the file and line. `make build`
(a rule), `make proto-check` (a `.PHONY` name), `make linux/amd64` (from `$(PLATFORMS)`) and a
name that a `%` rule matches pass. `make test GO=go1.26` passes, because no argument is checked. A
runbook that names `make` in a repository with no root `Makefile` fails with `make_target`.
*Example tests on fixture Makefiles in `internal/gates/testdata`.*

**C17. Behaviour: a runbook's package script must exist.** Proves ADR 0003 § Decision, G7.
`pnpm run nosuch`, `npm run nosuch`, `yarn run nosuch`, `bun run nosuch`, `pnpm nosuch` and
`yarn nosuch` fail with `package_script` when the root `package.json` names no such script.
`pnpm install`, `pnpm exec x`, `npm test`, `pnpm --filter web build`, `pnpm -C apps/web build`,
`pnpm -r build` and `pnpm run build --prod` give no finding. A script call in a repository with no
root `package.json` fails with `package_script`.
*Example tests.*

**C18. Boundary: the runbooks gate reports no command that is not one.** Proves ADR 0003 §
Decision, G7. A `make` or `pnpm` word that is not at command position gives no finding, as in the
span "`... by yourself. make sure this is part ...`". A Makefile with an `include` line, or with
a target that keeps a `$` after expansion, is open: a call that matches no target gives no
finding. `loop gates --only runbooks` passes on this repository at the head of work entry 6.
*Example tests, and the gate run on this repository in CI.*

**C19. Behaviour: a spec merge records its commit.** Proves ADR 0003 § Decision, G5, "A person's
merge of a reviewed PRD or spec passes". `doc_merged` from `spec_signoff` sets `spec_merged` to
the merge sha. This holds on the outran path, whose second apply is that row.
*An example test on the machine, for the sign-off path and the outran path.*

**C20. Behaviour: `MergeOf` returns the reviewed head at the merge.** Proves ADR 0003 § Decision,
G5. For a ticket, a PRD and a spec, `MergeOf(project, sha)` returns the item, the fact, and the
reviewed head that the item held at its merge transition. After a PRD merges and the spec then
passes review, `MergeOf` of the PRD's commit still returns the PRD's reviewed head. A ticket
merged from `in_review` returns an empty reviewed head. An unknown sha returns not found.
`Outran` is true for a PRD merged from `prd_review` or `prd_amending`, and for a spec merged
from `spec_review` or `spec_amending`. It is false for a document merged from sign-off, and for a
spec merged from sign-off after its PRD outran its own review.
*Example tests on a store with history rows, one for each of the six `Outran` cases.*

**C21. Behaviour: a commit with no merge record raises one alert, once.** Proves A5. A
first-parent commit on the base that no item records raises one `unreviewed_merge` alert, with
subject `<project>@<sha>`. The body names the commit's subject and author. The next tick, with no
new commit, raises no alert and does not count up the open one.
*An example test on a test mirror and store, over two ticks.*

**C22. Behaviour: a recorded merge passes only at its reviewed head.** Proves ADR 0003 §
Decision, G5. A recorded merge whose `MergeOf` says `Outran` raises `unreviewed_merge`, whatever
its parents and its reviewed head. Any other merge commit that an item records passes when its
second parent equals the reviewed head that `MergeOf` returns. It raises `unreviewed_merge` when
the second parent differs, or when the reviewed head is empty. Any other one-parent commit that an
item records passes when the reviewed head is not empty. The body of each alert states which rule
failed: `outran`, `head_mismatch` or `no_reviewed_head`.
*Example tests: a direct-mechanism ticket merge (passes), a PRD merge at its pinned head
(passes), a ticket merged from `in_review` (fails), a recorded squash ticket commit at a reviewed
head (passes), a spec squash-merged during its review after its PRD passed at another head
(fails `outran`), a PRD squash-merged during review after a pass, a comment and an amendment
(fails `outran`), and an outran merge commit whose second parent equals the stale reviewed head
(fails `outran`).*

**C23. Boundary: the first tick audits nothing.** Proves ADR 0003 § Decision, G5, "The cursor
starts at the base head on the first tick". With no `base_audit` cursor, the tick writes the base
head as the cursor and raises no alert, whatever the history holds.
*An example test on a base with ten direct pushes.*

**C24. Behaviour: a rewritten base raises one alert and moves the cursor.** Proves ADR 0003 §
Decision, G5. When the base head does not reach the cursor, the tick raises one `base_rewritten`
alert that names the old cursor and the new head. It raises no `unreviewed_merge` for the new
range, and it writes the new head as the cursor.
*An example test that resets the test base to an unrelated commit.*

**C25. Refusal: the audit never judges a commit on a partial read, and never skips a commit.**
Proves A5. On a tick whose `pollPulls` returns an error, the audit judges each commit that holds a
merge record, and stops at the first commit that holds none. It raises no `unreviewed_merge` for
that commit, and the cursor stays before it. On the next tick whose `pollPulls` returns no error,
that commit is judged. When the alert write for a commit fails, the cursor stays before that
commit, and the next tick audits it.
*Example tests: a failing pull list over a base whose recorded merge comes before a direct push
(the merge is judged and the cursor stops before the push), the same base on a later tick with a
working pull list (the push raises one alert), and a store that fails one write.*

**C26. Invariant: the base audit makes no GitHub request.** Proves ADR 0003 § Decision, G5, "We
reject a port of `audit-main.ts`". `auditBase` sends no request to the GitHub API.
*A test on `githubtest` that counts requests across one audit of three commits.*

**C27. Behaviour: the base audit walks every first-parent commit in order.** Proves ADR 0003 §
Decision, G5. With three commits after the cursor, the step judges each one, oldest first, and
leaves the cursor at the newest. A commit that a merge brought in on a second parent is never
judged.
*An example test on a base with a merge whose branch holds two commits.*

**C28. Behaviour: the pack names the reason rule for a raised baseline.** Proves ADR 0003 §
Consequences, "The pack". `pack/runbooks/implementer.md` states that a new baseline file or a
raised count needs a reason in the pull request body. `pack/runbooks/code-reviewer.md` rejects a
pull request that adds or raises a baseline with no reason. Every `loop gates` command the two
runbooks name passes the pack gate's schema check.
*`make pack-check`, and `grep -c 'baseline' pack/runbooks/code-reviewer.md` prints at least `1`.
This is a command proof, because a runbook line has no test.*

**C29. Behaviour: `loop-init` scaffolds the audit workflow.** Proves ADR 0003 § Consequences,
"The pack". `pack/skills/loop-init/SKILL.md` tells the agent to write
`.github/workflows/audit.yml` with `loop gates --only audit --full`, and to run
`loop gates --only lint --update` once for a project that adopts the loop with lint debt.
*`make pack-check`, and `grep -c 'loop gates --only audit --full' pack/skills/loop-init/SKILL.md`
prints at least `1`. A command proof.*

**C30. Invariant: every `jq` line that `loop-init` teaches reads silence as a failure.** Proves
ADR 0003 § Consequences, "What this makes hard", and "The pack". `pack/skills/loop-init/SKILL.md`
holds at least one `jq` line that prints `loop:end`. Each such line has the form of the
Decisions: `jq -rn '[inputs] | select(length > 0) | (.[] | <keys>), "loop:end"'`, with only the
key filter its own. With its key filter replaced by `.a[]`, each line prints nothing on empty
input, and prints `x`, `y` and one `loop:end`, in that order, on the stream `{"a":["x"]}`
`{"a":["y"]}`. No key filter holds `?` or `// empty`.
*A Go test, `internal/pack/loopinit_jq_test.go`, that extracts each `jq` command holding
`loop:end` from the skill, matches it against the form, and runs it as stated with the `jq` on its
`PATH`. It fails when the skill holds no such line, and when `jq` is absent.*

**C31. Behaviour: a walk stopped for 15 minutes raises one alert.** Proves ADR 0003 §
Consequences, "What this makes easy": "A merge outside the loop reaches the inbox on the next
tick". When `pollPulls` fails on every tick and the walk stops at one commit, ticks before 15
minutes raise no alert. The first tick at 15 minutes or more after the first stop raises one `base_audit_stalled` alert, with
subject `<project>@<sha>`, and a body that names the commit and the first line of the `pollPulls`
error. Later ticks raise no second alert while it is open. A stop at a different commit starts the
15 minutes again. Once the walk passes the commit, the stall row is gone.
*An example test with a fake clock and a pull list that fails on every tick, over 16 ticks one
minute apart, then one tick with a working pull list.*

## Observable behaviour

None, because this epic draws no screen. Its outputs are gate findings in a log, two files under
the project's control, one CI workflow and three alert kinds. The inbox already lists every alert.

## Open questions

- **Q1. Split the base audit into its own epic?** The scope check's split rule says a second
  component needs a line why it cannot be its own epic. The base audit (work entries 7 and 8)
  shares no type, file or contract with the ratchet gates (entries 1 to 6), and it lands and is
  proven alone. ADR 0003 decides both, and its sign-off is past, so `loop-sdlc-split` § 4 puts the
  split with the operator. The proposal: keep this spec as it is for entries 1 to 6, and move
  entries 7 and 8 with C19 to C27 and C31 to a new epic, whose source is ADR 0003 § Decision, G5. The
  other answer is to keep one epic, as this spec is written. Either answer needs no change to the
  design. This blocks the decomposer's cut, not any entry's content.
