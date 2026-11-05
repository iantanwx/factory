package agenthosttest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/fake"
)

// recTB records a failure instead of failing the test it wraps.
type recTB struct {
	testing.TB
	failed bool
}

func (r *recTB) Error(...any)          { r.failed = true }
func (r *recTB) Errorf(string, ...any) { r.failed = true }
func (r *recTB) Fail()                 { r.failed = true }
func (r *recTB) FailNow()              { r.failed = true }
func (r *recTB) Fatal(...any)          { r.failed = true }
func (r *recTB) Fatalf(string, ...any) { r.failed = true }
func (r *recTB) Failed() bool          { return r.failed }

// fakeStart returns a fake host and a spec that starts one session on it.
func fakeStart(t *testing.T) (*fake.Host, agenthost.StartSpec) {
	t.Helper()
	h := fake.New(t.TempDir())
	co, err := h.CreateCheckout(context.Background(), agenthost.CheckoutSpec{Name: "work"})
	if err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "p.txt")
	if err := os.WriteFile(prompt, []byte("do the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, agenthost.StartSpec{Kind: agenthost.KindStage, Checkout: co.Path, Harness: agenthost.Claude,
		Model: "claude-opus-5-5", PromptFile: prompt}
}

// C1: a scenario cannot start past its declared sessions.
func TestCountingRefusesAStartPastTheDeclaredSessions(t *testing.T) {
	for _, n := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			h, spec := fakeStart(t)
			rec := &recTB{TB: t}
			counted := Counting(rec, h, n)
			for i := range n {
				if _, err := counted.Start(context.Background(), spec); err != nil {
					t.Fatalf("declared start %d of %d: %v", i+1, n, err)
				}
			}
			if rec.failed {
				t.Fatal("a declared start failed the test")
			}
			if _, err := counted.Start(context.Background(), spec); !errors.Is(err, ErrOverDeclared) {
				t.Fatalf("start %d of %d declared = %v; want ErrOverDeclared", n+1, n, err)
			}
			if !rec.failed {
				t.Fatal("a start past the declared sessions left the test green")
			}
			if got := len(h.Launches()); got != n {
				t.Fatalf("the host launched %d sessions; want %d", got, n)
			}
		})
	}
}

// C1: a refused start still counts, because it can still start a harness.
func TestCountingCountsARefusedStart(t *testing.T) {
	h, spec := fakeStart(t)
	rec := &recTB{TB: t}
	counted := Counting(rec, h, 1)
	outside := spec
	outside.Checkout = t.TempDir()
	if _, err := counted.Start(context.Background(), outside); !errors.Is(err, agenthost.ErrOutsideRoot) {
		t.Fatalf("start outside the root = %v; want ErrOutsideRoot", err)
	}
	if _, err := counted.Start(context.Background(), spec); !errors.Is(err, ErrOverDeclared) {
		t.Fatalf("start after a refused start = %v; want ErrOverDeclared", err)
	}
	if got := len(h.Launches()); got != 0 {
		t.Fatalf("the host launched %d sessions; want 0", got)
	}
}

// C2: a plan above the cap starts nothing.
func TestCheckPlanRefusesAPlanAboveTheCap(t *testing.T) {
	if LaneSessionCap != 20 {
		t.Fatalf("LaneSessionCap = %d; want 20", LaneSessionCap)
	}
	for _, tc := range []struct {
		plan []LaneEntry
		ok   bool
	}{
		{nil, true},
		{[]LaneEntry{{Name: "a", Sessions: 0}}, true},
		{[]LaneEntry{{Name: "a", Sessions: 18}, {Name: "b", Sessions: 2}}, true},
		{[]LaneEntry{{Name: "a", Sessions: 18}, {Name: "b", Sessions: 3}}, false},
	} {
		if err := CheckPlan(tc.plan, LaneSessionCap); (err == nil) != tc.ok {
			t.Errorf("CheckPlan(%+v) = %v; want ok %v", tc.plan, err, tc.ok)
		}
	}
}

// laneEnv holds every LOOP_ORCA_* value the gate requires.
func laneEnv(missing ...string) func(string) string {
	return func(name string) string {
		if slices.Contains(missing, name) || !strings.HasPrefix(name, "LOOP_ORCA_") {
			return ""
		}
		return "set"
	}
}

