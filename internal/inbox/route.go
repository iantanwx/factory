package inbox

import (
	"slices"
	"strings"

	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/rpc"
	"github.com/iantanwx/loop/internal/sched"
)

// Route names the project whose coordinator gets e (spec factorio-1edo,
// Approach, "Routing"). A question routes by the project of the
// coordinator that prepared it. A question with no project, which an
// earlier release or the old session prepared, routes as the entry it
// names. With no entry, or with one that no longer lists, it routes to the
// loop project.
func Route(e Entry, t coord.Topology) string {
	if e.Kind == KindQuestion && e.Project == "" {
		if e.about == nil {
			return t.Loop
		}
		return Route(*e.about, t)
	}
	return t.Dest(e.Project, instanceWide(e))
}

// Sessions names the coordinator session that serves a project.
type Sessions func(project string) (agenthost.RunID, bool)

func (in *Inbox) served(p rpc.Principal, es []Entry) []Entry {
	if p.Kind != cli.PrincipalCoordinator || in.cfg.Topology == nil || in.cfg.Sessions == nil {
		return es
	}
	t := in.cfg.Topology()
	home := p.Project
	if home == "" {
		home = t.Old
	}
	mine, _ := in.cfg.Sessions(home)
	return slices.DeleteFunc(es, func(e Entry) bool {
		id, _ := in.cfg.Sessions(Route(e, t))
		return id != mine
	})
}

// owner is the project that holds e: a question's is the project of the
// coordinator that prepared it, or else of the entry it names, and "" when
// it names none.
func owner(e Entry) string {
	if e.Kind != KindQuestion || e.Project != "" {
		return e.Project
	}
	if e.about == nil {
		return ""
	}
	return e.about.Project
}

// instanceWide reports whether e is the loop's own work: a loop fault, an
// alert of a failed coordinator start, or an entry with no project.
func instanceWide(e Entry) bool {
	switch {
	case e.Project == "", e.Kind == KindFault:
		return true
	case e.Kind != KindAlert:
		return false
	}
	switch e.Status {
	case sched.AlertCoordinator, sched.AlertCoordinatorCheckoutMissing:
		return true
	case sched.AlertConfigConflict:
		return strings.HasSuffix(e.Dedupe, "/"+sched.CoordinatorStage)
	}
	return false
}
