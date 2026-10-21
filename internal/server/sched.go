package server

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/sched"
)

// SchedOptions holds the scheduler's optional parts (U8). The server
// composes the scheduler when Options.Agents is set, and beads and
// admission are composed.
type SchedOptions struct {
	// Checkouts returns one project's checkout host. Options.Agents when
	// nil.
	Checkouts func(project string) sched.CheckoutHost
	// Refs returns where a project's committed config lives: a reader and
	// the default branch. LocalRefs over the project's own directory when
	// nil. The GitHub mirror (U10) is the production source.
	Refs RefSource
	// ProjectConfig replaces the config loader over Refs. Optional.
	ProjectConfig sched.ProjectSource
	Pack          brief.Pack
	Coordinator   *sched.CoordinatorConfig
	Now           func() time.Time
	TickInterval  time.Duration
	NewRunID      func() string
}

// RefSource returns a reader of a project's committed files and its
// default branch.
type RefSource func(ctx context.Context, p config.Project) (config.RefReader, string, error)

// composeSched builds the scheduler and registers run finish and drain.
func (s *Server) composeSched() error {
	o := s.opts.Sched
	src := o.ProjectConfig
	if src == nil {
		refs := o.Refs
		if refs == nil {
			refs = LocalRefs
		}
		src = s.projectLoader(refs)
	}
	sc, err := sched.New(sched.Config{
		Store: s.Store, Host: &config.HostLoader{Path: s.HostPath, Alerts: config.StoreAlerter(s.Store)},
		Projects: s.Projects.List, ProjectConfig: src,
		Machine: s.Machine, Admission: s.Admission, Beads: s.opts.Beads, Secrets: s.Secrets, Tokens: s.Tokens,
		Agents: s.opts.Agents, Checkouts: o.Checkouts, Pack: o.Pack, Alerts: config.StoreAlerter(s.Store),
		Release: cli.Version, Coordinator: o.Coordinator, Now: o.Now, TickInterval: o.TickInterval,
		Logger: s.log, NewRunID: o.NewRunID,
	})
	if err != nil {
		return err
	}
	if err := sc.Register(s.RPC); err != nil {
		return err
	}
	s.Sched = sc
	return nil
}

// projectConfigTTL bounds how long one read of a project's config serves.
// The scheduler reads it on every tick for every stage and live run.
const projectConfigTTL = 30 * time.Second

type cachedSnap struct {
	snap *config.ProjectSnapshot
	at   time.Time
}

// projectLoader reads a project's config and manifest from its default
// branch through config.Loader, so an invalid commit keeps the last known
// good config and raises an alert (KTD20).
func (s *Server) projectLoader(refs RefSource) sched.ProjectSource {
	loader := &config.Loader{LKG: config.StoreLKG(s.Store), Alerts: config.StoreAlerter(s.Store)}
	var mu sync.Mutex
	cache := map[string]cachedSnap{}
	return func(ctx context.Context, project string) (*config.ProjectSnapshot, error) {
		mu.Lock()
		c, ok := cache[project]
		mu.Unlock()
		if ok && time.Since(c.at) < projectConfigTTL {
			return c.snap, nil
		}
		p, err := s.Projects.Show(project)
		if err != nil {
			return nil, err
		}
		read, branch, err := refs(ctx, p)
		if err != nil {
			return nil, err
		}
		l, err := loader.Load(ctx, project, read, branch)
		if err != nil {
			return nil, err
		}
		snap := &l.ProjectSnapshot
		mu.Lock()
		cache[project] = cachedSnap{snap: snap, at: time.Now()}
		mu.Unlock()
		return snap, nil
	}
}

// LocalRefs reads a project's committed files from its own directory at
// origin/main, with no user or system git config. A server without the
// GitHub mirror uses it.
func LocalRefs(_ context.Context, p config.Project) (config.RefReader, string, error) {
	return func(ctx context.Context, ref, path string) ([]byte, error) {
		run := func(args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "git", append([]string{"-C", p.Path}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
			}
			return out, nil
		}
		if _, err := run("cat-file", "-e", ref+":"+path); err != nil {
			if _, rerr := run("rev-parse", "--verify", "--quiet", ref+"^{commit}"); rerr == nil {
				return nil, fmt.Errorf("%s holds no %s: %w", ref, path, fs.ErrNotExist)
			}
			return nil, err
		}
		return run("show", ref+":"+path)
	}, sched.DefaultBaseBranch, nil
}