// C2: a lane binary given a plan above the cap exits 1 before it builds a
// host or starts a session.
func TestLaneGateStartsNothingOnAPlanAboveTheCap(t *testing.T) {
	h, _ := fakeStart(t)
	built := false
	var out bytes.Buffer
	g := LaneGate{
		Getenv:  laneEnv(),
		Plan:    []LaneEntry{{Name: "a", Sessions: 20}, {Name: "b", Sessions: 1}},
		Harness: agenthost.Claude,
		Quota:   func() (agenthost.QuotaReader, error) { built = true; return h, nil },
		Out:     &out,
	}
	if code := g.Run(context.Background()); code != 1 {
		t.Fatalf("Run = %d; want 1. Output:\n%s", code, out.String())
	}
	if built || len(h.Launches()) != 0 {
		t.Fatalf("a plan above the cap built the host (%v) or launched %d sessions", built, len(h.Launches()))
	}
}

// fakeOnly names each scenario that needs a capability only the fake has.
var fakeOnly = map[string]string{
	"incomplete_inventory_never_absent":  "InventoryBreaker",
	"never_ready_yields_not_ready":       "ReadinessBreaker",
	"quiet_after_ended_turn_reads_ended": "TurnDriver",
	"tool_call_running_never_ended":      "TurnDriver",
	"recorded_mismatch_refuses_start":    "ModelDropper",
	"missing_receipt_starts_session":     "ModelSilencer",
	"notify_refused_is_an_error":         "NotifyRefuser",
	"resume_or_refuse":                   "ResumeRefuser",
}

// C3: every real test declares its sessions.
func TestEveryLaneTestDeclaresItsSessions(t *testing.T) {
	plan := map[string]int{}
	total := 0
	for _, e := range LanePlan() {
		if _, dup := plan[e.Name]; dup || e.Sessions < 0 {
			t.Errorf("plan entry %+v is a duplicate or declares fewer than 0 sessions", e)
		}
		plan[e.Name] = e.Sessions
		total += e.Sessions
	}
	for _, sc := range scenarios {
		n, inPlan := plan[sc.name]
		switch {
		case sc.fakeOnly != fakeOnly[sc.name]:
			t.Errorf("scenario %s is fake-only for %q; want %q", sc.name, sc.fakeOnly, fakeOnly[sc.name])
		case sc.fakeOnly != "" && (inPlan || sc.sessions != 0):
			t.Errorf("fake-only scenario %s is in the plan (%v) or declares %d sessions", sc.name, inPlan, sc.sessions)
		case sc.fakeOnly == "" && (!inPlan || n != sc.sessions || n < 1):
			t.Errorf("scenario %s declares %d sessions; the plan holds %d (present %v)", sc.name, sc.sessions, n, inPlan)
		}
	}
	if total != 15 {
		t.Fatalf("the lane plan totals %d sessions; want 15", total)
	}
}

// laneTestFuncs scans the lane's test files: every top-level Test function
// in a _test.go file whose build line needs the tag orca, except
// TestOrcaContract, whose sessions are its scenarios' entries.
func laneTestFuncs(t *testing.T, dirs ...string) []string {
	t.Helper()
	var names []string
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no test files in %s: %v", dir, err)
		}
		for _, path := range files {
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			if !needsOrca(t, f) {
				continue
			}
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				name := ""
				if ok && fn.Recv == nil {
					name = fn.Name.Name
				}
				if strings.HasPrefix(name, "Test") && name != "TestMain" && name != "TestOrcaContract" {
					names = append(names, name)
				}
			}
		}
	}
	return names
}

// needsOrca reports whether f's build line is false with the tag orca unset
// and every other tag set.
func needsOrca(t *testing.T, f *ast.File) bool {
	t.Helper()
	for _, g := range f.Comments {
		if g.Pos() > f.Package {
			break
		}
		for _, c := range g.List {
			if !constraint.IsGoBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				t.Fatal(err)
			}
			return !expr.Eval(func(tag string) bool { return tag != "orca" })
		}
	}
	return false
}

