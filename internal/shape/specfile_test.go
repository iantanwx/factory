package shape_test

import (
	"testing"

	"github.com/iantanwx/loop/internal/shape"
)

// C2: the spec's path is the one changed file that matches
// docs/plans/<epic>-*-spec.md and is not removed.
func TestSpecFilePicksTheOneSpecOfTheEpic(t *testing.T) {
	const spec = "docs/plans/tk-1-cache-spec.md"
	cases := []struct {
		name    string
		files   []shape.ChangedFile
		path    string
		matches int
	}{
		{"no file", nil, "", 0},
		{
			"no file matches",
			[]shape.ChangedFile{
				{Path: "docs/plans/tk-1-cache.md"}, {Path: "docs/plans/tk-2-cache-spec.md"},
				{Path: "docs/plans/tk-1-spec.md"}, {Path: "docs/tk-1-cache-spec.md"},
				{Path: "docs/plans/old/tk-1-cache-spec.md"}, {Path: "docs/plans/tk-10-cache-spec.md"},
			},
			"", 0,
		},
		{"one match", []shape.ChangedFile{{Path: "README.md"}, {Path: spec}}, spec, 1},
		{
			"two matches",
			[]shape.ChangedFile{{Path: spec}, {Path: "docs/plans/tk-1-store-spec.md"}},
			"", 2,
		},
		{"a removed spec is no match", []shape.ChangedFile{{Path: spec, Removed: true}}, "", 0},
		{
			"a removed spec beside the new one",
			[]shape.ChangedFile{{Path: "docs/plans/tk-1-old-spec.md", Removed: true}, {Path: spec}},
			spec, 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path, matches := shape.SpecFile("tk-1", c.files)
			if path != c.path || matches != c.matches {
				t.Fatalf("SpecFile = %q, %d, want %q, %d", path, matches, c.path, c.matches)
			}
		})
	}
}
