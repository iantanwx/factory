package shape

import (
	"path"
	"strings"
)

// ChangedFile is one file that a PR changes.
type ChangedFile struct {
	Path    string
	Removed bool // the PR deletes the file
}

// SpecFile returns the one path of files that is epic's spec, or "" with the
// count of matches when there is not exactly one. A spec is a file that the
// PR does not remove, at docs/plans/<epic>-*-spec.md.
func SpecFile(epic string, files []ChangedFile) (spec string, matches int) {
	for _, f := range files {
		dir, name := path.Split(f.Path)
		rest, ok := strings.CutPrefix(name, epic+"-")
		if f.Removed || dir != "docs/plans/" || !ok || !strings.HasSuffix(rest, "-spec.md") {
			continue
		}
		spec, matches = f.Path, matches+1
	}
	if matches != 1 {
		return "", matches
	}
	return spec, 1
}