// C3: the plan names each lane test function, and no other test.
func TestLanePlanNamesEveryLaneTestFunction(t *testing.T) {
	scanned := laneTestFuncs(t, "../orca", "../../../acceptance/scenarios")
	isScenario := map[string]bool{}
	for _, sc := range scenarios {
		isScenario[sc.name] = true
	}
	var planned []string
	for _, e := range LanePlan() {
		if !isScenario[e.Name] {
			planned = append(planned, e.Name)
		}
	}
	slices.Sort(scanned)
	slices.Sort(planned)
	if !slices.Equal(scanned, planned) {
		t.Fatalf("lane test functions %q; the plan names %q", scanned, planned)
	}
}

func TestDeclaredReadsThePlan(t *testing.T) {
	if got := Declared(t, "TestOrcaAgentRunsShellCommands"); got != 1 {
		t.Fatalf("Declared(TestOrcaAgentRunsShellCommands) = %d; want 1", got)
	}
	if got := Declared(t, f1Name); got != 3 {
		t.Fatalf("Declared(%s) = %d; want 3", f1Name, got)
	}
	if got := Declared(t, "TestC14FalseReaderFindingsRejectNothing"); got != 1 {
		t.Fatalf("Declared(TestC14FalseReaderFindingsRejectNothing) = %d; want 1", got)
	}
	rec := &recTB{TB: t}
	Declared(rec, "TestNoSuchLaneTest")
	if !rec.failed {
		t.Fatal("Declared of a name outside the plan left the test green")
	}
}

// f1Name is the real-agent F1 scenario, and f1File holds it.
const (
	f1Name = "TestF1RealAgentsTakeATicketFromClaimToMerge"
	f1File = "../../../acceptance/scenarios/f1_orca_test.go"
)

// C28: F1 runs only under the lane's tags, and builds its world with
// OrcaHost(Declared(t, <its own name>)), so the world's Counting refuses a
// fourth start.
func TestF1IsBoundedByThePlan(t *testing.T) {
	src, err := os.ReadFile(f1File)
	if err != nil {
		t.Fatal(err)
	}
	if first, _, _ := strings.Cut(string(src), "\n"); first != "//go:build acceptance && orca" {
		t.Fatalf("%s starts with %q; want //go:build acceptance && orca", f1File, first)
	}
	f, err := parser.ParseFile(token.NewFileSet(), f1File, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if d, ok := d.(*ast.FuncDecl); ok && d.Recv == nil && d.Name.Name == f1Name {
			fn = d
		}
	}
	if fn == nil {
		t.Fatalf("%s defines no %s", f1File, f1Name)
	}
	var args []string
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && callee(c) == "acceptance.OrcaHost" && len(c.Args) == 1 {
			args = append(args, exprString(c.Args[0]))
		}
		return true
	})
	want := "agenthosttest.Declared(t, " + strconv.Quote(f1Name) + ")"
	if len(args) != 1 || args[0] != want {
		t.Fatalf("%s calls OrcaHost with %q; want one call with %s", f1Name, args, want)
	}
}

func callee(c *ast.CallExpr) string {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name + "." + sel.Sel.Name
}

func exprString(e ast.Expr) string {
	var b bytes.Buffer
	if err := printer.Fprint(&b, token.NewFileSet(), e); err != nil {
		return ""
	}
	return b.String()
}

// laneDriver offers only the capabilities the orca contract driver offers.
type laneDriver struct{ h *fake.Host }

func (d laneDriver) ProcessArgs() ([][]string, error) { return d.h.ProcessArgs() }
func (d laneDriver) CheckoutExists(path string) bool  { return d.h.CheckoutExists(path) }

// endsOnRead is a fake whose session has ended by the time the contract asks,
// as a real session ends on its own.
type endsOnRead struct{ *fake.Host }

func (h endsOnRead) Ended(ctx context.Context, s agenthost.Session) (agenthost.EndReport, error) {
	h.Exit(s.ID, 0)
	return h.Host.Ended(ctx, s)
}

