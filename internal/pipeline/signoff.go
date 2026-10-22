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
// `loop work show` line for every other item. It reads the item from
// beads, so the inbox calls it outside a store transaction.
func Link(m *machine.Machine, projects github.Projects) func(project, item string) string {
	link := ItemLink(projects)
	return func(project, item string) string {
		ctx, cancel := context.WithTimeout(context.Background(), linkTimeout)
		defer cancel()
		it, ref := m.Read(ctx, project, item)
		if ref != nil {
			return workShow(project, item)
		}
		return link(it)
	}
}

// ItemLink is Link for an item the caller already read. It reads no beads.
func ItemLink(projects github.Projects) func(it machine.Item) string {
	return func(it machine.Item) string {
		if it.Kind != machine.KindEpic || it.Fact(machine.FactDocPR) == "" {
			return workShow(it.Project, it.ID)
		}
		p, err := projects.Show(it.Project)
		if err != nil {
			return workShow(it.Project, it.ID)
		}
		return "https://github.com/" + p.Repo + "/pull/" + it.Fact(machine.FactDocPR)
	}
}

func workShow(project, item string) string { return "loop work show " + item + " --project " + project }
