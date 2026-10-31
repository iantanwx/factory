package server

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/userspace"
)

// SchedOptions holds the scheduler's optional parts. The server
// composes the scheduler when Options.Agents is set, and beads and
// admission are composed.
type SchedOptions struct {
	// Checkouts returns one project's checkout host. Options.Agents when
	// nil.
	Checkouts func(project string) sched.CheckoutHost
	// Refs returns where a project's committed config lives: a reader and
	// the default branch. LocalRefs over the project's own directory when
	// nil. The GitHub mirror is the production source.
	Refs RefSource
	// ProjectConfig replaces the config loader over Refs. Optional.
	ProjectConfig sched.ProjectSource
	Pack          brief.Pack
	Coordinator   *sched.CoordinatorConfig
	Now           func() time.Time
	TickInterval  time.Duration
	NewRunID      func() string
	// BaseHead reads the remote head that each new checkout starts from.
	// sched.OriginBaseHead over the registered projects when nil. The
	// GitHub mirror is the production source.
	BaseHead sched.BaseHeadSource
	// SkillStore keeps each project's fetched userspace skill pins.
	SkillStore userspace.Store
	// FetchSkill fetches one userspace pin. When nil, the server fetches
	// no pin. `loop serve` builds it from release.SkillFetcher.
	FetchSkill userspace.Fetch
}

// RefSource returns the readers of a project's committed files and tree,
// and its default branch.
type RefSource func(ctx context.Context, p config.Project) (config.Refs, string, error)

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
		Machine: s.Machine, Admission: s.Admission, Beads: s.opts.Beads, Pulls: s.opts.Pulls, Secrets: s.Secrets, Tokens: s.Tokens,
		Agents: s.opts.Agents, Checkouts: o.Checkouts, BaseHead: s.baseHead(), Pack: o.Pack, Alerts: config.StoreAlerter(s.Store),
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

// baseHead is the source of the remote head each new checkout starts
// from: the option, or a fetch from each project clone's origin.
func (s *Server) baseHead() sched.BaseHeadSource {
	if s.opts.Sched.BaseHead != nil {
		return s.opts.Sched.BaseHead
	}
	return sched.OriginBaseHead(s.Projects.List)
}

// projectConfigTTL bounds how long one read of a project's config serves.
// The scheduler reads it on every tick for every stage and live run.
const projectConfigTTL = 30 * time.Second

type cachedSnap struct {
	snap *config.ProjectSnapshot
	at   time.Time
}

// configCaches holds the forget func of each project config loader.
type configCaches struct {
	mu      sync.Mutex
	forgets []func(project string)
}

func (c *configCaches) add(forget func(project string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.forgets = append(c.forgets, forget)
}

// forget drops project's cached config from every loader, so the next
// read loads the default branch again. A merged settings change calls it.
func (c *configCaches) forget(project string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range c.forgets {
		f(project)
	}
}

// projectLoader reads a project's config and manifest from its default
// branch through config.Loader, so an invalid commit keeps the last known
// good config and raises an alert (KTD20). After each load it hands the
// skill pins of the config in force to the syncer, which fetches in the
// background (ADR 0013 decision 2). Server.caches drops its cached copy
// of a project.
func (s *Server) projectLoader(refs RefSource) sched.ProjectSource {
	loader := &config.Loader{LKG: config.StoreLKG(s.Store), Alerts: config.StoreAlerter(s.Store),
		Check: config.StageCheck(&config.HostLoader{Path: s.HostPath})}
	var mu sync.Mutex
	cache := map[string]cachedSnap{}
	// gen counts each project's forgets, so a load that started before a
	// forget does not cache what it read.
	gen := map[string]int{}
	s.caches.add(func(project string) {
		mu.Lock()
		delete(cache, project)
		gen[project]++
		mu.Unlock()
	})
	return func(ctx context.Context, project string) (*config.ProjectSnapshot, error) {
		mu.Lock()
		c, ok := cache[project]
		g := gen[project]
		mu.Unlock()
		if ok && time.Since(c.at) < projectConfigTTL {
			return c.snap, nil
		}
		p, err := s.Projects.Show(project)
		if err != nil {
			return nil, err
		}
		r, branch, err := refs(ctx, p)
		if err != nil {
			return nil, err
		}
		l, err := loader.Load(ctx, project, r, branch)
		if err != nil {
			return nil, err
		}
		snap := &l.ProjectSnapshot
		if s.skills != nil {
			s.skills.Observe(ctx, project, snap.Config.Skills)
		}
		mu.Lock()
		if gen[project] == g {
			cache[project] = cachedSnap{snap: snap, at: time.Now()}
		}
		mu.Unlock()
		return snap, nil
	}
}

// LocalRefs reads a project's committed files and tree from its own
// directory at origin/<its registered default branch>, with no user or
// system git config. A server without the GitHub mirror uses it.
func LocalRefs(_ context.Context, p config.Project) (config.Refs, string, error) {
	read := func(ctx context.Context, ref, path string) ([]byte, error) {
		run := func(args ...string) ([]byte, error) { return gitx.Run(ctx, p.Path, args...) }
		if _, err := run("cat-file", "-e", ref+":"+path); err != nil {
			if _, rerr := run("rev-parse", "--verify", "--quiet", ref+"^{commit}"); rerr == nil {
				return nil, fmt.Errorf("%s holds no %s: %w", ref, path, fs.ErrNotExist)
			}
			return nil, err
		}
		return run("show", ref+":"+path)
	}
	tree := func(ctx context.Context, ref, dir string) ([]config.TreeEntry, error) {
		sha, err := gitx.Run(ctx, p.Path, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if err != nil {
			return nil, nil
		}
		out, err := gitx.Run(ctx, p.Path, "ls-tree", "--full-tree", "-z", strings.TrimSpace(string(sha)), "--", dir+"/")
		if err != nil {
			return nil, err
		}
		return config.ParseTree(out), nil
	}
	return config.Refs{Read: read, Tree: tree}, p.BaseBranch(), nil
}
