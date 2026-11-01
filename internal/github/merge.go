package github

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/machine"
	"github.com/iantanwx/loop/internal/shape"
)

// prdDocStates are the epic states whose document PR is the PRD's.
var prdDocStates = []string{machine.PRDReview, machine.PRDAmending, machine.PRDSignoff}

// DocMerge fills the merge event of an epic's document PR that merged at
// sha. The reactor's merge path and the sign-off's both call it, so the
// two route one epic one way. For a PRD it reads the project config and
// the PRD at sha: when the config declares a UI, UIStories holds the label
// of each UI: yes story. A config or a PRD that the commit lacks, or a
// config that does not load, declares no UI story, so the merge goes on
// with no design stage. The caller sets the branches.
func (s *Service) DocMerge(ctx context.Context, p config.Project, it machine.Item, sha string) (machine.MergeEvent, error) {
	ev := machine.MergeEvent{Project: p.ID, Item: it.ID, SHA: sha}
	path := it.Fact(machine.FactPRDPath)
	if it.Kind != machine.KindEpic || !slices.Contains(prdDocStates, it.Status) || path == "" {
		return ev, nil
	}
	raw, err := s.ReadAt(ctx, p, sha, config.ProjectConfigPath)
	if errors.Is(err, fs.ErrNotExist) {
		return ev, nil
	}
	if err != nil {
		return ev, err
	}
	cfg, err := config.ParseProjectConfig(raw)
	if err != nil {
		s.log.WarnContext(ctx, "the config at a PRD merge does not load, so the epic routes as one with no UI",
			"project", p.ID, "item", it.ID, "sha", sha, "err", err)
		return ev, nil
	}
	if !cfg.DeclaresUI() {
		return ev, nil
	}
	prd, err := s.ReadAt(ctx, p, sha, path)
	if errors.Is(err, fs.ErrNotExist) {
		s.log.WarnContext(ctx, "the PRD merge commit holds no PRD at prd_path, so the epic routes as one with no UI story",
			"project", p.ID, "item", it.ID, "sha", sha, "path", path)
		return ev, nil
	}
	if err != nil {
		return ev, err
	}
	ev.UIStories = strings.Join(shape.UIStories(string(prd)), ",")
	return ev, nil
}
