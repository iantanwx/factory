// Package overwatch is the sweep that wakes the coordinator (ADR 0009). A
// sweep reads the records once and runs every part at once. Each part reads
// the loop's state through one World and returns its findings and notes. A
// finding is work until an open record names its key. The native job
// `overwatch` rings each coordinator whose projects hold work, and the loop
// project's coordinator when a read failed. `loop overwatch sweep` prints
// the same report.
package overwatch

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/frontend"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/store"
)

// Code is a part's or a report's exit convention (overwatch-report.ts,
// alpha).
type Code int

const (
	// Quiet: nothing for the coordinator.
	Quiet Code = 0
	// Work: at least one finding has a key that no record names.
	Work Code = 1
	// Unread: a read failed, so the part or the report is partial.
	Unread Code = 2
)

// Finding is one thing the coordinator acts on. It is work until a record
// names Key.
type Finding struct {
	Key     string    // overwatch:<part>:<subject>, or a fault id for the bugs part
	Line    string    // quotes Key
	At      time.Time // when a flaky finding's run started, and zero for the other parts
	Project string    // the project the finding is about, or "" for the instance
}

// Reading is a part's findings and its notes. A note that starts UNREAD
// is a read that failed for part of the part, such as one project's: the
// part has code 2, and the rest of its reading stands.
type Reading struct {
	Findings []Finding
	Notes    []string // lines that hold no work
}

// Part is one read of the sweep. Read makes no write.
type Part struct {
	Name    string // one of PartNames
	Command string // the loop command that repeats the read by hand; empty when none does
	Read    func(ctx context.Context, w World) (Reading, error)
}

// PartReport is one part's result in a report.
type PartReport struct {
	Name    string   `json:"name"`
	Code    Code     `json:"code"`
	Command string   `json:"command,omitempty"`
	Lines   []string `json:"lines"` // unfiled findings, then filed findings with their record, then notes
}

// Report is one sweep.
type Report struct {
	At      time.Time    `json:"at"`
	Code    Code         `json:"code"`
	Records string       `json:"records,omitempty"` // the UNREAD reason of the records read; empty when it worked
	Parts   []PartReport `json:"parts"`
}

// PartDeadline bounds each part, and the records read. The sweep has no
// deadline of its own, so one slow vendor costs its own part only.
const PartDeadline = 60 * time.Second

// partDeadline is PartDeadline, which a test shortens.
var partDeadline = PartDeadline

// PartNames is the order of every part that ports: of a report, of the ring
// text and of the runbook's table.
var PartNames = []string{"stalls", "questions", "decisions", "reminders", "bugs", "backlog", "starved", "claims", "pulls", "worktrees",
	"main", "ci", "flaky", "audit", "timer-health", "inbox", "server", "rules", "ui", "qa"}

// Parts returns the shipped part list. Each part entry adds its part.
func Parts() []Part {
	return []Part{stallsPart, questionsPart(), decisionsPart(), remindersPart(), bugsPart(), backlogPart(), starvedPart(), claimsPart(),
		pullsPart, worktreesPart(), mainPart, ciPart, flakyPart, auditPart, timersPart(), inboxPart(), serverPart(), rulesPart(), uiPart(),
		qaPart()}
}

// Intervals are the scheduler's own, from sched.Config.
type Intervals struct {
	Tick     time.Duration
	Liveness time.Duration
	Cleanup  time.Duration
}

// World is every read a part may make. The server builds the one
// production World.
type World interface {
	Now() time.Time
	Host(ctx context.Context) (*config.HostConfig, error)
	Projects(ctx context.Context) ([]config.Project, error)
	ProjectConfig(ctx context.Context, project string) (*config.ProjectSnapshot, error)
	Items(ctx context.Context, project string, f machine.ListFilter) ([]machine.Item, error)
	Body(ctx context.Context, project, item string) (string, error)
	// Dependencies are an item's beads edges of every kind, each with its
	// status. An empty list means the item has no edge.
	Dependencies(ctx context.Context, project, item string) ([]beadsx.Dependency, error)
	Store() *store.Store // read transactions only
	Intervals() Intervals
	// Jobs are the native jobs registered on the scheduler. A server with
	// no scheduler registers none.
	Jobs() sched.Jobs
	// Drains are the drains of the server that the store records, oldest
	// first, also those that a restart ended.
	Drains(ctx context.Context) ([]sched.Drain, error)
	// Pauses are the stage pauses that hold, oldest first.
	Pauses(ctx context.Context) ([]admission.StagePause, error)
	// WorkQuery is the server's admission, or nil on a server with none.
	WorkQuery() WorkQuery
	// CheckoutDirs lists each directory named loop-* one level under a
	// directory of agent_host.checkout_root, as absolute paths.
	CheckoutDirs(ctx context.Context) ([]string, error)
	Inventory(ctx context.Context) (agenthost.Inventory, error)
	// Coordinators holds each coordinator by the project it serves.
	Coordinators() map[string]sched.CoordinatorState
	GitHub() GitHubReads
	Doctor(ctx context.Context) ([]github.CheckLine, error) // host lines, then GitHub.Doctor
	// LastRings is the last ring to each destination project.
	LastRings(ctx context.Context) (map[string]RingOutcome, error)
	// Inbox is every entry of the inbox, as `loop inbox list` prints it.
	Inbox(ctx context.Context) ([]inbox.Entry, error)
	// Tracked is the tracked files of the project's tree at
	// origin/<its default branch>.
	Tracked(ctx context.Context, project string) (frontend.Tree, error)
}

