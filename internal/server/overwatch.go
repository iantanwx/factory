package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/beadsx"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/inbox"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/overwatch"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/store"
)

// overwatchState is the sweep's world and part list. The list is
// overwatch.Parts() outside tests, and a test replaces it.
type overwatchState struct {
	world overwatch.World
	mu    sync.Mutex
	parts []overwatch.Part
	// install and github are the checks World.Doctor reads. A Setup step
	// sets them, before the job ticks or the verb answers.
	install func(ctx context.Context) github.CheckLine
	github  *GitHub
	reads   overwatch.GitHubReads // nil until the GitHub setup runs
}

// composeOverwatch builds the production World, serves `overwatch sweep`,
// and registers the job overwatch on a server with the scheduler (ADR
// 0009).
func (s *Server) composeOverwatch() error {
	s.overwatch.world = &serverWorld{s: s, now: s.humanClock()}
	s.setOverwatchParts(overwatch.Parts())
	if err := s.RPC.Handle("overwatch sweep", s.overwatchSweep); err != nil {
		return err
	}
	if s.Sched == nil {
		return nil
	}
	return s.Sched.RegisterInstanceJob(overwatch.JobName, func(ctx context.Context) error {
		return s.overwatchJob().Tick(ctx)
	})
}

// SetOverwatchDoctor gives the server part its checks: install, the
// install drift line of `loop doctor`, and gh, the GitHub actor, nil when
// the host has no App. internal/release imports this package, so `loop
// serve` builds the install check and calls this in a Setup step.
func (s *Server) SetOverwatchDoctor(install func(ctx context.Context) github.CheckLine, gh *GitHub) {
	s.overwatch.install, s.overwatch.github = install, gh
}

func (s *Server) setOverwatchParts(parts []overwatch.Part) {
	s.overwatch.mu.Lock()
	defer s.overwatch.mu.Unlock()
	s.overwatch.parts = parts
}

// setOverwatchGitHub gives the world the GitHub service, once the GitHub
// setup has built it.
func (s *Server) setOverwatchGitHub(svc *github.Service) {
	s.overwatch.mu.Lock()
	defer s.overwatch.mu.Unlock()
	s.overwatch.reads = githubReads{svc}
}

func (s *Server) overwatchPartList() []overwatch.Part {
	s.overwatch.mu.Lock()
	defer s.overwatch.mu.Unlock()
	return s.overwatch.parts
}

// overwatchJob is the job over the server's world and part list. It rings
// through recordedRing("overwatch").
func (s *Server) overwatchJob() *overwatch.Job {
	return &overwatch.Job{World: s.overwatch.world, Parts: s.overwatchPartList(), Ring: s.recordedRing(overwatch.SourceOverwatch)}
}

// overwatchSweep is `loop overwatch sweep`: a fresh sweep of the job's world
// and parts. It exits 0 with the report as data, the sweep's code in
// data.code, and it never rings.
func (s *Server) overwatchSweep(ctx context.Context, _ *rpc.Call) cli.Result {
	return cli.Found(overwatch.Sweep(ctx, s.overwatch.world, s.overwatchPartList()))
}

// humanClock is the clock of the inbox, the decision timers and the ring
// record: HumanOptions.Now, then Sched.Now, then time.Now.
func (s *Server) humanClock() func() time.Time {
	if now := s.opts.Human.Now; now != nil {
		return now
	}
	if now := s.opts.Sched.Now; now != nil {
		return now
	}
	return time.Now
}

