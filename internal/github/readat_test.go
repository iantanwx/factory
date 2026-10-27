package github

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

// ReadAt reads one file at a commit through the contents API, so a
// sign-off reads the head the operator approves. A path the commit lacks
// is fs.ErrNotExist.
func TestReadAtReadsAFileAtACommit(t *testing.T) {
	e := newEnv(t)
	w := e.withWorld()
	w.SetFile(shaA, "docs/plans/ep-1-cache-spec.md", "# Spec\n\n## Open questions\n\nNone.\n")
	w.SetFile("main", "docs/plans/ep-1-cache-spec.md", "the base text")
	ctx := context.Background()
	got, err := e.svc.ReadAt(ctx, e.project, shaA, "docs/plans/ep-1-cache-spec.md")
	if err != nil || string(got) != "# Spec\n\n## Open questions\n\nNone.\n" {
		t.Fatalf("ReadAt = %q, %v", got, err)
	}
	if calls := e.api.Calls(); !strings.HasSuffix(calls[len(calls)-1], "/contents/docs/plans/ep-1-cache-spec.md?ref="+shaA) {
		t.Fatalf("last call = %q, want the file at %s", calls[len(calls)-1], shaA)
	}
	if _, err := e.svc.ReadAt(ctx, e.project, shaA, "docs/plans/missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing path = %v, want fs.ErrNotExist", err)
	}
}
