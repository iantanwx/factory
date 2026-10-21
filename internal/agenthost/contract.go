package agenthost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ContractEnv is what one contract run needs from its factory. The suite
// runs every scenario against Host. A scenario that needs to inject a state
// asks Driver for a capability interface, and it skips when the driver has
// none. The fake implements every capability. A real host implements the
// ones it can drive without a person.
type ContractEnv struct {
	Host Host
	// NewCheckout makes a fresh checkout inside the checkout root and
	// returns its path. The suite removes it through Host.RemoveCheckout.
	NewCheckout func(t testing.TB) string
	Harness     Harness
	Model       string
	Effort      string
	// PromptDir holds the prompt files. Empty means t.TempDir().
	PromptDir string
	// Prompt is the prompt text. Empty means a short default.
	Prompt string
	// Wait bounds every read that a real host answers eventually. Zero
	// means one read.
	Wait time.Duration
	// EndedWithin bounds the wait for a real session to end on its own,
	// when Driver has no TurnDriver. Zero skips that scenario.
	EndedWithin time.Duration
	// OutsideRoot is an absolute path outside the checkout root.
	OutsideRoot string
	// Driver offers the optional capability interfaces below.
	Driver any
}

// ContractFactory builds one environment for one scenario.
type ContractFactory func(t *testing.T) ContractEnv

// Capability interfaces a contract driver may implement.
type (
	// Advancer moves the host's clock.
	Advancer interface{ Advance(d time.Duration) }
	// TurnDriver sets what the session's transcript shows.
	TurnDriver interface {
		EndTurn(id RunID)
		StartTool(id RunID)
	}
	// InventoryBreaker makes every inventory incomplete.
	InventoryBreaker interface{ SetInventoryIncomplete(on bool) }
	// ReadinessBreaker makes every later start never become ready.
	ReadinessBreaker interface{ SetNeverReady(on bool) }
	// ModelDropper makes the host drop the model argument of later starts.
	ModelDropper interface{ SetDropModel(on bool) }
	// NotifyRefuser makes the host refuse input to one session.
	NotifyRefuser interface{ RefuseNotify(id RunID, on bool) }
	// Staler makes one session's handle stale.
	Staler interface{ MakeStale(id RunID) }
	// ArgvReader lists the argument vector of every process the host
	// started, or every process on the machine.
	ArgvReader interface {
		ProcessArgs() ([][]string, error)
	}
	// NotifyObserver lists what a session received through Notify.
	NotifyObserver interface{ Notifications(id RunID) []string }
	// CheckoutObserver says whether a checkout still exists.
	CheckoutObserver interface{ CheckoutExists(path string) bool }
)

// RunContract runs the agent-host contract against the hosts that f builds.
func RunContract(t *testing.T, f ContractFactory) {
	t.Helper()
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			env := f(t)
			sc.run(t, newContract(t, env))
		})
	}
}

type scenario struct {
	name string
	run  func(t *testing.T, c *contract)
}

var scenarios = []scenario{
	{"start_inventory_settle", scStart},
	{"run_id_is_a_stable_lease_owner", scRunID},
	{"prompt_never_in_process_args", scArgv},
	{"incomplete_inventory_never_absent", scIncomplete},
	{"stale_handle_settles_unknown", scStale},
	{"never_ready_yields_not_ready", scNotReady},
	{"quiet_after_ended_turn_reads_ended", scEndedQuiet},
	{"tool_call_running_never_ended", scToolRunning},
	{"real_session_ends_on_its_own", scEndedReal},
	{"close_keeps_checkout", scCloseKeeps},
	{"dropped_model_reads_unverified", scDroppedModel},
	{"requested_model_reads_verified", scVerified},
	{"notify_idle_coordinator", scNotify},
	{"notify_refused_is_an_error", scNotifyRefused},
	{"stop_reports_exit_only_when_proven", scStop},
	{"outside_root_refused", scOutsideRoot},
}

type contract struct {
	env ContractEnv
	ctx context.Context
}

func newContract(t *testing.T, env ContractEnv) *contract {
	t.Helper()
	if env.Host == nil || env.NewCheckout == nil {
		t.Fatal("contract env needs Host and NewCheckout")
	}
	if env.PromptDir == "" {
		env.PromptDir = t.TempDir()
	}
	if env.Prompt == "" {
		env.Prompt = "Reply with the single word ready, then wait. Run no tool."
	}
	ctx := context.Background()
	if dl, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, dl)
		t.Cleanup(cancel)
	}
	return &contract{env: env, ctx: ctx}
}

// checkout makes a checkout and removes it when the scenario ends.
func (c *contract) checkout(t *testing.T) string {
	t.Helper()
	path := c.env.NewCheckout(t)
	t.Cleanup(func() { _ = c.env.Host.RemoveCheckout(context.Background(), path) })
	return path
}

