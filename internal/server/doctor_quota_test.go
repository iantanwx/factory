package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/config"
	"github.com/iantanwx/loop/internal/github"
	gt "github.com/iantanwx/loop/internal/github/githubtest"
)

// decoyToken is the operator's own token. No quota read may carry it.
const decoyToken = "ghp_operatorDecoy0123456789"

// lineOf returns the one line named check of project alpha.
func lineOf(t *testing.T, docs []ProjectDoctor, check string) (github.CheckLine, bool) {
	t.Helper()
	if len(docs) != 1 || docs[0].Project != "alpha" {
		t.Fatalf("doctor = %+v", docs)
	}
	var found []github.CheckLine
	for _, l := range docs[0].Lines {
		if l.Check == check {
			found = append(found, l)
		}
	}
	if len(found) > 1 {
		t.Fatalf("%d %s lines: %+v", len(found), check, found)
	}
	if len(found) == 0 {
		return github.CheckLine{}, false
	}
	return found[0], true
}

// doctorWorld registers alpha with its project config on the default
// branch, so every line but the one a test changes passes.
func doctorWorld(t *testing.T) *ghWorld {
	t.Helper()
	w := newGHWorld(t)
	if err := w.srv.Projects.Add(context.Background(), config.Project{ID: "alpha", Repo: gt.Repo, Path: w.checkout}); err != nil {
		t.Fatal(err)
	}
	w.api.World.SetFile("main", config.ProjectConfigPath, "merge: direct\n")
	return w
}

// C19: the quota line reads /rate_limit six times, 500 ms apart, with the
// installation's read token, and prints the lowest values and the count of
// counters. It never reads the operator's own token.
func TestC19TheQuotaLineReadsTheLowestOfSixReads(t *testing.T) {
	w := doctorWorld(t)
	ctx := context.Background()

	// The operator's own token sits where release.GHToken reads it.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho "+decoyToken+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_TOKEN", decoyToken)
	t.Setenv("GITHUB_TOKEN", decoyToken)

	resetA, resetB := int64(1_800_000_000), int64(1_800_001_020)
	var mu sync.Mutex
	var auths []string
	answer := func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		n := len(auths)
		mu.Unlock()
		core, graphql, reset := 4720, 4990, resetA
		if n > 2 {
			core, graphql, reset = 2654, 4800, resetB
		}
		gt.WriteJSON(w, 200, map[string]any{"resources": map[string]any{
			"core":    map[string]any{"limit": 5000, "remaining": core, "reset": reset},
			"graphql": map[string]any{"limit": 5000, "remaining": graphql, "reset": reset},
		}})
	}
	w.api.Route("GET /rate_limit", answer)
	var pauses []time.Duration
	w.gh.Sleep = func(_ context.Context, d time.Duration) error {
		pauses = append(pauses, d)
		return nil
	}

	docs, err := w.gh.Doctor(ctx, w.srv.Projects)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := lineOf(t, docs, "github_quota")
	if !ok || !l.OK {
		t.Fatalf("github_quota = %+v", l)
	}
	for _, want := range []string{"core: 2654 of 5000", "graphql: 4800 of 5000", "counters: 2"} {
		if !strings.Contains(l.Detail, want) {
			t.Errorf("detail %q lacks %q", l.Detail, want)
		}
	}
	if len(auths) != github.QuotaReads || github.QuotaReads != 6 {
		t.Fatalf("%d reads of /rate_limit, want 6", len(auths))
	}
	if !slices.Equal(pauses, slices.Repeat([]time.Duration{500 * time.Millisecond}, 5)) {
		t.Fatalf("pauses = %v, want five of 500ms", pauses)
	}
	minted, mints := w.api.Minted(), w.api.Mints()
	for _, a := range auths {
		i := slices.Index(minted, a)
		if a == decoyToken || i < 0 {
			t.Fatalf("a read carried %q, which the fake did not mint", a)
		}
		perms, _ := mints[i]["permissions"].(map[string]any)
		if len(perms) != len(github.Permissions(github.ScopeRead)) || perms["contents"] != "read" {
			t.Fatalf("a read's token was minted with %v, not scope read", perms)
		}
	}
	raw, _ := json.Marshal(docs)
	for _, tok := range append(minted, decoyToken) {
		if strings.Contains(string(raw), tok) {
			t.Fatalf("the answer names a token: %s", raw)
		}
	}

	// A low value fails nothing: ADR D8 rejected a floor.
	w.api.Route("GET /rate_limit", func(w http.ResponseWriter, r *http.Request) {
		gt.WriteJSON(w, 200, map[string]any{"resources": map[string]any{
			"core":    map[string]any{"limit": 5000, "remaining": 3, "reset": resetA},
			"graphql": map[string]any{"limit": 5000, "remaining": 0, "reset": resetA},
		}})
	})
	docs, _ = w.gh.Doctor(ctx, w.srv.Projects)
	if l, _ := lineOf(t, docs, "github_quota"); !l.OK || !strings.Contains(l.Detail, "core: 3 of 5000") || !strings.Contains(l.Detail, "counters: 1") {
		t.Fatalf("github_quota on a low quota = %+v", l)
	}

	// A failed read fails the line and quotes the error.
	w.api.Route("GET /rate_limit", func(w http.ResponseWriter, r *http.Request) {
		gt.WriteJSON(w, 403, map[string]string{"message": "Resource not accessible by integration"})
	})
	docs, _ = w.gh.Doctor(ctx, w.srv.Projects)
	if l, _ := lineOf(t, docs, "github_quota"); l.OK || !strings.Contains(l.Detail, "Resource not accessible by integration") {
		t.Fatalf("github_quota on a failed read = %+v", l)
	}
}
