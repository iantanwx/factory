package overwatch

import (
	"context"
	"fmt"

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
	return classifyServer(lines), nil
}

// classifyServer is the server part's rule. Lines that all passed read as
// one note.
func classifyServer(lines []github.CheckLine) Reading {
	var r Reading
	for _, l := range lines {
		if l.OK {
			continue
		}
		key := Key("server", l.Check)
		r.Findings = append(r.Findings, Finding{Key: key, Line: fmt.Sprintf("%s failed: %s (%s)", l.Check, l.Detail, key)})
	}
	if len(r.Findings) == 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("all %d doctor checks passed", len(lines)))
	}
	return r
}