// recordedRing is the doorbell of one source. It rings the stored
// coordinator session and writes the outcome with overwatch.RecordRing. A
// failed row write is logged, and the ring's own result stands.
//
// After overwatch.RefusalLimit refused rings in a row to one session, both
// doorbells share one backoff and one open alert that names the session
// (F-32). Each ring the backoff holds rings no one and writes no row. A new
// stored session, or a hold that passed, rings again, and an accepted ring
// closes the alert.
func (s *Server) recordedRing(source string) inbox.Doorbell {
	now := s.humanClock()
	return func(ctx context.Context, text string) error {
		bg := context.WithoutCancel(ctx)
		last, _, err := overwatch.LastRing(ctx, s.Store)
		if err != nil {
			s.log.WarnContext(ctx, "read the coordinator ring", "source", source, "err", err)
		}
		id, ok := s.coordinatorSession()
		handle := ""
		if ok {
			handle = id.String()
		}
		at := now()
		if last.Held(handle, at) {
			return inbox.ErrRingHeld
		}
		err = inbox.ErrNobodyToRing
		if ok {
			err = s.opts.Agents.Notify(ctx, id, text)
		}
		o := overwatch.RingOutcome{At: at, Source: source, Result: overwatch.RingAccepted, Handle: handle, Alerted: last.Alerted}
		switch {
		case errors.Is(err, inbox.ErrNobodyToRing):
			o.Result, o.Handle = overwatch.RingNobody, ""
		case err != nil:
			o.Result, o.Detail, o.Refusals = overwatch.RingRefused, err.Error(), 1
			if s.Redactor != nil {
				o.Detail = s.Redactor.Redact(o.Detail)
			}
			if last.Result == overwatch.RingRefused && last.Handle == handle {
				o.Refusals = last.Refusals + 1
			}
		}
		switch {
		case o.Result == overwatch.RingAccepted && o.Alerted != "":
			if s.clearRingAlert(bg, o.Alerted) {
				o.Alerted = ""
			}
		case o.Result == overwatch.RingRefused && o.Refusals >= overwatch.RefusalLimit:
			// The alert of a session the server no longer stores is moot.
			if o.Alerted != "" && o.Alerted != handle && !s.clearRingAlert(bg, o.Alerted) {
				break
			}
			if s.raiseRingAlert(bg, id, o) {
				o.Alerted = handle
			}
		}
		if werr := overwatch.RecordRing(bg, s.Store, o); werr != nil {
			s.log.WarnContext(ctx, "record the coordinator ring", "source", source, "result", o.Result, "err", werr)
		}
		return err
	}
}

// raiseRingAlert raises, or counts up, the one open alert of a session
// that refused o.Refusals rings in a row. It names the session's terminal
// and quotes the last refusal. ok is false when the write failed.
func (s *Server) raiseRingAlert(ctx context.Context, id agenthost.RunID, o overwatch.RingOutcome) bool {
	a := config.Alert{Kind: overwatch.AlertRingRefused, DedupeKey: overwatch.RingAlertKey(o.Handle),
		Subject: fmt.Sprintf("the coordinator session in terminal %s refused %d rings in a row", id.Terminal, o.Refusals),
		Body: fmt.Sprintf("The agent host refused the last %d rings to the stored coordinator session %s (terminal %s). "+
			"The last refusal: %q. The server rings that session again after %s, and backs off up to %s each time, "+
			"until the stored session changes or a ring is accepted. An accepted ring closes this alert.",
			o.Refusals, id, id.Terminal, o.Detail, o.Hold(), overwatch.HoldMax)}
	if err := config.StoreAlerter(s.Store).Raise(ctx, a); err != nil {
		s.log.WarnContext(ctx, "raise the refused ring alert", "session", id.String(), "err", err)
		return false
	}
	return true
}

// clearRingAlert closes the refused ring alert of handle. ok is false when
// the write failed, so the next ring tries again.
func (s *Server) clearRingAlert(ctx context.Context, handle string) bool {
	if err := config.StoreAlerter(s.Store).Clear(ctx, overwatch.RingAlertKey(handle)); err != nil {
		s.log.WarnContext(ctx, "clear the refused ring alert", "session", handle, "err", err)
		return false
	}
	return true
}

// errNoPartReads is the answer of a World read that no landed part makes
// yet. The part entry that first reads it builds it (spec, Work 5 to 10).
var errNoPartReads = errors.New("overwatch: no part reads this yet")

// serverWorld is the production World: the reads the server already holds.
type serverWorld struct {
	s   *Server
	now func() time.Time
}

var _ overwatch.World = (*serverWorld)(nil)

func (w *serverWorld) Now() time.Time { return w.now() }

func (w *serverWorld) Host(context.Context) (*config.HostConfig, error) {
	return config.LoadHost(w.s.HostPath)
}

func (w *serverWorld) Projects(context.Context) ([]config.Project, error) { return w.s.Projects.List() }

func (w *serverWorld) ProjectConfig(ctx context.Context, project string) (*config.ProjectSnapshot, error) {
	return w.s.projectSource()(ctx, project)
}

