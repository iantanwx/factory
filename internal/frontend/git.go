package frontend

import (
	"context"
	"maps"
	"slices"

	"github.com/iantanwx/loop/internal/gitx"
)

// gitTree is the tree of one commit in a git repository.
type gitTree struct {
	ctx   context.Context
	dir   string
	blobs map[string]string // each tracked path to its object id
}

// GitTree reads the tracked paths of ref in the repository at dir, its
// whole tree whatever subdirectory dir names.
func GitTree(ctx context.Context, dir, ref string) (Tree, error) {
	out, err := gitx.Run(ctx, dir, "ls-tree", "-r", "-z", "--full-tree", ref)
	if err != nil {
		return nil, err
	}
	return gitTree{ctx: ctx, dir: dir, blobs: gitx.TreeFiles(out, []string{""})}, nil
}

func (g gitTree) Paths() []string { return slices.Sorted(maps.Keys(g.blobs)) }

func (g gitTree) Read(path string) ([]byte, error) {
	return gitx.Run(g.ctx, g.dir, "cat-file", "blob", g.blobs[path])
}
