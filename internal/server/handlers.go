package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/secrets"
)

// reachable keeps the rows that the call's coordinator reaches (spec
// factorio-1edo, Approach, "Scope"). A row with no project is the
// instance's, and only the loop project's coordinator reaches it. Every
// other caller, the old session too, keeps every row.
func reachable[T any](ctx context.Context, rows []T, record func(T) coord.Record) ([]T, error) {
	c, ok := rpc.CallerFrom(ctx)
	if !ok || c.Reach == coord.ReachUnscoped {
		return rows, nil
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		r := record(row)
		keep := c.Reach == coord.ReachLoop
		if r.Project != "" {
			var err error
			if keep, err = c.Reaches(ctx, r); err != nil {
				return nil, err
			}
		}
		if keep {
			out = append(out, row)
		}
	}
	return out, nil
}

// ManifestReader reads a project's secret manifest from its default branch.
type ManifestReader func(ctx context.Context, p config.Project, branch string) (*config.Manifest, error)

// wire registers the verbs this unit implements.
func (s *Server) wire() error {
	for path, h := range map[string]rpc.Handler{
		"status":              s.status,
		"project add":         s.projectAdd,
		"project list":        s.projectList,
		"project show":        s.projectShow,
		"project remove":      s.projectRemove,
		"project move":        s.projectMove,
		"secret check":        s.secretCheck,
		"secret set":          s.secretSet,
		"secret import":       s.secretImport,
		"coordinator send":    s.coordinatorSend,
		"coordinator upgrade": s.coordinatorUpgrade,
		"doctor-env":          s.doctorEnv,
	} {
		if err := s.RPC.Handle(path, h); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) status(context.Context, *rpc.Call) cli.Result {
	return cli.Found(map[string]any{
		"server_version": cli.Version,
		"ready":          true,
		"instance":       s.Instance,
		"pid":            os.Getpid(),
		"started_at":     s.Started.UTC().Format(time.RFC3339),
		"uptime_seconds": int64(time.Since(s.Started).Seconds()),
	})
}

// projectView is a registration as the CLI prints it.
type projectView struct {
	ID     string `json:"id"`
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch"` // the base branch of the project's runs
}

func view(p config.Project) projectView {
	return projectView{ID: p.ID, Repo: p.Repo, Path: p.Path, Branch: p.BaseBranch()}
}

func projectFault(err error) cli.Result {
	switch {
	case errors.Is(err, config.ErrProjectExists):
		return cli.Reject(cli.CodeAlreadyExists, "project_exists", err.Error())
	case errors.Is(err, config.ErrProjectNotFound):
		return cli.Reject(cli.CodeNotFound, "project_not_found", err.Error())
	case errors.Is(err, config.ErrProjectHasLiveRun):
		return cli.Reject(cli.CodeProjectBusy, "project_has_live_run", err.Error())
	case errors.Is(err, config.ErrInvalid):
		return cli.Reject(cli.CodeUsage, "invalid_project", err.Error())
	}
	return cli.Internal(err)
}

func (s *Server) projectAdd(ctx context.Context, call *rpc.Call) cli.Result {
	p := config.Project{ID: call.Inv.Arg("project"), Repo: call.Inv.Flags["repo"], Path: call.Inv.Flags["path"], Branch: call.Inv.Flags["branch"]}
	if err := s.Projects.Add(ctx, p); err != nil {
		return projectFault(err)
	}
	return cli.Found(view(p))
}

func (s *Server) projectList(ctx context.Context, _ *rpc.Call) cli.Result {
	ps, err := s.Projects.List()
	if err != nil {
		return projectFault(err)
	}
	if ps, err = reachable(ctx, ps, func(p config.Project) coord.Record {
		return coord.Record{Kind: coord.KindRegistration, ID: p.ID, Project: p.ID}
	}); err != nil {
		return cli.Internal(err)
	}
	out := make([]projectView, 0, len(ps))
	for _, p := range ps {
		out = append(out, view(p))
	}
	return cli.List(out)
}

// projectShowView is project show's answer: the registration and the
// project's userspace skills.
type projectShowView struct {
	projectView
	UserSkills userSkillsView `json:"user_skills"`
}

// userSkillsView lists a project's userspace skills: each pin of its last
// good config, and each committed skill directory at its default branch.
type userSkillsView struct {
	Pins      []pinView       `json:"pins"`
	Committed []committedView `json:"committed"`
}

// pinView is one pin, and whether the host's store holds it.
type pinView struct {
	config.SkillPin
	Held bool `json:"held"`
}

// committedView is one committed skill directory and its root.
type committedView struct {
	Root string `json:"root"`
	Name string `json:"name"`
}

func (s *Server) projectShow(ctx context.Context, call *rpc.Call) cli.Result {
	p, err := s.Projects.Show(call.Inv.Arg("project"))
	if err != nil {
		return projectFault(err)
	}
	skills, err := s.userSkillsOf(ctx, p)
	if err != nil {
		return cli.Internal(err)
	}
	return cli.Found(projectShowView{projectView: view(p), UserSkills: skills})
}

// userSkillsOf reads the pins of p's last good config, whether the store
// holds each one, and the directories outside loop- under each skill root
// at p's default branch.
func (s *Server) userSkillsOf(ctx context.Context, p config.Project) (userSkillsView, error) {
	out := userSkillsView{Pins: []pinView{}, Committed: []committedView{}}
	raw, _, err := config.StoreLKG(s.Store).LoadLKG(ctx, p.ID)
	if err != nil {
		return out, err
	}
	if raw != nil {
		cfg, err := config.ParseProjectConfig(raw)
		if err != nil {
			return out, err
		}
		for _, pin := range cfg.Skills {
			_, held := s.opts.Sched.SkillStore.Held(p.ID, pin)
			out.Pins = append(out.Pins, pinView{SkillPin: pin, Held: held})
		}
	}
	refs := s.opts.Sched.Refs
	if refs == nil {
		refs = LocalRefs
	}
	r, branch, err := refs(ctx, p)
	if err != nil {
		return out, err
	}
	for _, root := range []string{brief.SkillRoot(agenthost.Claude), brief.SkillRoot(agenthost.Codex)} {
		entries, err := r.Tree(ctx, config.DefaultRef(branch), root)
		if err != nil {
			return out, err
		}
		for _, e := range entries {
			if e.Dir && !strings.HasPrefix(e.Name, brief.SkillPrefix) {
				out.Committed = append(out.Committed, committedView{Root: root, Name: e.Name})
			}
		}
	}
	return out, nil
}

func (s *Server) projectRemove(ctx context.Context, call *rpc.Call) cli.Result {
	id := call.Inv.Arg("project")
	if err := s.Projects.Remove(ctx, id, s.liveRuns); err != nil {
		return projectFault(err)
	}
	if err := s.RefreshRedactor(ctx); err != nil {
		s.log.ErrorContext(ctx, "refresh the redactor", "err", err)
	}
	return cli.Found(map[string]any{"id": id, "removed": true})
}

// projectMove points a project at another repository, as after a transfer
// on GitHub, or at its renamed default branch.
func (s *Server) projectMove(ctx context.Context, call *rpc.Call) cli.Result {
	p, err := s.Projects.Move(ctx, call.Inv.Arg("project"), call.Inv.Flags["repo"], call.Inv.Flags["branch"])
	if err != nil {
		return projectFault(err)
	}
	return cli.Found(view(p))
}

// liveRuns reports whether a project has a run that has not ended.
func (s *Server) liveRuns(ctx context.Context, project string) (bool, error) {
	var n int
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE project = ? AND ended_at IS NULL`, project).Scan(&n)
	})
	return n > 0, err
}

// secretProject is the project a secret verb acts on. A run acts on its
// own project only.
func secretProject(call *rpc.Call) (string, *cli.Result) {
	project := call.Inv.Flags["project"]
	if call.Inv.Flags["scope"] == "global" {
		if project != "" {
			res := cli.Reject(cli.CodeUsage, "scope_and_project", "--scope global stores one value for every project; drop --project")
			return "", &res
		}
		return config.GlobalScope, nil
	}
	p := call.Principal
	if p.Kind == cli.PrincipalRun || p.Kind == cli.PrincipalRoom {
		if project != "" && project != p.Project {
			res := cli.Reject(cli.CodeCapabilityDenied, "other_project", "a run checks the secrets of its own project only")
			return "", &res
		}
		project = p.Project
	}
	if project == "" {
		res := cli.Reject(cli.CodeUsage, "missing_project", "name the project with --project, or store one value for every project with --scope global")
		return "", &res
	}
	return project, nil
}

func (s *Server) secretCheck(ctx context.Context, call *rpc.Call) cli.Result {
	project, fault := secretProject(call)
	if fault != nil {
		return *fault
	}
	name := call.Inv.Arg("name")
	if !config.ValidSecretName(name) {
		return cli.Reject(cli.CodeUsage, "bad_secret_name", "a secret name is an environment variable name")
	}
	// A run gets the global value when its project stores none.
	for _, scope := range []string{project, config.GlobalScope} {
		ok, err := s.Secrets.Check(ctx, scope, name)
		if err != nil {
			return cli.Internal(err)
		}
		if ok {
			return cli.Found(map[string]any{"project": project, "name": name, "present": true, "scope": scopeName(scope)})
		}
	}
	return cli.Nothing()
}

// scopeName is how the CLI names a store scope.
func scopeName(scope string) string {
	if scope == config.GlobalScope {
		return "global"
	}
	return "project"
}

func (s *Server) secretSet(ctx context.Context, call *rpc.Call) cli.Result {
	project, fault := secretProject(call)
	if fault != nil {
		return *fault
	}
	raw, ok := call.Inv.Extra[cli.ExtraStdin]
	if !ok {
		return cli.Reject(cli.CodeUsage, "no_stdin", "secret set reads the value from stdin; pipe it in")
	}
	value, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return cli.Reject(cli.CodeUsage, "bad_stdin", "the value did not arrive intact; run the command again")
	}
	defer clear(value)
	name := call.Inv.Arg("name")
	if err := s.Secrets.Set(ctx, project, name, bytes.NewReader(value)); err != nil {
		if errors.Is(err, config.ErrProjectNotFound) {
			return cli.Reject(cli.CodeNotFound, "project_not_found", fmt.Sprintf("project %s is not registered", project))
		}
		// The secret store's errors never hold the value.
		return cli.Reject(cli.CodeUsage, "secret_refused", err.Error())
	}
	if err := s.RefreshRedactor(ctx); err != nil {
		return cli.Internal(err)
	}
	if project == config.GlobalScope {
		return cli.Found(map[string]any{"scope": "global", "name": name, "stored": true})
	}
	return cli.Found(map[string]any{"scope": "project", "project": project, "name": name, "stored": true})
}

func (s *Server) secretImport(ctx context.Context, call *rpc.Call) cli.Result {
	id := call.Inv.Flags["project"]
	p, err := s.Projects.Show(id)
	if err != nil {
		return projectFault(err)
	}
	branch := call.Inv.Flags["branch"]
	if branch == "" {
		branch = p.BaseBranch()
	}
	read := s.opts.ManifestReader
	if read == nil {
		read = GitManifest
	}
	m, err := read(ctx, p, branch)
	if err != nil {
		if errors.Is(err, config.ErrInvalid) {
			return cli.Reject(cli.CodeUsage, "invalid_manifest", err.Error())
		}
		return cli.Internal(err)
	}
	cfg, err := config.LoadHost(s.HostPath)
	if err != nil {
		return cli.Internal(err)
	}
	im := &secrets.Importer{Store: s.Secrets, Vaults: cfg.Vaults, OpPath: s.opts.OpPath, Logger: s.log, Actor: call.Principal.Actor()}
	res, err := im.Import(ctx, p.ID, m)
	if refreshErr := s.RefreshRedactor(ctx); refreshErr != nil {
		s.log.ErrorContext(ctx, "refresh the redactor", "err", refreshErr)
	}
	if errors.Is(err, secrets.ErrVaultNotDeclared) {
		return cli.Reject(cli.CodeUsage, "vault_not_declared", err.Error())
	}
	if err != nil {
		return cli.Internal(err)
	}
	imported, added := res.Imported, res.Added
	if imported == nil {
		imported = []string{}
	}
	if added == nil {
		added = []string{}
	}
	return cli.Found(map[string]any{"project": p.ID, "imported": imported, "added": added})
}

// GitManifest reads the manifest from origin/<branch> in the project's
// local directory with `git show`. It runs no hook and reads no user or
// system git config. A server with the GitHub mirror reads the bare mirror
// instead.
func GitManifest(ctx context.Context, p config.Project, branch string) (*config.Manifest, error) {
	read := func(ctx context.Context, ref, path string) ([]byte, error) {
		out, err := gitx.Run(ctx, p.Path, "show", ref+":"+path)
		if ge, ok := errors.AsType[*gitx.Error](err); ok &&
			(strings.Contains(ge.Stderr, "does not exist") || strings.Contains(ge.Stderr, "exists on disk, but not in")) {
			return nil, fs.ErrNotExist
		}
		return out, err
	}
	snap, err := config.ReadProjectSnapshot(ctx, read, branch)
	if err != nil {
		return nil, err
	}
	return snap.Manifest, nil
}