func (w *serverWorld) Items(ctx context.Context, project string, f machine.ListFilter) ([]machine.Item, error) {
	if w.s.Machine == nil {
		return nil, errors.New("overwatch: the server holds no state machine")
	}
	items, ref := w.s.Machine.List(ctx, project, f)
	if ref != nil {
		return nil, fmt.Errorf("list the items of %s: %s: %s", project, ref.Code, ref.Hint)
	}
	return items, nil
}

func (w *serverWorld) Body(ctx context.Context, project, item string) (string, error) {
	if w.s.Machine == nil {
		return "", errors.New("overwatch: the server holds no state machine")
	}
	it, ref := w.s.Machine.Read(ctx, project, item)
	if ref != nil {
		return "", fmt.Errorf("read %s: %s: %s", item, ref.Code, ref.Hint)
	}
	return it.Body, nil
}

// Dependencies reads the item's beads edges of every kind, each with its
// status, from the project's beads client.
func (w *serverWorld) Dependencies(ctx context.Context, project, item string) ([]beadsx.Dependency, error) {
	if w.s.opts.Beads == nil {
		return nil, errors.New("overwatch: the server holds no beads")
	}
	bc, err := w.s.opts.Beads(project)
	if err != nil {
		return nil, fmt.Errorf("beads of %s: %w", project, err)
	}
	return bc.Dependencies(ctx, item)
}

func (w *serverWorld) Store() *store.Store { return w.s.Store }

// Intervals belongs to the scheduler parts (spec, Work 6). Until then it is
// zero.
func (w *serverWorld) Intervals() overwatch.Intervals { return overwatch.Intervals{} }

// CheckoutDirs belongs to the worktrees part (spec, Work 6).
func (w *serverWorld) CheckoutDirs(context.Context) ([]string, error) { return nil, errNoPartReads }

func (w *serverWorld) Inventory(ctx context.Context) (agenthost.Inventory, error) {
	if w.s.opts.Agents == nil {
		return agenthost.Inventory{}, errors.New("overwatch: the server holds no agent host")
	}
	return w.s.opts.Agents.Inventory(ctx)
}

func (w *serverWorld) Coordinator() sched.CoordinatorState {
	if w.s.Sched == nil {
		return sched.CoordinatorState{}
	}
	return w.s.Sched.Coordinator()
}

// GitHub is the GitHub service's reads, or nil on a server with no GitHub
// App.
func (w *serverWorld) GitHub() overwatch.GitHubReads {
	w.s.overwatch.mu.Lock()
	defer w.s.overwatch.mu.Unlock()
	return w.s.overwatch.reads
}

// Doctor is the install drift line, then each project's GitHub doctor
// lines, each check named <project>/<check> as loop doctor names it, so
// one project's finding key differs from another's. A check that cannot
// run is a failed line, never a pass.
func (w *serverWorld) Doctor(ctx context.Context) ([]github.CheckLine, error) {
	line := github.CheckLine{Check: "install", Detail: "this server was started with no install check; run it with loop serve"}
	if install := w.s.overwatch.install; install != nil {
		line = install(ctx)
	}
	lines := []github.CheckLine{line}
	ps, err := w.s.Projects.List()
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return lines, nil
	}
	gh := w.s.overwatch.github
	if gh == nil {
		return append(lines, github.CheckLine{Check: "github", Detail: "the server holds no GitHub actor; set credentials.github_app in host config"}), nil
	}
	docs, err := gh.Doctor(ctx, w.s.Projects)
	if err != nil {
		return append(lines, github.CheckLine{Check: "github", Detail: "the server's GitHub checks could not run: " + err.Error()}), nil
	}
	for _, d := range docs {
		for _, l := range d.Lines {
			l.Check = d.Project + "/" + l.Check
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func (w *serverWorld) LastRing(ctx context.Context) (overwatch.RingOutcome, bool, error) {
	return overwatch.LastRing(ctx, w.s.Store)
}

// githubReads are the GET reads of the GitHub parts, over the service.
type githubReads struct{ svc *github.Service }

func (r githubReads) OpenPulls(ctx context.Context, p config.Project) ([]github.PR, error) {
	return r.svc.OpenPulls(ctx, p.ID)
}

func (r githubReads) WorkflowRuns(ctx context.Context, p config.Project, q github.RunQuery) ([]github.RunView, error) {
	return r.svc.WorkflowRuns(ctx, p, q)
}