// laneFactory builds a lane-mode env over a fresh fake, and keeps each host
// by its scenario's test name.
func laneFactory(hosts map[string]*fake.Host) ContractFactory {
	return func(t *testing.T) ContractEnv {
		h := fake.New(t.TempDir())
		hosts[t.Name()] = h
		n := 0
		return ContractEnv{
			Host: endsOnRead{h},
			NewCheckout: func(tb testing.TB) string {
				n++
				co, err := h.CreateCheckout(context.Background(), agenthost.CheckoutSpec{Name: fmt.Sprint("co", n)})
				if err != nil {
					tb.Fatalf("CreateCheckout: %v", err)
				}
				return co.Path
			},
			Harness:     agenthost.Claude,
			Model:       "claude-opus-5-5",
			EndedWithin: time.Minute,
			OutsideRoot: t.TempDir(),
			Driver:      laneDriver{h},
			Lane:        true,
		}
	}
}

// C4: lane mode runs no fake-only scenario.
func TestLaneModeRunsNoFakeOnlyScenario(t *testing.T) {
	var out bytes.Buffer
	laneOut = &out
	t.Cleanup(func() { laneOut = os.Stdout })
	hosts := map[string]*fake.Host{}
	t.Run("lane", func(t *testing.T) { RunContract(t, laneFactory(hosts)) })
	for name, capability := range fakeOnly {
		line := "fake-only: " + name + " needs " + capability
		if !strings.Contains(out.String(), line+"\n") {
			t.Errorf("lane output lacks %q:\n%s", line, out.String())
		}
		if h := hosts[t.Name()+"/lane/"+name]; h != nil && len(h.Launches()) != 0 {
			t.Errorf("fake-only scenario %s launched %d sessions", name, len(h.Launches()))
		}
	}
	launched := 0
	for _, h := range hosts {
		launched += len(h.Launches())
	}
	if launched != 9 {
		t.Fatalf("the lane launched %d sessions; want 9", launched)
	}
}

// C4: lane mode refuses a skip that is not fake-only. The stub run happens in
// a child process, because its failure would fail this test.
func TestLaneModeFailsAScenarioThatSkips(t *testing.T) {
	if mode := os.Getenv("AGENTHOSTTEST_SKIP_STUB"); mode != "" {
		scenarios = []scenario{{name: "stub_skips", run: func(t *testing.T, _ *contract) { t.Skip("stub skips") }}}
		factory := laneFactory(map[string]*fake.Host{})
		RunContract(t, func(t *testing.T) ContractEnv {
			env := factory(t)
			env.Lane = mode == "lane"
			return env
		})
		return
	}
	for _, mode := range []string{"lane", "fake"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLaneModeFailsAScenarioThatSkips$", "-test.v")
		cmd.Env = append(os.Environ(), "AGENTHOSTTEST_SKIP_STUB="+mode)
		out, err := cmd.CombinedOutput()
		failed := strings.Contains(string(out), "--- FAIL: TestLaneModeFailsAScenarioThatSkips/stub_skips")
		if (mode == "lane") != (failed && err != nil) {
			t.Errorf("%s mode: a skipping scenario failed %v (exit %v); want failed %v. Output:\n%s",
				mode, failed, err, mode == "lane", out)
		}
	}
}

// C5: a missing LOOP_ORCA_* value fails, and never skips.
func TestLaneGateFailsOnAMissingValue(t *testing.T) {
	names := []string{"LOOP_ORCA_ROOT", "LOOP_ORCA_REPO", "LOOP_ORCA_MODEL"}
	for mask := range 8 {
		var missing []string
		for i, name := range names {
			if mask&(1<<i) != 0 {
				missing = append(missing, name)
			}
		}
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			h, _ := fakeStart(t)
			built := false
			var out bytes.Buffer
			g := LaneGate{
				Getenv:  laneEnv(missing...),
				Plan:    LanePlan(),
				Harness: agenthost.Claude,
				Quota:   func() (agenthost.QuotaReader, error) { built = true; return h, nil },
				Out:     &out,
			}
			code := g.Run(context.Background())
			want := 0
			if len(missing) > 0 {
				want = 1
			}
			if code != want || built == (len(missing) > 0) {
				t.Fatalf("Run = %d, built the host %v; want %d. Output:\n%s", code, built, want, out.String())
			}
			for _, name := range names {
				has := strings.Contains(out.String(), name+" is not set\n")
				if has != slices.Contains(missing, name) {
					t.Errorf("output names %s as not set: %v. Output:\n%s", name, has, out.String())
				}
			}
		})
	}
}

