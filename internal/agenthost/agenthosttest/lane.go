package agenthosttest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/sched"
)

// The real-orca lane spends harness quota, so every real test declares the
// sessions it starts, and the lane plan bounds their total.

// LaneSessionCap is the most sessions one lane run may declare.
const LaneSessionCap = 20

// Exit codes of a lane binary that LaneGate stops.
const (
	LaneExitFail  = 1 // a missing value, or a plan above the cap
	LaneExitSpent = 3 // a quota window at or above the limit
)

// LaneEntry is one real-orca test and the sessions it declares.
type LaneEntry struct {
	Name     string
	Sessions int
}

// laneTests are the lane's test functions. TestOrcaContract has no entry,
// because its sessions are its scenarios' entries.
var laneTests = []LaneEntry{
	{Name: "TestOrcaAgentRunsShellCommands", Sessions: 1},
	// One implementer and one code-reviewer. A repair round would make
	// the flow branch on a model's answer.
	{Name: "TestF1RealAgentsTakeATicketFromClaimToMerge", Sessions: 2},
}

// laneOut takes the lane mode's fake-only lines.
var laneOut io.Writer = os.Stdout

// LanePlan returns every real-orca test and its declared sessions: each
// contract scenario that is not fake-only, then each lane test function.
func LanePlan() []LaneEntry {
	var plan []LaneEntry
	for _, sc := range scenarios {
		if sc.fakeOnly == "" {
			plan = append(plan, LaneEntry{Name: sc.name, Sessions: sc.sessions})
		}
	}
	return append(plan, laneTests...)
}

// CheckPlan refuses a plan whose total passes limit.
func CheckPlan(plan []LaneEntry, limit int) error {
	total := 0
	for _, e := range plan {
		total += e.Sessions
	}
	if total > limit {
		return fmt.Errorf("the lane plan declares %d sessions, above the cap of %d", total, limit)
	}
	return nil
}

// Declared returns the sessions the plan declares for name. It fails tb
// when the plan holds no entry of that name.
func Declared(tb testing.TB, name string) int {
	tb.Helper()
	for _, e := range LanePlan() {
		if e.Name == name {
			return e.Sessions
		}
	}
	tb.Fatalf("the lane plan declares no sessions for %s", name)
	return 0
}

var ErrOverDeclared = errors.New("agenthosttest: the scenario started more sessions than it declares")

// Counting wraps h. Start past declared fails t and returns ErrOverDeclared
// without calling h. A refused Start counts, because it can still start a
// harness on the host.
func Counting(t testing.TB, h agenthost.Host, declared int) agenthost.Host {
	return &counting{Host: h, t: t, left: declared}
}

type counting struct {
	agenthost.Host
	t    testing.TB
	mu   sync.Mutex
	left int
}

func (c *counting) Start(ctx context.Context, spec agenthost.StartSpec) (agenthost.Session, error) {
	c.mu.Lock()
	over := c.left <= 0
	c.left--
	c.mu.Unlock()
	if over {
		c.t.Errorf("Start of a %s session: %v", spec.Kind, ErrOverDeclared)
		return agenthost.Session{}, ErrOverDeclared
	}
	return c.Host.Start(ctx, spec)
}

// RequireLaneEnv returns one "LOOP_ORCA_<NAME> is not set" line per missing value.
func RequireLaneEnv(getenv func(string) string) (missing []string) {
	for _, name := range []string{"LOOP_ORCA_ROOT", "LOOP_ORCA_REPO", "LOOP_ORCA_MODEL"} {
		if getenv(name) == "" {
			missing = append(missing, name+" is not set")
		}
	}
	return missing
}

// QuotaGate reads h's quota. It returns spent and a line that names the
// first window at or above limit and its reset. A failed read returns its
// reason, not spent, because the plan bounds the spend. A reading below
// limit returns an empty line.
func QuotaGate(ctx context.Context, r agenthost.QuotaReader, h agenthost.Harness, limit float64) (line string, spent bool) {
	q, err := r.Quota(ctx, h)
	if err != nil {
		return fmt.Sprintf("quota: %s %v", h, err), false
	}
	for _, w := range q.Windows {
		if w.UsedPercent < limit {
			continue
		}
		reset := "reset unknown"
		if !w.ResetsAt.IsZero() {
			reset = "resets " + w.ResetsAt.UTC().Format(time.RFC3339)
		}
		return fmt.Sprintf("quota: %s window %s at %g%% %s", h, w.Name, w.UsedPercent, reset), true
	}
	return "", false
}

// LaneGate is one lane binary's checks before any session.
type LaneGate struct {
	Getenv  func(string) string
	Plan    []LaneEntry
	Harness agenthost.Harness
	// Quota builds the quota reader. Run calls it only after the env check.
	Quota func() (agenthost.QuotaReader, error)
	Out   io.Writer
}

// Run does the env, plan and quota checks in that order, prints each line
// to Out, and returns 0, LaneExitFail or LaneExitSpent. A window at or above
// sched.QuotaLimit is spent.
func (g LaneGate) Run(ctx context.Context) int {
	if missing := RequireLaneEnv(g.Getenv); len(missing) > 0 {
		for _, line := range missing {
			_, _ = fmt.Fprintln(g.Out, line)
		}
		return LaneExitFail
	}
	if err := CheckPlan(g.Plan, LaneSessionCap); err != nil {
		_, _ = fmt.Fprintln(g.Out, err)
		return LaneExitFail
	}
	r, err := g.Quota()
	if err != nil {
		_, _ = fmt.Fprintf(g.Out, "quota: %v\n", err)
		return LaneExitFail
	}
	line, spent := QuotaGate(ctx, r, g.Harness, sched.QuotaLimit)
	if line != "" {
		_, _ = fmt.Fprintln(g.Out, line)
	}
	if spent {
		return LaneExitSpent
	}
	return 0
}
