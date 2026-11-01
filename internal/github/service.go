package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/secrets"
	"github.com/iantanwx/loop/internal/store"
)

// Projects reads the project registrations. *config.Projects is one.
type Projects interface {
	List() ([]config.Project, error)
	Show(id string) (config.Project, error)
}

// Config configures a Service. The owner and the repository of each
// project come from its registration, never from a constant.
type Config struct {
	// Apps lists the loop Apps, one for each GitHub owner. The service
	// calls it when it first meets an owner. Nil means no App.
	Apps func() ([]AppKey, error)
	// API is GitHub's REST endpoint; DefaultAPI when empty.
	API string
	// GitURL is the explicit URL of a repository; GitHub's HTTPS URL when
	// nil. A test points it at a local server.
	GitURL func(repo string) string
	// Protocols are the git transports the server may use besides file;
	// https when empty.
	Protocols []string
	// MirrorRoot holds one bare mirror for each project.
	MirrorRoot string
	Store      *store.Store
	Redactor   *secrets.Redactor
	Projects   Projects
	// Machine moves items for the reactor and the PR verbs. Optional: a
	// server without beads has none.
	Machine   *machine.Machine
	HTTP      *http.Client
	GitBinary string
	Now       func() time.Time
	Logger    *slog.Logger
	// Sleep waits between read retries; a test makes it instant.
	Sleep func(ctx context.Context, d time.Duration) error
	// UserSkills reads the pin names of a project's last good config, for
	// the push scan (ADR 0013). Nil, or no last good config, means none.
	UserSkills func(ctx context.Context, project string) ([]string, error)
}

// Service is the server's GitHub actor: one App for each owner, one shared cooldown,
// one token cache and one mirror for each project.
type Service struct {
	cfg  Config
	api  *Client
	cool *Cooldown
	git  *Git
	log  *slog.Logger

	mu       sync.Mutex
	apps     map[string]*App // by lower-case owner
	installs map[string]int64
	tokens   map[string]cachedToken
	defaults map[string]string
	locks    map[string]*sync.Mutex

	// crash lets a test stop a write at a kill point. It returns true to
	// stop, as a crash would.
	crash func(point string) bool
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Projects == nil || cfg.MirrorRoot == "" {
		return nil, errors.New("github: a service needs a store, the projects and a mirror root")
	}
	if cfg.Redactor == nil {
		cfg.Redactor = secrets.NewRedactor()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.GitURL == nil {
		cfg.GitURL = func(repo string) string { return "https://github.com/" + repo + ".git" }
	}
	if len(cfg.Protocols) == 0 {
		cfg.Protocols = []string{"https"}
	}
	log := cfg.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	home := filepath.Join(cfg.MirrorRoot, ".home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	cool := NewCooldown(cfg.Now)
	return &Service{
		cfg:      cfg,
		api:      &Client{Base: cfg.API, HTTP: cfg.HTTP, Cooldown: cool, Redactor: cfg.Redactor, Sleep: cfg.Sleep},
		cool:     cool,
		git:      &Git{Binary: cfg.GitBinary, Home: home, Protocols: cfg.Protocols},
		log:      log,
		apps:     map[string]*App{},
		installs: map[string]int64{},
		tokens:   map[string]cachedToken{},
		defaults: map[string]string{},
		locks:    map[string]*sync.Mutex{},
	}, nil
}

// Cooldown is the rate-limit pause every caller shares.
func (s *Service) Cooldown() *Cooldown { return s.cool }

// project reads one registration.
func (s *Service) project(id string) (config.Project, error) {
	return s.cfg.Projects.Show(id)
}

// Mirror returns the project's bare mirror. It does not create it.
func (s *Service) Mirror(p config.Project) *Mirror {
	return &Mirror{git: s.git, Dir: filepath.Join(s.cfg.MirrorRoot, p.ID+".git"), URL: s.cfg.GitURL(p.Repo)}
}

// forget drops the cached installation and tokens of one project.
func (s *Service) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.installs, id)
	for key := range s.tokens {
		if strings.HasPrefix(key, id+"/") {
			delete(s.tokens, key)
		}
	}
}

