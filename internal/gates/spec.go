package gates

import (
	"context"
	"os"
	"path/filepath"

	docshape "github.com/iantanwx/loop/internal/shape"
)

// specGlobs are every spec a project keeps, <epic id>-<slug>-spec.md in
// each directory of docshape.SpecDirs.
func specGlobs() []string {
	globs := make([]string, len(docshape.SpecDirs))
	for i, dir := range docshape.SpecDirs {
		globs[i] = dir + "*-spec.md"
	}
	return globs
}

// Spec checks the shape of each spec, as docshape.CheckSpec reads it: the
// template's headings once each and in order, no empty section, no line of
// the template left, a filename that starts with the epic id, and no open
// question that the spec sign-off refuses as undecided. It reads
// every spec, as the adr gate reads every ADR. It cannot check what a
// section says; the doc-reviewer reads that.
func Spec(_ context.Context, r *Repo) ([]Finding, error) {
	files, err := r.glob(specGlobs())
	if err != nil {
		return nil, err
	}
	var out []Finding
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(r.Root, f))
		if err != nil {
			return nil, err
		}
		for _, s := range docshape.CheckSpec(f, string(raw)) {
			out = append(out, Finding{File: f, Line: s.Line, Rule: s.Rule, Message: s.Message})
		}
	}
	return out, nil
}