// prompt writes text to a new prompt file and returns its path.
func (c *contract) prompt(t *testing.T, text string) string {
	t.Helper()
	f, err := os.CreateTemp(c.env.PromptDir, "prompt-*.txt")
	if err != nil {
		t.Fatalf("prompt file: %v", err)
	}
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("prompt file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("prompt file: %v", err)
	}
	return f.Name()
}

func (c *contract) spec(kind Kind, checkout, promptFile string) StartSpec {
	return StartSpec{
		Kind: kind, Checkout: checkout, Harness: c.env.Harness, Model: c.env.Model,
		Effort: c.env.Effort, PromptFile: promptFile, Title: "loop contract " + string(kind),
	}
}

// start starts a session and closes it when the scenario ends.
func (c *contract) start(t *testing.T, spec StartSpec) Session {
	t.Helper()
	s, err := c.env.Host.Start(c.ctx, spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = c.env.Host.Close(context.Background(), s.ID) })
	return s
}

func (c *contract) startDefault(t *testing.T, kind Kind) (Session, string) {
	t.Helper()
	path := c.checkout(t)
	return c.start(t, c.spec(kind, path, c.prompt(t, c.env.Prompt))), path
}

// eventually retries cond until it holds or the env's Wait passes.
func (c *contract) eventually(cond func() bool) bool {
	deadline := time.Now().Add(c.env.Wait)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}

func need[T any](t *testing.T, driver any) T {
	t.Helper()
	v, ok := driver.(T)
	if !ok {
		var zero T
		t.Skipf("driver has no %T capability", &zero)
	}
	return v
}

// staleOf names a handle the host never issued, on the session's host.
func staleOf(id RunID) RunID {
	return RunID{Host: id.Host, Terminal: "term_00000000-0000-0000-0000-000000000000", Incarnation: id.Incarnation}
}

func scStart(t *testing.T, c *contract) {
	s, path := c.startDefault(t, KindStage)
	if !s.ID.Valid() {
		t.Fatalf("session id %+v is not valid", s.ID)
	}
	if s.Checkout != path || s.Harness != c.env.Harness || s.Model != c.env.Model {
		t.Fatalf("session %+v does not echo the request", s)
	}
	var inv Inventory
	ok := c.eventually(func() bool {
		var err error
		inv, err = c.env.Host.Inventory(c.ctx)
		return err == nil && inv.Complete && inv.Lookup(s) == Present
	})
	if !ok {
		t.Fatalf("inventory never listed %s: complete=%v why=%q", s.ID, inv.Complete, inv.Why)
	}
	st, err := c.env.Host.Settle(c.ctx, s.ID)
	if err != nil || st.Liveness != Live {
		t.Fatalf("Settle live session = %+v, %v; want live", st, err)
	}
}

func scRunID(t *testing.T, c *contract) {
	s, _ := c.startDefault(t, KindStage)
	owner := s.ID.String()
	back, err := ParseRunID(owner)
	if err != nil || back != s.ID {
		t.Fatalf("ParseRunID(%q) = %+v, %v; want %+v", owner, back, err, s.ID)
	}
	if s.ID.String() != owner {
		t.Fatal("RunID.String is not stable")
	}
}

func scArgv(t *testing.T, c *contract) {
	reader := need[ArgvReader](t, c.env.Driver)
	marker := "loop-contract-marker-" + strings.ReplaceAll(t.Name(), "/", "-") +
		"-" + time.Now().Format("150405.000000000")
	path := c.checkout(t)
	file := c.prompt(t, c.env.Prompt+"\nThe marker is "+marker+". It's \"quoted\" and `ticked`; $HOME stays.\n")
	c.start(t, c.spec(KindStage, path, file))
	args, err := reader.ProcessArgs()
	if err != nil {
		t.Fatalf("ProcessArgs: %v", err)
	}
	named := false
	for _, argv := range args {
		for _, a := range argv {
			if strings.Contains(a, marker) {
				t.Fatalf("prompt text reached a process argument: %q", argv)
			}
			named = named || strings.Contains(a, file)
		}
	}
	if !named {
		t.Fatalf("no process argument names the prompt file %s", file)
	}
}

