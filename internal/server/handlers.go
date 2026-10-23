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

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
	"github.com/iantanwx/loop/internal/secrets"
)

// ManifestReader reads a project's secret manifest from its default branch.
type ManifestReader func(ctx context.Context, p config.Project, branch string) (*config.Manifest, error)

// wire registers the verbs this unit implements.
func (s *Server) wire() error {
	for path, h := range map[string]rpc.Handler{
		"status":           s.status,
		"project add":      s.projectAdd,
		"project list":     s.projectList,
		"project show":     s.projectShow,
		"project remove":   s.projectRemove,
		"project move":     s.projectMove,
		"secret check":     s.secretCheck,
		"secret set":       s.secretSet,
		"secret import":    s.secretImport,
		"coordinator send": s.coordinatorSend,
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
	ID   string `json:"id"`
	Repo string `json:"repo"`
	Path string `json:"path"`
}

func view(p config.Project) projectView { return projectView{ID: p.ID, Repo: p.Repo, Path: p.Path} }

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
	p := config.Project{ID: call.Inv.Arg("project"), Repo: call.Inv.Flags["repo"], Path: call.Inv.Flags["path"]}
	if err := s.Projects.Add(ctx, p); err != nil {
		return projectFault(err)
	}
	return cli.Found(view(p))
}

func (s *Server) projectList(context.Context, *rpc.Call) cli.Result {
	ps, err := s.Projects.List()
	if err != nil {
		return projectFault(err)
	}
	out := make([]projectView, 0, len(ps))
	for _, p := range ps {
		out = append(out, view(p))
	}
	return cli.List(out)
}

func (s *Server) projectShow(_ context.Context, call *rpc.Call) cli.Result {
	p, err := s.Projects.Show(call.Inv.Arg("project"))
	if err != nil {
		return projectFault(err)
	}
	return cli.Found(view(p))
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
// on GitHub.
func (s *Server) projectMove(ctx context.Context, call *rpc.Call) cli.Result {
	p, err := s.Projects.Move(ctx, call.Inv.Arg("project"), call.Inv.Flags["repo"])
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
		branch = sched.DefaultBaseBranch
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
