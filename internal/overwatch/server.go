package overwatch

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
)

// serverPart is the part server (C19): host drift. Each failed doctor
// line, from the install drift check, a required binary or the GitHub
// checks, is work keyed by its check name, and its line quotes the
// check's detail.
func serverPart() Part {
	return Part{Name: "server", Command: "loop doctor", Read: readServer}
}

// readServer reads the doctor lines: the host lines, then GitHub.
func readServer(ctx context.Context, w World) (Reading, error) {
	lines, err := w.Doctor(ctx)
	if err != nil {
		return Reading{}, err
	}
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, fmt.Errorf("read the projects: %w", err)
	}
	return classifyServer(lines, projects), nil
}

// classifyServer is the server part's rule. Lines that all passed read as
// one note. A check named <project>/<check> is about that project, and a
// host line names no project.
func classifyServer(lines []github.CheckLine, projects []config.Project) Reading {
	var r Reading
	for _, l := range lines {
		if l.OK {
			continue
		}
		key := Key("server", l.Check)
		f := Finding{Key: key, Line: fmt.Sprintf("%s failed: %s (%s)", l.Check, l.Detail, key)}
		if i := slices.IndexFunc(projects, func(p config.Project) bool { return strings.HasPrefix(l.Check, p.ID+"/") }); i >= 0 {
			f.Project = projects[i].ID
		}
		r.Findings = append(r.Findings, f)
	}
	if len(r.Findings) == 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("all %d doctor checks passed", len(lines)))
	}
	return r
}