func scIncomplete(t *testing.T, c *contract) {
	// The rule holds for every inventory value, whatever the host.
	ghost := Session{ID: RunID{Host: "h", Terminal: "t", Incarnation: "i"}, Epoch: "e"}
	if got := (Inventory{Complete: false, Epoch: "e"}).Lookup(ghost); got == Absent {
		t.Fatal("an incomplete inventory reported a run absent")
	}
	if got := (Inventory{Complete: true, Epoch: "e2"}).Lookup(ghost); got == Absent {
		t.Fatal("an inventory of another epoch reported a run absent")
	}
	breaker := need[InventoryBreaker](t, c.env.Driver)
	s, _ := c.startDefault(t, KindStage)
	if err := c.env.Host.Close(c.ctx, s.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	breaker.SetInventoryIncomplete(true)
	t.Cleanup(func() { breaker.SetInventoryIncomplete(false) })
	inv, _ := c.env.Host.Inventory(c.ctx)
	if inv.Complete {
		t.Fatal("broken inventory reads complete")
	}
	if got := inv.Lookup(s); got == Absent {
		t.Fatalf("incomplete inventory reported %s absent", s.ID)
	}
}

func scStale(t *testing.T, c *contract) {
	s, _ := c.startDefault(t, KindStage)
	st, err := c.env.Host.Settle(c.ctx, staleOf(s.ID))
	if st.Liveness != Unknown || err == nil {
		t.Fatalf("Settle stale handle = %+v, %v; want unknown and an error", st, err)
	}
	staler, ok := c.env.Driver.(Staler)
	if !ok {
		return
	}
	staler.MakeStale(s.ID)
	st, err = c.env.Host.Settle(c.ctx, s.ID)
	if st.Liveness != Unknown || !errors.Is(err, ErrStaleHandle) {
		t.Fatalf("Settle after MakeStale = %+v, %v; want unknown, ErrStaleHandle", st, err)
	}
}

func scNotReady(t *testing.T, c *contract) {
	breaker := need[ReadinessBreaker](t, c.env.Driver)
	breaker.SetNeverReady(true)
	t.Cleanup(func() { breaker.SetNeverReady(false) })
	path := c.checkout(t)
	s, err := c.env.Host.Start(c.ctx, c.spec(KindStage, path, c.prompt(t, c.env.Prompt)))
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Start of a never-ready agent = %v; want ErrNotReady", err)
	}
	if s.ID.Valid() {
		t.Cleanup(func() { _ = c.env.Host.Close(context.Background(), s.ID) })
	}
}

func scEndedQuiet(t *testing.T, c *contract) {
	turns := need[TurnDriver](t, c.env.Driver)
	clock := need[Advancer](t, c.env.Driver)
	s, path := c.startDefault(t, KindStage)
	if r, err := c.env.Host.Ended(c.ctx, s); err != nil || r.Ended {
		t.Fatalf("Ended of a working session = %+v, %v; want not ended", r, err)
	}
	turns.EndTurn(s.ID)
	clock.Advance(DefaultQuietPeriod - time.Minute)
	if r, err := c.env.Host.Ended(c.ctx, s); err != nil || r.Ended {
		t.Fatalf("Ended inside the quiet period = %+v, %v; want not ended", r, err)
	}
	clock.Advance(2 * time.Minute)
	r, err := c.env.Host.Ended(c.ctx, s)
	if err != nil || !r.Ended || r.Cause != EndQuiet {
		t.Fatalf("Ended after the quiet period = %+v, %v; want ended by quiet", r, err)
	}
	if err := c.env.Host.Close(c.ctx, s.ID); err != nil {
		t.Fatalf("Close ended session: %v", err)
	}
	c.assertCheckout(t, path)
}

func scToolRunning(t *testing.T, c *contract) {
	turns := need[TurnDriver](t, c.env.Driver)
	clock := need[Advancer](t, c.env.Driver)
	s, _ := c.startDefault(t, KindStage)
	turns.StartTool(s.ID)
	clock.Advance(3 * DefaultQuietPeriod)
	r, err := c.env.Host.Ended(c.ctx, s)
	if err != nil || r.Ended {
		t.Fatalf("Ended with a tool call running = %+v, %v; want not ended", r, err)
	}
}