// quotaReader answers one scripted reading.
type quotaReader struct {
	q   agenthost.Quota
	err error
}

func (r quotaReader) Quota(context.Context, agenthost.Harness) (agenthost.Quota, error) {
	return r.q, r.err
}

// C6: a spent window stops the lane and names the reset.
func TestQuotaGateStopsAtTheLimitAndNamesTheReset(t *testing.T) {
	reset := time.Date(2026, 10, 25, 5, 0, 0, 0, time.UTC)
	window := func(name string, used float64, at time.Time) quotaReader {
		return quotaReader{q: agenthost.Quota{Harness: agenthost.Claude, OK: true, Windows: []agenthost.QuotaWindow{
			{Name: "fableWeekly", UsedPercent: 10, ResetsAt: reset},
			{Name: name, UsedPercent: used, ResetsAt: at},
		}}}
	}
	unavailable := fmt.Errorf("%w: orca has no reading for claude", agenthost.ErrQuotaUnavailable)
	for _, tc := range []struct {
		r     quotaReader
		line  string
		spent bool
	}{
		{window("session", 93, reset), "quota: claude window session at 93% resets 2026-10-25T05:00:00Z", true},
		{window("weekly", 90, reset), "quota: claude window weekly at 90% resets 2026-10-25T05:00:00Z", true},
		{window("session", 90, time.Time{}), "quota: claude window session at 90% reset unknown", true},
		{window("session", 89.9, reset), "", false},
		{quotaReader{err: unavailable}, "quota: claude quota unavailable: orca has no reading for claude", false},
	} {
		line, spent := QuotaGate(context.Background(), tc.r, agenthost.Claude, 90)
		if line != tc.line || spent != tc.spent {
			t.Errorf("QuotaGate(%+v) = %q, %v; want %q, %v", tc.r, line, spent, tc.line, tc.spent)
		}
	}
}

// C6: the gate exits 3 on a spent window before any session, and goes on
// when the reading is unavailable.
func TestLaneGateExitsThreeOnASpentWindow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		quota *agenthost.Quota
		code  int
		line  string
	}{
		{"spent", &agenthost.Quota{Harness: agenthost.Claude, OK: true, Windows: []agenthost.QuotaWindow{
			{Name: "weekly", UsedPercent: 95, ResetsAt: time.Date(2026, 10, 25, 5, 0, 0, 0, time.UTC)},
		}}, 3, "quota: claude window weekly at 95% resets 2026-10-25T05:00:00Z\n"},
		{"unavailable", nil, 0, "quota: claude quota unavailable: claude\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := fakeStart(t)
			if tc.quota != nil {
				h.SetQuota(*tc.quota)
			}
			var out bytes.Buffer
			g := LaneGate{
				Getenv:  laneEnv(),
				Plan:    LanePlan(),
				Harness: agenthost.Claude,
				Quota:   func() (agenthost.QuotaReader, error) { return h, nil },
				Out:     &out,
			}
			if code := g.Run(context.Background()); code != tc.code || out.String() != tc.line {
				t.Fatalf("Run = %d, output %q; want %d, %q", code, out.String(), tc.code, tc.line)
			}
			if len(h.Launches()) != 0 {
				t.Fatalf("the gate launched %d sessions", len(h.Launches()))
			}
		})
	}
}

func TestLaneGateFailsWhenItCannotBuildTheHost(t *testing.T) {
	var out bytes.Buffer
	g := LaneGate{
		Getenv:  laneEnv(),
		Plan:    LanePlan(),
		Harness: agenthost.Claude,
		Quota:   func() (agenthost.QuotaReader, error) { return nil, errors.New("orca: no binary") },
		Out:     &out,
	}
	if code := g.Run(context.Background()); code != 1 || !strings.Contains(out.String(), "orca: no binary") {
		t.Fatalf("Run = %d, output %q; want 1 and the build error", code, out.String())
	}
}
