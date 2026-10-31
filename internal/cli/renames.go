package cli

import (
	"fmt"

	"github.com/iantanwx/loop/internal/config"
)

// stageRenames is the rename table that each stage input reads. A test
// puts its own pair in place.
var stageRenames = config.StageRenames

// renamedStage refuses value when it is an old stage name or room kind,
// and the hint names the new name. A runbook of this release sends only
// new names, so only a session that started under an old pack meets it.
func renamedStage(path, input, value string) *Result {
	for _, r := range stageRenames() {
		if r[0] == value {
			return usage("stage_renamed", fmt.Sprintf("%s: %s is now %s; send %s", input, r[0], r[1], r[1]), path)
		}
	}
	return nil
}

// renamedArg refuses the first argument of v that names a stage by its
// old name.
func renamedArg(v *Verb, args []string) *Result {
	for i, a := range v.Args {
		if a.Stage && i < len(args) {
			if res := renamedStage(v.Path, "<"+a.Name+">", args[i]); res != nil {
				return res
			}
		}
	}
	return nil
}
