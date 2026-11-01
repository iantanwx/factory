package shape

import (
	"path"
	"slices"
	"strings"
)

// SpecDir is where a new spec lands.
const SpecDir = "docs/specs/"

// SpecDirs lists each directory that holds a spec, the new one first. A
// spec merged before SpecDir stays under docs/plans/
// (docs/plans/factorio-vlle-loop-vocabulary-spec.md, The spec path).
var SpecDirs = []string{SpecDir, "docs/plans/"}

// ChangedFile is one file that a PR changes.
type ChangedFile struct {
	Path    string
	Removed bool // the PR deletes the file
}

// SpecPattern names the path of epic's spec in each of SpecDirs, for a hint.
func SpecPattern(epic string) string {
	patterns := make([]string, len(SpecDirs))
	for i, dir := range SpecDirs {
		patterns[i] = dir + epic + "-*-spec.md"
	}
	return strings.Join(patterns, " or ")
}

// SpecFile returns the one path of files that is epic's spec, or "" with the
// count of matches when there is not exactly one. A spec is a file that the
// PR does not remove, at <dir><epic>-*-spec.md for a dir of SpecDirs.
func SpecFile(epic string, files []ChangedFile) (spec string, matches int) {
	for _, f := range files {
		dir, name := path.Split(f.Path)
		rest, ok := strings.CutPrefix(name, epic+"-")
		if f.Removed || !slices.Contains(SpecDirs, dir) || !ok || !strings.HasSuffix(rest, "-spec.md") {
			continue
		}
		spec, matches = f.Path, matches+1
	}
	if matches != 1 {
		return "", matches
	}
	return spec, 1
}