// lock serialises the git work on one key, for example one branch.
func (s *Service) lock(key string) func() {
	s.mu.Lock()
	l, ok := s.locks[key]
	if !ok {
		l = &sync.Mutex{}
		s.locks[key] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func repoPath(p config.Project, rest string) string { return "/repos/" + p.Repo + rest }

// owner is the owner part of owner/name.
func owner(p config.Project) string {
	o, _, _ := strings.Cut(p.Repo, "/")
	return o
}

// DefaultBranch is the project's base branch: the default branch that
// `loop register` recorded, or for a registration that names none, the
// remote default branch, read once from GitHub and recorded in the mirror.
func (s *Service) DefaultBranch(ctx context.Context, p config.Project) (string, error) {
	if p.Branch != "" {
		return p.Branch, nil
	}
	s.mu.Lock()
	b, ok := s.defaults[p.ID]
	s.mu.Unlock()
	if ok {
		return b, nil
	}
	m := s.Mirror(p)
	if err := m.Init(ctx); err != nil {
		return "", err
	}
	if b = m.DefaultBranch(ctx); b == "" {
		var repo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := s.repoGet(ctx, p, ScopeRead, repoPath(p, ""), &repo); err != nil {
			return "", err
		}
		if repo.DefaultBranch == "" {
			return "", fmt.Errorf("github: %s names no default branch", p.Repo)
		}
		b = repo.DefaultBranch
		if err := m.SetDefaultBranch(ctx, b); err != nil {
			return "", err
		}
	}
	s.mu.Lock()
	s.defaults[p.ID] = b
	s.mu.Unlock()
	return b, nil
}

// BranchFor is the branch an item's work lives on: a ticket's, and an
// epic's PRD. The reactor finds an item's pull request by this head branch
// (KTD6). An epic's spec lives on SpecBranchFor; BranchOf picks one.
func BranchFor(item string) string { return "loop/" + item }

// specSuffix ends the branch of an epic's spec.
const specSuffix = "-spec"

// SpecBranchFor is the branch of an epic's spec. The spec has its own
// branch because the PRD's branch keeps the PRD commits after a squash
// merge, and a spec pushed onto it would find the remote ahead.
func SpecBranchFor(epic string) string { return BranchFor(epic) + specSuffix }

// ADRBranchFor is the branch of an ADR that an epic with a PRD or a landed
// ADR writes in the ADR stage. Its spec pull request stays open on
// SpecBranchFor meanwhile, so the ADR needs a branch of its own.
func ADRBranchFor(epic string) string { return BranchFor(epic) + "-adr" }

// designSuffix ends the branch of an epic's design record.
const designSuffix = "-design"

// DesignBranchFor is the branch of an epic's design record. The spec
// branch opens only after the design merges, and a PRD that merged by
// squash leaves the PRD's branch behind the base.
func DesignBranchFor(epic string) string { return BranchFor(epic) + designSuffix }

// BranchOf is the branch that holds an item's current work. An epic whose
// PRD merged, or whose ADR landed, writes its spec on SpecBranchFor, its
// ADR on ADRBranchFor while it is in architecture or adr_review, and its
// design record on DesignBranchFor while it is in a design state. Every
// other item, and an epic before that, uses BranchFor: an epic created
// for an ADR writes its first one there.
func BranchOf(it machine.Item) string {
	if it.Kind != machine.KindEpic || it.Fact(machine.FactPRDMerged) == "" && it.Fact(machine.FactADR) == "" {
		return BranchFor(it.ID)
	}
	switch it.Status {
	case machine.Architecture, machine.ADRReview:
		return ADRBranchFor(it.ID)
	case machine.Design, machine.DesignReview, machine.DesignSignoff:
		return DesignBranchFor(it.ID)
	}
	return SpecBranchFor(it.ID)
}

// ItemOf is the item a head branch belongs to, or "". For a spec branch
// it is the whole name after loop/; SpecEpicOf names the epic.
func ItemOf(branch string) string {
	item, ok := strings.CutPrefix(branch, "loop/")
	if !ok || item == "" || strings.Contains(item, "/") {
		return ""
	}
	return item
}

// SpecEpicOf is the epic a spec branch belongs to, or "" when branch is
// no spec branch.
func SpecEpicOf(branch string) string { return epicOf(branch, specSuffix) }

// DesignEpicOf is the epic a design branch belongs to, or "" when branch
// is no design branch.
func DesignEpicOf(branch string) string { return epicOf(branch, designSuffix) }

func epicOf(branch, suffix string) string {
	epic, ok := strings.CutSuffix(ItemOf(branch), suffix)
	if !ok || epic == "" {
		return ""
	}
	return epic
}

// itemBranch names the branch of an item's current work. With no machine
// it is BranchFor.
func (s *Service) itemBranch(ctx context.Context, project, item string) (string, *cli.Refusal) {
	it, ref := s.item(ctx, project, item)
	if ref != nil {
		return "", ref
	}
	return BranchOf(it), nil
}

// item reads an item. With no machine, or for an id the machine does not
// hold, it is an item with only its id, whose branch is BranchFor.
func (s *Service) item(ctx context.Context, project, id string) (machine.Item, *cli.Refusal) {
	if s.cfg.Machine == nil {
		return machine.Item{ID: id}, nil
	}
	it, ref := s.cfg.Machine.Read(ctx, project, id)
	if ref != nil && ref.Code == cli.CodeNotFound {
		return machine.Item{ID: id}, nil
	}
	return it, ref
}

// repoGet reads one repository path with a token of scope.
func (s *Service) repoGet(ctx context.Context, p config.Project, scope Scope, path string, out any) error {
	_, err := s.repoDo(ctx, p, scope, Call{Method: http.MethodGet, Path: path}, out)
	return err
}

// repoDo sends one call with a token of scope.
func (s *Service) repoDo(ctx context.Context, p config.Project, scope Scope, c Call, out any) (*Response, error) {
	tok, err := s.Token(ctx, p, scope)
	if err != nil {
		return nil, err
	}
	c.Token = tok
	return s.api.Do(ctx, c, out)
}

// ReadAtBase reads path at the tip of the base branch in the project's
// mirror, and returns the base sha it read. A path the base lacks matches
// fs.ErrNotExist. It reads the mirror only: the reactor's tick and
// `branch sync` bring the base up to date. Admission reads declared
// files and cited documents through it.
func (s *Service) ReadAtBase(ctx context.Context, project, path string) ([]byte, string, error) {
	p, err := s.project(project)
	if err != nil {
		return nil, "", err
	}
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return nil, "", err
	}
	m := s.Mirror(p)
	ref := config.DefaultRef(base)
	sha := m.Resolve(ctx, ref)
	if sha == "" {
		return nil, "", fmt.Errorf("github: the mirror of %s holds no %s: %w", project, ref, fs.ErrNotExist)
	}
	data, err := m.Show(ctx, sha, path)
	return data, sha, err
}

// RefReader reads files of one project's mirror at a ref such as
// config.DefaultRef(branch). Project config and the secret manifest load
// through it (KTD20).
func (s *Service) RefReader(project string) config.RefReader {
	return func(ctx context.Context, ref, path string) ([]byte, error) {
		p, err := s.project(project)
		if err != nil {
			return nil, err
		}
		return s.Mirror(p).Show(ctx, ref, path)
	}
}

// TreeReader lists directories of one project's mirror at a ref. The
// config load checks the skill directories through it.
func (s *Service) TreeReader(project string) config.TreeReader {
	return func(ctx context.Context, ref, dir string) ([]config.TreeEntry, error) {
		p, err := s.project(project)
		if err != nil {
			return nil, err
		}
		return s.Mirror(p).Tree(ctx, ref, dir)
	}
}

// ManifestReader reads a project's secret manifest from the mirror. It
// fetches the branch first, so `secret import` reads the current default
// branch. Its shape matches server.ManifestReader.
func (s *Service) ManifestReader(ctx context.Context, p config.Project, branch string) (*config.Manifest, error) {
	tok, err := s.Token(ctx, p, ScopeRead)
	if err != nil {
		return nil, err
	}
	m := s.Mirror(p)
	if err := m.Init(ctx); err != nil {
		return nil, err
	}
	if err := m.Fetch(ctx, tok, branch); err != nil {
		return nil, err
	}
	snap, err := config.ReadProjectSnapshot(ctx, s.RefReader(p.ID), branch)
	if err != nil {
		return nil, err
	}
	return snap.Manifest, nil
}

// SyncBase fetches the base branch of one project into its mirror.
func (s *Service) SyncBase(ctx context.Context, p config.Project) (string, string, error) {
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return "", "", err
	}
	tok, err := s.Token(ctx, p, ScopeRead)
	if err != nil {
		return "", "", err
	}
	m := s.Mirror(p)
	if err := m.Fetch(ctx, tok, base); err != nil {
		return "", "", err
	}
	return base, m.Resolve(ctx, config.DefaultRef(base)), nil
}

