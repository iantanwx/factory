package overwatch

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"

	"github.com/iantanwx/loop/internal/config"
)

// verifySkill matches the tracked path of a project's verify skill, which
// loop-verify-create writes.
const verifySkill = ".claude/skills/verify-*/SKILL.md"

// qaPart reads each project whose last good config turns the qa stage on.
// A tree at its default branch with no verify skill is work: a QA run there
// can only post neutral, so the coordinator brings the project to the
// operator (factorio-nhb5, C32). The part reads no tree of a project with
// the stage off.
func qaPart() Part { return Part{Name: "qa", Read: readQA} }

func readQA(ctx context.Context, w World) (Reading, error) {
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	for _, p := range projects {
		snap, err := w.ProjectConfig(ctx, p.ID)
		switch {
		case errors.Is(err, config.ErrConfigMissing):
			continue
		case err != nil:
			r.Notes = append(r.Notes, fmt.Sprintf("UNREAD: %s: read the project config: %v", p.ID, err))
			continue
		case !snap.Config.StageEnabled(config.StageQA):
			continue
		}
		ref := config.DefaultRef(p.BaseBranch())
		tree, err := w.Tracked(ctx, p.ID)
		if err != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("UNREAD: %s: read the tracked paths of %s: %v", p.ID, ref, err))
			continue
		}
		if slices.ContainsFunc(tree.Paths(), func(f string) bool { ok, _ := path.Match(verifySkill, f); return ok }) {
			continue
		}
		key := Key("qa", p.ID)
		r.Findings = append(r.Findings, Finding{Key: key, Project: p.ID, Line: fmt.Sprintf(
			"%s: the qa stage is on, and %s tracks no %s, so each QA run posts neutral. Run loop-verify-create in the project, or turn the stage off. %s",
			p.ID, ref, verifySkill, key)})
	}
	return r, nil
}
