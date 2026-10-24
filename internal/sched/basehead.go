package sched

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/gitx"
)

// BaseHeadSource returns the commit the remote holds for a project's base
// branch, and brings it into the project's clone first, so a checkout can
// start from it (C-bug-1). The clone's local branch moves only when a
// person moves it, so a checkout never starts from it.
type BaseHeadSource func(ctx context.Context, project string) (string, error)

// OriginBaseHead fetches the base branch from the origin remote of the
// project's clone into refs/remotes/origin/<base>, and returns that head.
// A server without the GitHub mirror uses it. The fetch leaves every local
// branch of the clone as it was.
func OriginBaseHead(projects func() ([]config.Project, error)) BaseHeadSource {
	return func(ctx context.Context, project string) (string, error) {
		ps, err := projects()
		if err != nil {
			return "", err
		}
		for _, p := range ps {
			if p.ID != project {
				continue
			}
			ref := "refs/remotes/origin/" + DefaultBaseBranch
			if _, err := gitx.Run(ctx, p.Path, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules",
				"origin", "+refs/heads/"+DefaultBaseBranch+":"+ref); err != nil {
				return "", err
			}
			out, err := gitx.Run(ctx, p.Path, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(out)), nil
		}
		return "", fmt.Errorf("sched: project %s is not registered", project)
	}
}

// BaseCheckout is the spec of a new checkout that starts at the remote
// base head. A head that cannot be read is an error, never the clone's
// local branch.
func BaseCheckout(ctx context.Context, heads BaseHeadSource, project, name string) (agenthost.CheckoutSpec, error) {
	sha, err := heads(ctx, project)
	if err == nil && sha == "" {
		err = errors.New("the remote holds no head")
	}
	if err != nil {
		return agenthost.CheckoutSpec{}, fmt.Errorf("sched: read the head of %s for project %s: %w", DefaultBaseBranch, project, err)
	}
	return agenthost.CheckoutSpec{Name: name, BaseBranch: sha}, nil
}
