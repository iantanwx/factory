package config

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// stageRenames maps each old stage name to its new name, for one release
// (docs/plans/factorio-vlle-loop-vocabulary-spec.md, The grace for an old
// name). Each config read rewrites an old key to its new key as it parses,
// so no other code reads an old name. No new name is also an old name, each
// new name is a stage of this loop, and no old name is one. The release
// after the renames empties it, and an old name then refuses the config.
var stageRenames = map[string]string{
	"prd-author": StageUnattendedBrainstorm,
	"fixer":      StageTriage,
}

// retiredWords are the old role words that name no stage key.
var retiredWords = []string{"PRD author"}

// NewStageName returns the new name of an old stage name, and false for
// any other name.
func NewStageName(old string) (string, bool) {
	n, ok := stageRenames[old]
	return n, ok
}

// StageRenames returns a copy of the table, sorted by old name.
func StageRenames() [][2]string {
	out := [][2]string{}
	for _, old := range slices.Sorted(maps.Keys(stageRenames)) {
		out = append(out, [2]string{old, stageRenames[old]})
	}
	return out
}

// RetiredWords lists each old role word that no pack file, help text or
// inbox kind may hold: the old keys of the table, and the words that name
// no stage.
func RetiredWords() []string {
	return slices.Concat(slices.Sorted(maps.Keys(stageRenames)), retiredWords)
}

// Rename is one old stage name that a config read rewrote.
type Rename struct {
	Source string `json:"source"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// AlertOldStageNames is the alert kind of a config that holds an old name.
// Its dedupe key is AlertOldStageNames + ":" + project, or + ":host".
const AlertOldStageNames = "old_stage_names"

// renameKeys moves each old stage key of m under section to its new key,
// and records each move under source. A stage under its old key and its
// new key fails ck, since one of the two would drop with no error.
func renameKeys[V any](ck *checker, source, section string, m map[string]V) []Rename {
	var out []Rename
	for _, old := range slices.Sorted(maps.Keys(m)) {
		n, ok := NewStageName(old)
		if !ok {
			continue
		}
		if _, both := m[n]; both {
			ck.fail(section+"."+old, "is the old name of %s.%s, which the file also holds; keep %s.%s only", section, n, section, n)
			continue
		}
		m[n] = m[old]
		delete(m, old)
		out = append(out, Rename{Source: source, Old: old, New: n})
	}
	return out
}

// renameList rewrites each old stage name of a manifest entry's stages to
// its new name, and records each rewrite under source. A list that holds a
// stage under both names keeps it once.
func renameList(source string, stages []string) ([]string, []Rename) {
	if stages == nil {
		return nil, nil
	}
	out := make([]string, 0, len(stages))
	var renames []Rename
	for _, st := range stages {
		n, ok := NewStageName(st)
		if !ok {
			out = append(out, st)
			continue
		}
		renames = append(renames, Rename{Source: source, Old: st, New: n})
		if !slices.Contains(stages, n) && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, renames
}

// ReportRenames raises the old_stage_names alert of project when renames
// holds a rewrite, and clears it when it holds none. An empty project
// names the host.
func ReportRenames(ctx context.Context, a Alerter, project string, renames []Rename) error {
	scope, file := project, "the project config"
	if project == "" {
		scope, file = "host", "the host config"
	}
	key := AlertOldStageNames + ":" + scope
	if len(renames) == 0 {
		return a.Clear(ctx, key)
	}
	return a.Raise(ctx, Alert{
		Project:   project,
		Kind:      AlertOldStageNames,
		Subject:   file + " names a stage by its old name",
		DedupeKey: key,
		Body:      renamesBody(renames),
	})
}

// renamesBody names each old name, its new name and its file, one to a
// line.
func renamesBody(renames []Rename) string {
	var b strings.Builder
	for _, r := range renames {
		fmt.Fprintf(&b, "%s names the stage %s, which is now %s.\n", r.Source, r.Old, r.New)
	}
	b.WriteString("Commit each new name. The next release refuses an old one.")
	return b.String()
}
