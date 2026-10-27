package orca

import (
	"os"
	"path/filepath"
)

// LaneFixtureMarker is the file under a lane fixture's .git directory. A
// lane fixture is a repository that belongs to the real-orca lane alone, so
// the lane may reset its main and change its origin. Only the lane job's
// fixture step, or the operator by hand, writes the marker. It lives under
// .git, so no commit carries it and a reset of main keeps it.
const LaneFixtureMarker = "loop-orca-fixture"

// IsLaneFixture reports whether clone's .git directory holds the marker.
func IsLaneFixture(clone string) bool {
	st, err := os.Stat(filepath.Join(clone, ".git", LaneFixtureMarker))
	return err == nil && st.Mode().IsRegular()
}