// AddHook is the config.AddHook of `project add` and `project move`: it
// forgets the installation and tokens of the project's old repository,
// creates the bare
// mirror, fetches the default branch and checks the branch rules. A
// missing rule fails the add with one checklist line for each rule. A
// missing CI check does not: a bare project has no CI yet, and loop
// doctor reports it until it does. An empty repository has nothing to
// fetch or check, so it registers as it is.
func (s *Service) AddHook(ctx context.Context, p config.Project) error {
	s.forget(p.ID)
	if err := s.Mirror(p).Init(ctx); err != nil {
		return err
	}
	if _, _, err := s.SyncBase(ctx, p); err != nil {
		if s.empty(ctx, p) {
			return nil
		}
		return err
	}
	lines, err := s.CheckRules(ctx, p)
	if err != nil {
		return err
	}
	return failing(slices.DeleteFunc(lines, func(l CheckLine) bool { return l.Check == "ci_required" }))
}

// empty reports whether the repository has no commit: GitHub answers 409
// for its commit list.
func (s *Service) empty(ctx context.Context, p config.Project) bool {
	var commits []json.RawMessage
	err := s.repoGet(ctx, p, ScopeRead, repoPath(p, "/commits?per_page=1"), &commits)
	var api *APIError
	return errors.As(err, &api) && api.Status == http.StatusConflict
}

// Files returns the blob id of every file on the base branch in the
// project's mirror whose path starts with one of prefixes. It satisfies
// admission.BaseReader: the blob id is the content id of R10's retry
// identity. It reads the mirror only, which the reactor's tick and
// `branch sync` keep current.
func (s *Service) Files(ctx context.Context, project string, prefixes []string) (map[string]string, error) {
	p, err := s.project(project)
	if err != nil {
		return nil, err
	}
	base, err := s.DefaultBranch(ctx, p)
	if err != nil {
		return nil, err
	}
	m := s.Mirror(p)
	ref := config.DefaultRef(base)
	sha := m.Resolve(ctx, ref)
	if sha == "" {
		return nil, fmt.Errorf("github: the mirror of %s holds no %s yet: %w", project, ref, fs.ErrNotExist)
	}
	out, err := s.git.run(ctx, gitRun{dir: m.Dir, args: []string{"ls-tree", "-r", "-z", "--full-tree", sha}})
	if err != nil {
		return nil, err
	}
	return gitx.TreeFiles(out, prefixes), nil
}
