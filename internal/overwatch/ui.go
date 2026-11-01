package overwatch

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/frontend"
)

// uiPart reads each project whose last good config declares no ui block.
// A frontend at its default branch is work, so the coordinator asks the
// operator whether to declare one (factorio-791i, C26 to C28). The part
// reads no tree of a project that declares ui, and it writes no file and
// no ref: only loop-init writes the block, on the operator's yes.
func uiPart() Part { return Part{Name: "ui", Read: readUI} }

func readUI(ctx context.Context, w World) (Reading, error) {
	projects, err := w.Projects(ctx)
	if err != nil {
		return Reading{}, err
	}
	var r Reading
	for _, p := range projects {
		snap, err := w.ProjectConfig(ctx, p.ID)
		switch {
		case errors.Is(err, config.ErrConfigMissing):
			r.Notes = append(r.Notes, fmt.Sprintf("%s: the project holds no config yet, so loop-init proposes its ui block", p.ID))
			continue
		case err != nil:
			r.Notes = append(r.Notes, fmt.Sprintf("UNREAD: %s: read the project config: %v", p.ID, err))
			continue
		case snap.Config.UI != nil:
			continue
		}
		ref := config.DefaultRef(p.BaseBranch())
		tree, err := w.Tracked(ctx, p.ID)
		if err != nil {
			r.Notes = append(r.Notes, fmt.Sprintf("UNREAD: %s: read the tracked paths of %s: %v", p.ID, ref, err))
			continue
		}
		r.add(classifyUI(p, ref, frontend.Detect(tree)))
	}
	return r, nil
}

// classifyUI is the ui part's rule for a project that declares no ui
// block: each signal at ref is one clause of one finding, which proposes
// the block that covers them.
func classifyUI(p config.Project, ref string, d frontend.Detection) Reading {
	if len(d.Signals) == 0 {
		return Reading{}
	}
	sigs := make([]string, 0, len(d.Signals))
	for _, s := range d.Signals {
		sigs = append(sigs, fmt.Sprintf("%s (%s)", s.Path, s.Detail))
	}
	paths := make([]string, 0, len(d.Proposed.Paths))
	for _, g := range d.Proposed.Paths {
		paths = append(paths, g.String())
	}
	key := Key("ui", p.ID)
	return Reading{Findings: []Finding{{Key: key, Line: fmt.Sprintf(
		"%s: %s shows a frontend, and the project config declares no ui block: %s. The proposed block is kind %s, paths %s. %s",
		p.ID, ref, joinAnd(sigs), d.Proposed.Kind, strings.Join(paths, ", "), key)}}}
}
