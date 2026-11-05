package inbox

import (
	"strings"

	"github.com/iantanwx/loop/internal/coord"
	"github.com/iantanwx/loop/internal/sched"
)

// Route names the project whose coordinator gets e (spec factorio-1edo,
// Approach, "Routing"). A question routes as the entry it names, and a
// question with no entry, or whose entry no longer lists, routes to the
// loop project.
func Route(e Entry, t coord.Topology) string {
	if e.Kind == KindQuestion {
		if e.about == nil {
			return t.Loop
		}
		return Route(*e.about, t)
	}
	return t.Dest(e.Project, instanceWide(e))
}

// owner is the project that holds e: a question's is the project of the
// entry it names, and "" when it names none.
func owner(e Entry) string {
	if e.Kind != KindQuestion {
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
