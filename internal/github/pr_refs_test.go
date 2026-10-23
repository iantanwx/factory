package github

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// GitHub creates a pull request only for a token that reads both refs, the
// contents permission. A token without it gets 422 "not all refs are
// readable", as F-3 saw on slothware-labs/factorio.
func TestOpenPRTokenReadsBothRefs(t *testing.T) {
	e := newEnv(t)
	e.withWorld()
	e.api.Route("POST /repos/"+gt.Repo+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		i := slices.Index(e.api.Minted(), tok)
		perms, _ := e.api.Mints()[i]["permissions"].(map[string]any)
		if c := perms["contents"]; c != "read" && c != "write" {
			gt.WriteJSON(w, 422, map[string]any{"message": "Validation Failed",
				"errors": []any{map[string]string{"resource": "PullRequest", "code": "custom", "message": "not all refs are readable"}}})
			return
		}
		gt.WriteJSON(w, 201, map[string]any{"number": 1, "state": "open", "head": map[string]string{"ref": "loop/tk-1"},
			"base": map[string]string{"ref": "main"}})
	})
	pr, created, err := e.svc.OpenPR(context.Background(), e.project, BranchFor("tk-1"), "title", "body")
	if err != nil || !created || pr.Number != 1 {
		t.Fatalf("open: %v created=%v #%d", err, created, pr.Number)
	}
}

// ScopePulls holds the smallest subset that opens a pull request and replies
// on a thread: contents read for the refs, and nothing more.
func TestScopePullsPermissions(t *testing.T) {
	want := map[string]string{"contents": "read", "pull_requests": "write", "metadata": "read"}
	if got := Permissions(ScopePulls); !maps.Equal(got, want) {
		t.Fatalf("ScopePulls = %v, want %v", got, want)
	}
}
