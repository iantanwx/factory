package config

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/iantanwx/loop/internal/store"
)

// Project registration errors. Callers test them with errors.Is.
var (
	ErrProjectExists     = errors.New("config: project already registered")
	ErrProjectNotFound   = errors.New("config: project not registered")
	ErrProjectHasLiveRun = errors.New("config: project has a live run")
)

// Project is one registration in host config (R1). A project is exactly one
// git repository. No field names an organisation (R2): Repo's owner is part
// of the repository's name and nothing else.
type Project struct {
	ID   string `yaml:"id"`
	Repo string `yaml:"repo"` // owner/name on GitHub
	Path string `yaml:"path"` // the project's local directory on this host
}

// repoRe is owner/name. A URL, a host name or a third segment fails it.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)

func (p Project) validate(ck *checker, field string) {
	if !validIdent(p.ID) {
		ck.fail(field+".id", "must start with a lower-case letter and hold only a-z, 0-9, _ and -")
	}
	if !repoRe.MatchString(p.Repo) || strings.HasSuffix(p.Repo, ".git") {
		ck.fail(field+".repo", "must be owner/name, with no host and no .git suffix")
	}
	if !filepath.IsAbs(p.Path) || filepath.Clean(p.Path) != p.Path {
		ck.fail(field+".path", "must be a clean absolute path")
	}
}

// validateRegistrations refuses a second registration of one id, one
// repository or one local path.
func validateRegistrations(ck *checker, ps []Project) {
	ids, repos, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, p := range ps {
		f := fmt.Sprintf("projects[%d]", i)
		p.validate(ck, f)
		repo := strings.ToLower(p.Repo)
		switch {
		case ids[p.ID]:
			ck.fail(f+".id", "registers a project id twice")
		case repos[repo]:
			ck.fail(f+".repo", "registers a repository twice")
		case paths[p.Path]:
			ck.fail(f+".path", "registers a local path twice")
		}
		ids[p.ID], repos[repo], paths[p.Path] = true, true, true
	}
}

// AddHook prepares one part of a new project, for example its beads
// database (U3), its bare mirror and branch-rules check (U10) or its merge
// mechanism check (U11). Projects.Add runs every hook before it saves the
// registration, and it saves nothing if a hook fails. A hook must be safe to
// run again, because an operator repeats a failed add.
type AddHook func(ctx context.Context, p Project) error

// RemoveHook releases one part of a removed project. Projects.Remove runs
// every hook before it deletes the registration. A hook must be safe to run
// again.
type RemoveHook func(ctx context.Context, p Project) error

// LiveRunCheck reports whether a project has a live run.
type LiveRunCheck func(ctx context.Context, projectID string) (bool, error)

// Projects edits the project registrations in host config. Its lock orders
// edits inside one process. The server is the only writer of host config.
type Projects struct {
	HostPath    string
	AddHooks    []AddHook
	RemoveHooks []RemoveHook
	mu          sync.Mutex
}

// Add registers p. It refuses a second registration of the same id,
// repository or local path, and it runs every AddHook before it saves.
func (ps *Projects) Add(ctx context.Context, p Project) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	cfg, err := LoadHost(ps.HostPath)
	if err != nil {
		return err
	}
	ck := &checker{source: "project add"}
	p.validate(ck, "project")
	if err := ck.err(); err != nil {
		return err
	}
	for _, q := range cfg.Projects {
		switch {
		case q.ID == p.ID:
			return fmt.Errorf("%w: id %s", ErrProjectExists, p.ID)
		case strings.EqualFold(q.Repo, p.Repo):
			return fmt.Errorf("%w: repository %s is project %s", ErrProjectExists, p.Repo, q.ID)
		case q.Path == p.Path:
			return fmt.Errorf("%w: path %s belongs to project %s", ErrProjectExists, p.Path, q.ID)
		}
	}
	for _, hook := range ps.AddHooks {
		if err := hook(ctx, p); err != nil {
			return fmt.Errorf("config: project add %s: %w", p.ID, err)
		}
	}
	cfg.Projects = append(cfg.Projects, p)
	return SaveHost(ps.HostPath, cfg)
}

// List returns every registration in host config order.
func (ps *Projects) List() ([]Project, error) {
	cfg, err := LoadHost(ps.HostPath)
	if err != nil {
		return nil, err
	}
	return cfg.Projects, nil
}

// Show returns the registration with id.
func (ps *Projects) Show(id string) (Project, error) {
	cfg, err := LoadHost(ps.HostPath)
	if err != nil {
		return Project{}, err
	}
	for _, p := range cfg.Projects {
		if p.ID == id {
			return p, nil
		}
	}
	return Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, id)
}

// Remove deletes the registration with id. It refuses while live reports a
// live run, and it refuses when the caller supplies no check.
func (ps *Projects) Remove(ctx context.Context, id string, live LiveRunCheck) error {
	if live == nil {
		return errors.New("config: project remove needs a live-run check")
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	cfg, err := LoadHost(ps.HostPath)
	if err != nil {
		return err
	}
	idx := -1
	for i, p := range cfg.Projects {
		if p.ID == id {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, id)
	}
	busy, err := live(ctx, id)
	if err != nil {
		return fmt.Errorf("config: project remove %s: check live runs: %w", id, err)
	}
	if busy {
		return fmt.Errorf("%w: %s", ErrProjectHasLiveRun, id)
	}
	p := cfg.Projects[idx]
	for _, hook := range ps.RemoveHooks {
		if err := hook(ctx, p); err != nil {
			return fmt.Errorf("config: project remove %s: %w", id, err)
		}
	}
	cfg.Projects = append(cfg.Projects[:idx:idx], cfg.Projects[idx+1:]...)
	return SaveHost(ps.HostPath, cfg)
}

// StoreProjectRow returns the AddHook that creates the project's row in the
// store. The secrets, runs and last-known-good config rows reference it. A
// removed project keeps its row as history, so the hook refuses an id whose
// row names another repository: that project would inherit the old secrets.
func StoreProjectRow(st *store.Store) AddHook {
	return func(ctx context.Context, p Project) error {
		return st.Write(ctx, func(tx *store.Tx) error {
			var repo string
			err := tx.QueryRow(`SELECT repo FROM projects WHERE id = ?`, p.ID).Scan(&repo)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				now := tx.Now().UnixMilli()
				_, err = tx.Exec(`INSERT INTO projects (id, repo, created_at, updated_at) VALUES (?, ?, ?, ?)`,
					p.ID, p.Repo, now, now)
				return err
			case err != nil:
				return err
			case !strings.EqualFold(repo, p.Repo):
				return fmt.Errorf("%w: the store holds id %s for repository %s", ErrProjectExists, p.ID, repo)
			}
			return nil
		})
	}
}