func scEndedReal(t *testing.T, c *contract) {
	if _, ok := c.env.Driver.(TurnDriver); ok || c.env.EndedWithin == 0 {
		t.Skip("covered by the driven quiet scenario, or no EndedWithin")
	}
	s, path := c.startDefault(t, KindStage)
	deadline := time.Now().Add(c.env.EndedWithin)
	var r EndReport
	var err error
	for time.Now().Before(deadline) {
		r, err = c.env.Host.Ended(c.ctx, s)
		if err == nil && r.Ended {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if err != nil || !r.Ended {
		t.Fatalf("real session never ended: %+v, %v", r, err)
	}
	if err := c.env.Host.Close(c.ctx, s.ID); err != nil {
		t.Fatalf("Close ended session: %v", err)
	}
	c.assertCheckout(t, path)
}

func (c *contract) assertCheckout(t *testing.T, path string) {
	t.Helper()
	if obs, ok := c.env.Driver.(CheckoutObserver); ok {
		if !obs.CheckoutExists(path) {
			t.Fatalf("closing the session removed checkout %s", path)
		}
		return
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("closing the session removed checkout %s: %v", path, err)
	}
}

func scCloseKeeps(t *testing.T, c *contract) {
	s, path := c.startDefault(t, KindRoom)
	if err := c.env.Host.Close(c.ctx, s.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	c.assertCheckout(t, path)
	st, _ := c.env.Host.Settle(c.ctx, s.ID)
	if st.Liveness == Live {
		t.Fatal("a closed session still reads live")
	}
}

func scDroppedModel(t *testing.T, c *contract) {
	dropper := need[ModelDropper](t, c.env.Driver)
	dropper.SetDropModel(true)
	t.Cleanup(func() { dropper.SetDropModel(false) })
	s, path := c.startDefault(t, KindStage)
	rc, err := c.env.Host.ModelReceipt(c.ctx, s)
	if err != nil || rc.Status != Unverified {
		t.Fatalf("receipt of a dropped model = %+v, %v; want unverified", rc, err)
	}
	spec := c.spec(KindStage, path, c.prompt(t, c.env.Prompt))
	spec.RequireVerifiedModel = true
	esc, err := c.env.Host.Start(c.ctx, spec)
	if !errors.Is(err, ErrModelUnverified) {
		t.Fatalf("escalated start with a dropped model = %v; want ErrModelUnverified", err)
	}
	if esc.ID.Valid() {
		st, _ := c.env.Host.Settle(c.ctx, esc.ID)
		if st.Liveness == Live {
			t.Fatal("an escalated role with an unverified model is still live")
		}
	}
}

func scVerified(t *testing.T, c *contract) {
	s, _ := c.startDefault(t, KindStage)
	var rc ModelReceipt
	ok := c.eventually(func() bool {
		var err error
		rc, err = c.env.Host.ModelReceipt(c.ctx, s)
		return err == nil && rc.Status == Verified
	})
	if !ok {
		t.Fatalf("receipt of the requested model = %+v; want verified", rc)
	}
	if rc.Requested != c.env.Model {
		t.Fatalf("receipt requested %q; want %q", rc.Requested, c.env.Model)
	}
}

func scNotify(t *testing.T, c *contract) {
	s, _ := c.startDefault(t, KindCoordinator)
	if turns, ok := c.env.Driver.(TurnDriver); ok {
		turns.EndTurn(s.ID)
	}
	text := "loop contract doorbell: read the inbox."
	ok := c.eventually(func() bool { return c.env.Host.Notify(c.ctx, s.ID, text) == nil })
	if !ok {
		t.Fatalf("Notify of an idle coordinator: %v", c.env.Host.Notify(c.ctx, s.ID, text))
	}
	if obs, ok := c.env.Driver.(NotifyObserver); ok {
		got := obs.Notifications(s.ID)
		if len(got) == 0 || got[len(got)-1] != text {
			t.Fatalf("coordinator received %q; want %q", got, text)
		}
	}
	if err := c.env.Host.Notify(c.ctx, staleOf(s.ID), text); err == nil {
		t.Fatal("Notify of a stale handle returned success")
	}
}

func scNotifyRefused(t *testing.T, c *contract) {
	refuser := need[NotifyRefuser](t, c.env.Driver)
	s, _ := c.startDefault(t, KindCoordinator)
	refuser.RefuseNotify(s.ID, true)
	err := c.env.Host.Notify(c.ctx, s.ID, "doorbell")
	if !errors.Is(err, ErrNotifyRefused) {
		t.Fatalf("refused Notify = %v; want ErrNotifyRefused", err)
	}
}

func scStop(t *testing.T, c *contract) {
	s, _ := c.startDefault(t, KindStage)
	st, err := c.env.Host.Stop(c.ctx, staleOf(s.ID))
	if st.Liveness == Exited || !errors.Is(err, ErrExitUnproven) {
		t.Fatalf("Stop of a stale handle = %+v, %v; want no exit and ErrExitUnproven", st, err)
	}
	st, err = c.env.Host.Stop(c.ctx, s.ID)
	if err != nil || st.Liveness != Exited {
		t.Fatalf("Stop of a live session = %+v, %v; want exited", st, err)
	}
}

func scOutsideRoot(t *testing.T, c *contract) {
	if c.env.OutsideRoot == "" {
		t.Skip("env names no path outside the root")
	}
	if err := c.env.Host.RemoveCheckout(c.ctx, c.env.OutsideRoot); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("RemoveCheckout outside root = %v; want ErrOutsideRoot", err)
	}
	escape := filepath.Join(c.env.OutsideRoot, "..", filepath.Base(c.env.OutsideRoot))
	spec := c.spec(KindStage, escape, c.prompt(t, c.env.Prompt))
	if _, err := c.env.Host.Start(c.ctx, spec); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("Start outside root = %v; want ErrOutsideRoot", err)
	}
}