// WorkQuery is what the starved part asks of admission. Each read makes no
// claim and no write.
type WorkQuery interface {
	Room(ctx context.Context, project, stage string) *cli.Refusal
	Peek(ctx context.Context, project, stage string) (*admission.Candidate, *cli.Refusal)
	Admissible(ctx context.Context, stage string, it machine.Item) (bool, error)
	FilesWait(ctx context.Context, it machine.Item) (*cli.Refusal, error)
}

// GitHubReads are the GET reads of the GitHub parts, made with ScopeRead.
type GitHubReads interface {
	OpenPulls(ctx context.Context, p config.Project) ([]github.PR, error)
	WorkflowRuns(ctx context.Context, p config.Project, q github.RunQuery) ([]github.RunView, error)
	RunJobs(ctx context.Context, p config.Project, runID int64, attempt int) ([]github.JobView, error)
}

// Key builds overwatch:<part>:<subject>, with the subject's segments joined
// by ":". A character a key cannot hold becomes "-", and a final "." or ":"
// goes, so every key matches the key grammar. Slug makes one subject
// segment safe.
func Key(part string, subject ...string) string {
	k := strings.Join(append([]string{"overwatch", part}, subject...), ":")
	return strings.TrimRight(strings.Map(func(r rune) rune {
		if keyRune(r) {
			return r
		}
		return '-'
	}, k), ".:")
}

// Slug lowers name, and turns each run of characters other than letters
// and digits into one "-". It drops a "-" at either end.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	return b.String()
}

// keyRune reports whether a key may hold r: an ASCII letter or digit, or
// one of ._/@:-.
func keyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._/@:-", r)
}

// Readings is one sweep before classify turns its findings into lines.
type Readings struct {
	At            time.Time
	Parts         []PartReading
	Records       Records
	RecordsUnread string // the UNREAD reason of the records read; empty when it worked
}

// PartReading is one part's reading, or the UNREAD reason that replaces
// it.
type PartReading struct {
	Part    Part
	Reading Reading
	Unread  string
}

// Sweep reads every part, and returns the report of the whole sweep.
func Sweep(ctx context.Context, w World, parts []Part) Report {
	return ReadAll(ctx, w, parts).Report()
}

// ReadAll reads the records and runs every part at once, each under
// PartDeadline. An error, a panic or a passed deadline becomes one UNREAD
// reason for that part only. A sweep of no part reads no record: no
// finding needs one.
func ReadAll(ctx context.Context, w World, parts []Part) Readings {
	if len(parts) == 0 {
		return Readings{At: w.Now()}
	}
	var (
		wg      sync.WaitGroup
		recs    Records
		recsErr string
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		res := bounded(ctx, func(ctx context.Context) (Records, error) { return ReadRecords(ctx, w) })
		recs, recsErr = res.value, res.unread
	}()
	results := make([]PartReading, len(parts))
	for i, p := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := bounded(ctx, func(ctx context.Context) (Reading, error) { return p.Read(ctx, w) })
			results[i] = PartReading{Part: p, Reading: res.value, Unread: res.unread}
		}()
	}
	wg.Wait()
	return Readings{At: w.Now(), Parts: results, Records: recs, RecordsUnread: recsErr}
}

// Report classifies the readings. It holds one PartReport for each part,
// in PartNames order.
func (r Readings) Report() Report {
	out := Report{At: r.At, Records: r.RecordsUnread, Parts: make([]PartReport, 0, len(r.Parts))}
	if r.RecordsUnread != "" {
		out.Code = Unread
	}
	for _, p := range r.Parts {
		pr := classify(p, r.Records, r.RecordsUnread == "")
		out.Code = max(out.Code, pr.Code)
		out.Parts = append(out.Parts, pr)
	}
	slices.SortStableFunc(out.Parts, func(a, b PartReport) int { return cmp.Compare(order(a.Name), order(b.Name)) })
	return out
}

