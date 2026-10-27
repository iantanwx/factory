package gates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/iantanwx/loop/internal/release"
)

// loopModule is the module of the loop's own repository.
const loopModule = "github.com/iantanwx/loop"

// Requirements checks that the Requirements section of the README names
// each binary of release.Requirements as a code span, so a new dependency
// with no README line fails. It reads only the loop's own repository: a
// client project's README owes the table nothing.
func Requirements(_ context.Context, r *Repo) ([]Finding, error) {
	mod, err := os.ReadFile(filepath.Join(r.Root, "go.mod"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if first, _, _ := strings.Cut(string(mod), "\n"); strings.TrimSpace(first) != "module "+loopModule {
		return nil, nil
	}
	readme, err := os.ReadFile(filepath.Join(r.Root, "README.md"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	section, _ := sectionBody(string(readme), "Requirements")
	var out []Finding
	for _, req := range release.Requirements {
		if !strings.Contains(section, "`"+req.Bin+"`") {
			out = append(out, Finding{File: "README.md", Rule: "requirement_not_in_readme",
				Message: "the Requirements section names no `" + req.Bin + "`; add it with what needs it: " + req.Need})
		}
	}
	return out, nil
}
