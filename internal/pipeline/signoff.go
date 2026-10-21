package pipeline

import (
	"context"
	"time"

	"github.com/iantanwx/loop/internal/github"
	"github.com/iantanwx/loop/internal/machine"
)

// linkTimeout bounds the beads read behind one inbox link.
const linkTimeout = 5 * time.Second

// Link returns the inbox link of an item: an epic's current document PR,
// so the operator opens the PR whose merge is the sign-off, and the
// `loop work show` line for every other item. It reads beads only, so
// the inbox may call it inside a store transaction.
func Link(m *machine.Machine, projects github.Projects) func(project, item string) string {
	return func(project, item string) string {
		def := "loop work show " + item + " --project " + project
		ctx, cancel := context.WithTimeout(context.Background(), linkTimeout)
		defer cancel()
		it, ref := m.Read(ctx, project, item)
		if ref != nil || it.Kind != machine.KindEpic || it.Fact(machine.FactDocPR) == "" {
			return def
		}
		p, err := projects.Show(project)
		if err != nil {
			return def
		}
		return "https://github.com/" + p.Repo + "/pull/" + it.Fact(machine.FactDocPR)
	}
}