// Route names the project whose coordinator gets f, a finding of part
// (spec factorio-1edo, Decisions, "Overwatch"). A rules finding compares a
// rule across the projects, so it is the instance's, and so is a finding
// with no project.
func Route(part string, f Finding, t coord.Topology) string {
	return t.Dest(f.Project, part == "rules" || f.Project == "")
}

// For keeps the findings that route to a destination that keep holds.
// Notes hold no work, and a failed read names no project, so both go to
// the loop project, and For keeps them when keep holds it.
func (r Readings) For(t coord.Topology, keep func(dest string) bool) Readings {
	loop := keep(t.Dest("", true))
	out := Readings{At: r.At, Records: r.Records, Parts: make([]PartReading, 0, len(r.Parts))}
	if loop {
		out.RecordsUnread = r.RecordsUnread
	}
	for _, p := range r.Parts {
		kept := PartReading{Part: p.Part}
		if loop {
			kept.Unread, kept.Reading.Notes = p.Unread, p.Reading.Notes
		}
		for _, f := range p.Reading.Findings {
			if keep(Route(p.Part.Name, f, t)) {
				kept.Reading.Findings = append(kept.Reading.Findings, f)
			}
		}
		out.Parts = append(out.Parts, kept)
	}
	return out
}

// Split returns the report of each destination project: the loop
// project's, and the report of each project that a finding routes to.
func Split(r Readings, t coord.Topology) map[string]Report {
	dests := map[string]bool{t.Dest("", true): true}
	for _, p := range r.Parts {
		for _, f := range p.Reading.Findings {
			dests[Route(p.Part.Name, f, t)] = true
		}
	}
	out := make(map[string]Report, len(dests))
	for dest := range dests {
		out[dest] = r.For(t, func(d string) bool { return d == dest }).Report()
	}
	return out
}

// Served keeps what `loop overwatch sweep` shows the coordinator of home:
// home's findings, and those of each destination that home's session
// serves.
func (r Readings) Served(t coord.Topology, sessions inbox.Sessions, home string) Readings {
	mine, ok := sessions(home)
	return r.For(t, func(dest string) bool {
		id, served := sessions(dest)
		return dest == home || ok && served && id == mine
	})
}

// order is a part's place in PartNames. A name outside it sorts last.
func order(name string) int {
	if i := slices.Index(PartNames, name); i >= 0 {
		return i
	}
	return len(PartNames)
}

// classify turns one part's reading into its report. A finding that recs
// names is filed when the records read worked.
func classify(p PartReading, recs Records, recsOK bool) PartReport {
	pr := PartReport{Name: p.Part.Name, Command: p.Part.Command, Lines: []string{}}
	if p.Unread != "" {
		pr.Code, pr.Lines = Unread, []string{p.Unread}
		return pr
	}
	var filed []string
	for _, f := range p.Reading.Findings {
		if rec, ok := recs.Files(f); recsOK && ok {
			filed = append(filed, fmt.Sprintf("%s (filed in %s)", f.Line, rec))
			continue
		}
		pr.Code = Work
		pr.Lines = append(pr.Lines, f.Line)
	}
	pr.Lines = append(append(pr.Lines, filed...), p.Reading.Notes...)
	if slices.ContainsFunc(p.Reading.Notes, func(n string) bool { return strings.HasPrefix(n, "UNREAD") }) {
		pr.Code = Unread
	}
	return pr
}

// boundedResult is the value of one bounded read, or why it is unread.
type boundedResult[T any] struct {
	value  T
	unread string
}

// bounded runs read under partDeadline. An error, a panic or a passed
// deadline becomes one UNREAD line. A read that ignores its context is left
// to end on its own, and no one waits on it.
func bounded[T any](ctx context.Context, read func(context.Context) (T, error)) boundedResult[T] {
	ctx, cancel := context.WithTimeout(ctx, partDeadline)
	defer cancel()
	done := make(chan boundedResult[T], 1)
	go func() {
		defer func() {
			if v := recover(); v != nil {
				done <- boundedResult[T]{unread: fmt.Sprintf("UNREAD: the read panicked: %v", v)}
			}
		}()
		v, err := read(ctx)
		if err != nil {
			done <- boundedResult[T]{unread: "UNREAD: " + err.Error()}
			return
		}
		done <- boundedResult[T]{value: v}
	}()
	select {
	case res := <-done:
		return res
	case <-ctx.Done():
		return boundedResult[T]{unread: fmt.Sprintf("UNREAD: the read passed its deadline of %s: %v", partDeadline, ctx.Err())}
	}
}
