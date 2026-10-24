// Package overwatch is the sweep that wakes the coordinator (ADR 0009). A
// sweep reads the records once and runs every part at once. Each part reads
// the loop's state through one World and returns its findings and notes. A
// finding is work until an open record names its key. The native job
// `overwatch` rings the coordinator when a sweep holds work or a failed
// read, and `loop overwatch sweep` prints the same report.
package overwatch

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
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
	Key  string // overwatch:<part>:<subject>, or a fault id for the bugs part
	Line string // quotes Key
}

// Reading is a part's findings and its notes.
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
var PartNames = []string{"stalls", "questions", "decisions", "bugs", "backlog", "claims", "pulls", "worktrees", "main", "ci",
	"flaky", "audit", "timer-health", "inbox", "server"}

// Parts returns the shipped part list. Each part entry adds its part.
func Parts() []Part {
	return []Part{bugsPart(), claimsPart(), worktreesPart(), timersPart(), inboxPart(), serverPart()}
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
	// CheckoutDirs lists each directory named loop-* one level under a
	// directory of agent_host.checkout_root, as absolute paths.
	CheckoutDirs(ctx context.Context) ([]string, error)
	Inventory(ctx context.Context) (agenthost.Inventory, error)
	Coordinator() sched.CoordinatorState
	GitHub() GitHubReads
	Doctor(ctx context.Context) ([]github.CheckLine, error) // install drift, then GitHub.Doctor
	LastRing(ctx context.Context) (RingOutcome, bool, error)
}

// GitHubReads are the GET reads of the GitHub parts, made with ScopeRead.
// The entries of the GitHub parts add WorkflowRuns and RunJobs with the
// types they read (docs/plans/factorio-a7r-overwatch-sweep-spec.md, Work 9
// and 10).
type GitHubReads interface {
	OpenPulls(ctx context.Context, p config.Project) ([]github.PR, error)
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

// partResult is one part's reading, or the UNREAD reason that replaces it.
type partResult struct {
	reading Reading
	unread  string
}

// Sweep reads the records and runs every part at once, each under
// PartDeadline. An error, a panic or a passed deadline becomes code 2 and
// one UNREAD line for that part only. The report holds one PartReport for
// each part given, in PartNames order. A sweep of no part reads no record:
// no finding needs one, so it holds code 0.
func Sweep(ctx context.Context, w World, parts []Part) Report {
	if len(parts) == 0 {
		return Report{At: w.Now(), Parts: []PartReport{}}
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
	results := make([]partResult, len(parts))
	for i, p := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := bounded(ctx, func(ctx context.Context) (Reading, error) { return p.Read(ctx, w) })
			results[i] = partResult{reading: res.value, unread: res.unread}
		}()
	}
	wg.Wait()

	r := Report{At: w.Now(), Records: recsErr, Parts: make([]PartReport, 0, len(parts))}
	if recsErr != "" {
		r.Code = Unread
	}
	for i, p := range parts {
		pr := classify(p, results[i], recs, recsErr == "")
		r.Code = max(r.Code, pr.Code)
		r.Parts = append(r.Parts, pr)
	}
	slices.SortStableFunc(r.Parts, func(a, b PartReport) int { return cmp.Compare(order(a.Name), order(b.Name)) })
	return r
}

// order is a part's place in PartNames. A name outside it sorts last.
func order(name string) int {
	if i := slices.Index(PartNames, name); i >= 0 {
		return i
	}
	return len(PartNames)
}

// classify turns one part's result into its report. A finding that recs
// names is filed when the records read worked.
func classify(p Part, res partResult, recs Records, recsOK bool) PartReport {
	pr := PartReport{Name: p.Name, Command: p.Command, Lines: []string{}}
	if res.unread != "" {
		pr.Code, pr.Lines = Unread, []string{res.unread}
		return pr
	}
	var filed []string
	for _, f := range res.reading.Findings {
		if rec, ok := recs.Names(f.Key); recsOK && ok {
			filed = append(filed, fmt.Sprintf("%s (filed in %s)", f.Line, rec))
			continue
		}
		pr.Code = Work
		pr.Lines = append(pr.Lines, f.Line)
	}
	pr.Lines = append(append(pr.Lines, filed...), res.reading.Notes...)
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
