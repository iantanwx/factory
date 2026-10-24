package overwatch_test

import (
	"testing"

	"github.com/iantanwx/loop/internal/overwatch"
	"github.com/iantanwx/loop/internal/release"
)

// C21: the bugs part names the loop's project by release.ReleaseRepo.
// overwatch cannot import release, which imports the server, so this test
// holds its copy equal.
func TestC21TheLoopRepositoryIsTheReleaseRepository(t *testing.T) {
	if overwatch.LoopRepo != release.ReleaseRepo {
		t.Fatalf("the bugs part reads %q, and releases come from %q", overwatch.LoopRepo, release.ReleaseRepo)
	}
}
