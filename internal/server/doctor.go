package server

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/brief"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/room"
	"github.com/iantanwx/loop/internal/rpc"
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

// StaleEnvCheck is the check name of each line `loop doctor-env` prints.
const StaleEnvCheck = "stale_env"

// doctorEnv answers `loop doctor-env`: each .env in a checkout with no
// live run, and with --fix the removal of each (KTD17). Every settlement
// already removes .env; this finds the one a settlement missed. The
// server answers it because only the store knows which runs live.
func (s *Server) doctorEnv(ctx context.Context, call *rpc.Call) cli.Result {
	dirs, err := s.staleEnvCheckouts(ctx)
	if err != nil {
		return cli.Internal(err)
	}
	fix := call.Inv.Flags["fix"] == "true"
	lines := []github.CheckLine{}
	for _, d := range dirs {
		// A checkout's root .env and each .env its record names, the
		// same files a settlement removes (parity 9.3).
		names, err := brief.PresentEnv(d)
		if err != nil {
			lines = append(lines, github.CheckLine{Check: StaleEnvCheck, Detail: d + ": " + err.Error()})
			continue
		}
		if len(names) == 0 {
			continue
		}
		var rerr error
		if fix {
			rerr = brief.RemoveEnv(d)
		}
		for _, n := range names {
			p := filepath.Join(d, filepath.FromSlash(n))
			line := github.CheckLine{Check: StaleEnvCheck}
			switch {
			case !fix:
				line.Detail = p + ": no live run holds this checkout; loop doctor --fix removes it"
			case rerr != nil:
				line.Detail = p + ": the removal failed: " + rerr.Error()
			default:
				line.OK, line.Detail = true, "removed "+p
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		lines = append(lines, github.CheckLine{Check: StaleEnvCheck, OK: true, Detail: "no checkout without a live run holds a .env"})
	}
	return cli.List(lines)
}

// staleEnvCheckouts lists each checkout the loop made that no live run
// holds. A checkout is held by its run until the run ends, and by its
// room while the room is open or live: an idle or interrupted room has
// none (KTD17). The coordinator's checkout, known by its token, never
// gets a .env, so a .env there is listed even while it runs.
func (s *Server) staleEnvCheckouts(ctx context.Context) ([]string, error) {
	var dirs []string
	err := s.Store.Read(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT c.path FROM checkouts c
			WHERE NOT EXISTS (SELECT 1 FROM runs r WHERE r.run_id = c.run_id AND r.ended_at IS NULL)
			AND NOT EXISTS (SELECT 1 FROM rooms m WHERE m.checkout_id = c.id AND m.state IN (?, ?, ?))
			UNION SELECT t.checkout FROM run_tokens t WHERE t.checkout NOT IN (SELECT path FROM checkouts)
			ORDER BY 1`, room.Opening, room.Live, room.Applied)
		if err != nil {
			return err
		}
		defer func() { _ = rs.Close() }()
		for rs.Next() {
			var d string
			if err := rs.Scan(&d); err != nil {
				return err
			}
			dirs = append(dirs, d)
		}
		return rs.Err()
	})
	return dirs, err
}
