package server

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/iantanwx/loop/internal/admission"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/rpc"
)

// composeAdmission builds admission over the host config file and the
// machine's beads resolver, unless Options.Claims names another guard.
func (s *Server) composeAdmission() (*admission.Admission, error) {
	base := s.opts.BaseFiles
	if base == nil {
		base = GitBase{Projects: s.Projects, Branch: "main"}
	}
	return admission.New(admission.Config{
		Store:   s.Store,
		Host:    &config.HostLoader{Path: s.HostPath, Alerts: config.StoreAlerter(s.Store)},
		Beads:   s.opts.Beads,
		Base:    base,
		Deps:    s.opts.Dependencies,
		Release: cli.Version,
	})
}

// workPeek is `loop work peek --stage`: the stage's work query, with no
// claim (R20). A run asks for its own project.
func (s *Server) workPeek(ctx context.Context, call *rpc.Call) cli.Result {
	p, flag := call.Principal, call.Inv.Flags["project"]
	project := p.Project
	switch {
	case flag != "" && p.Project != "" && flag != p.Project && p.Kind != cli.PrincipalCoordinator:
		return refuse(cli.CodeCapabilityDenied, "other_project", "a run asks for the work of its own project only")
	case flag != "":
		project = flag
	case project == "":
		return refuse(cli.CodeUsage, "no_project", "name the project with --project")
	}
	c, ref := s.Admission.Peek(ctx, project, call.Inv.Flags["stage"])
	if ref != nil {
		return cli.Refuse(*ref)
	}
	if c == nil {
		return cli.Nothing()
	}
	return cli.Found(c)
}

// GitBase reads a project's base branch from the project's local directory
// with `git ls-tree`, at origin/<Branch>. It runs no hook and reads no user
// or system git config. U10 replaces it with a reader over the bare mirror,
// which knows each project's base branch.
type GitBase struct {
	Projects *config.Projects
	Branch   string
}

// Files implements admission.BaseReader. The content id is the blob id.
func (g GitBase) Files(ctx context.Context, project string, prefixes []string) (map[string]string, error) {
	p, err := g.Projects.Show(project)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", p.Path, "ls-tree", "-r", "-z", "--full-tree", "origin/"+g.Branch)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree origin/%s: %w: %s", g.Branch, err, strings.TrimSpace(stderr.String()))
	}
	files := map[string]string{}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta) // mode, type, object id
		if len(f) != 3 {
			continue
		}
		for _, pre := range prefixes {
			if strings.HasPrefix(path, pre) {
				files[path] = f[2]
				break
			}
		}
	}
	return files, nil
}
