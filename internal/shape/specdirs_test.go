package shape_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/shape"
	"github.com/iantanwx/loop/pack"
)

// C18, C19: a spec of the epic under docs/specs/ or under docs/plans/ is
// the epic's spec.
func TestC18C19SpecFileReadsBothDirectories(t *testing.T) {
	for _, spec := range []string{"docs/specs/tk-1-cache-spec.md", "docs/plans/tk-1-cache-spec.md"} {
		files := []shape.ChangedFile{{Path: "README.md"}, {Path: "docs/specs/old/tk-1-cache-spec.md"}, {Path: spec}}
		if path, matches := shape.SpecFile("tk-1", files); path != spec || matches != 1 {
			t.Fatalf("SpecFile = %q, %d, want %q, 1", path, matches, spec)
		}
	}
}

// C20: one spec of the epic under each directory is no single spec.
func TestC20SpecFileRefusesOneSpecInEachDirectory(t *testing.T) {
	files := []shape.ChangedFile{{Path: "docs/specs/tk-1-cache-spec.md"}, {Path: "docs/plans/tk-1-cache-spec.md"}}
	if path, matches := shape.SpecFile("tk-1", files); path != "" || matches != 2 {
		t.Fatalf("SpecFile = %q, %d, want \"\", 2", path, matches)
	}
}

// C22: each writer of a spec names docs/specs/ as the place of a new spec,
// and none names docs/plans/ as that place.
func TestC22EachWriterOfASpecNamesDocsSpecs(t *testing.T) {
	const place = "docs/specs/<epic id>-<slug>-spec.md"
	texts := map[string]string{}
	for _, f := range []string{"runbooks/planner.md", "skills/loop-sdlc-documents/spec.md", "skills/loop-sdlc-documents/SKILL.md"} {
		raw, err := fs.ReadFile(pack.Files, f)
		if err != nil {
			t.Fatal(err)
		}
		texts[f] = string(raw)
	}
	texts["shape.TemplateLines"] = strings.Join(shape.TemplateLines, "\n")
	var hint string
	for _, f := range shape.CheckSpec("docs/specs/tk-2-cache-spec.md", "# Spec\n\n- **Epic:** tk-1\n") {
		if f.Rule == "name" {
			hint = f.Message
		}
	}
	texts["the name hint"] = hint
	for name, text := range texts {
		if !strings.Contains(text, place) {
			t.Errorf("%s does not name %s", name, place)
		}
		if strings.Contains(text, "docs/plans/<epic id>-<slug>-spec.md") {
			t.Errorf("%s names docs/plans/<epic id>-<slug>-spec.md as the place of a new spec", name)
		}
	}
}
