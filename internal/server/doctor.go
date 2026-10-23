package server

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// requiredPermissions is the App's granted set (KTD15). `loop doctor`
// fails when the installation lacks one. internal/github fails on any
// permission beyond its own allowed set.
var requiredPermissions = map[string]string{
	"contents": "write", "pull_requests": "write", "statuses": "write", "workflows": "write",
	"checks": "read", "actions": "read", "metadata": "read",
}

// ProjectDoctor is the server-side `loop doctor` answer for one project.
type ProjectDoctor struct {
	Project string             `json:"project"`
	Lines   []github.CheckLine `json:"lines"`
}

// Doctor runs the checks of `loop doctor` that need GitHub, so the
// platform credentials never leave the server: for each project, the
// installation's permissions, the default
// branch's rules, and project config on the default branch. A check
// that cannot run is a failed line, never a pass.
func (g *GitHub) Doctor(ctx context.Context, projects *config.Projects) ([]ProjectDoctor, error) {
	if g.Service == nil {
		return nil, fmt.Errorf("server: the GitHub service is not set up")
	}
	ps, err := projects.List()
	if err != nil {
		return nil, err
	}
	out := []ProjectDoctor{}
	for _, p := range ps {
		lines := g.Service.Doctor(ctx, p)
		lines = append(lines, g.requiredPermissionsLine(ctx, p))
		lines = append(lines, g.projectConfigLine(ctx, p))
		out = append(out, ProjectDoctor{Project: p.ID, Lines: lines})
	}
	return out, nil
}

func (g *GitHub) requiredPermissionsLine(ctx context.Context, p config.Project) github.CheckLine {
	line := github.CheckLine{Check: "installation_required_permissions"}
	in, err := g.Service.Installation(ctx, p)
	if err != nil {
		line.Detail = "the installation could not be read: " + err.Error()
		return line
	}
	var missing []string
	for _, k := range slices.Sorted(maps.Keys(requiredPermissions)) {
		if github.PermissionLevel(in.Permissions[k]) < github.PermissionLevel(requiredPermissions[k]) {
			missing = append(missing, k+":"+requiredPermissions[k])
		}
	}
	line.OK = len(missing) == 0
	line.Detail = "grant the loop App " + strings.Join(missing, ", ")
	if line.OK {
		line.Detail = "the App holds every permission the loop needs"
	}
	return line
}

func (g *GitHub) projectConfigLine(ctx context.Context, p config.Project) github.CheckLine {
	line := github.CheckLine{Check: "project_config"}
	branch, err := g.Service.DefaultBranch(ctx, p)
	if err != nil {
		line.Detail = "the default branch could not be read: " + err.Error()
		return line
	}
	ok, err := g.Service.FileAt(ctx, p, branch, config.ProjectConfigPath)
	switch {
	case err != nil:
		line.Detail = fmt.Sprintf("%s on %s could not be read: %v", config.ProjectConfigPath, branch, err)
	case !ok:
		line.Detail = fmt.Sprintf("merge %s to %s; loop project add proposes it", config.ProjectConfigPath, branch)
	default:
		line.OK, line.Detail = true, fmt.Sprintf("%s exists on %s", config.ProjectConfigPath, branch)
	}
	return line
}
