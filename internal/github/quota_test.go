package github

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// quotaRead is one /rate_limit answer.
func quotaRead(core, graphql int, reset time.Time) Quota {
	return Quota{
		Core:     Bucket{Limit: 5000, Remaining: core, Reset: reset},
		GraphQL:  Bucket{Limit: 5000, Remaining: graphql, Reset: reset},
		Counters: 1,
	}
}

// C19: Lowest keeps the lowest remaining of each bucket and counts the
// distinct reset times, so two counters behind one installation show.
func TestC19LowestKeepsTheLowestReadAndCountsTheCounters(t *testing.T) {
	a := time.Unix(1_800_000_000, 0).UTC()
	b := a.Add(17 * time.Minute)
	reads := []Quota{quotaRead(4720, 4990, a), quotaRead(4720, 4990, a),
		quotaRead(2654, 4801, b), quotaRead(2654, 4800, b), quotaRead(2654, 4800, b), quotaRead(2654, 4800, b)}
	got, err := Lowest(reads)
	if err != nil {
		t.Fatal(err)
	}
	want := Quota{Core: Bucket{Limit: 5000, Remaining: 2654, Reset: b}, GraphQL: Bucket{Limit: 5000, Remaining: 4800, Reset: b}, Counters: 2}
	if got != want {
		t.Fatalf("Lowest = %+v, want %+v", got, want)
	}
	if _, err := Lowest(nil); err == nil {
		t.Fatal("Lowest of no read answered no error")
	}
}

// C19: RateLimit makes one GET /rate_limit with the installation's read
// token, and never with the App's JWT.
func TestC19RateLimitReadsWithTheInstallationReadToken(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	var auths []string
	e.api.Route("GET /rate_limit", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auths = append(auths, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		mu.Unlock()
		writeJSON(w, 200, map[string]any{"resources": map[string]any{
			"core":    map[string]any{"limit": 5000, "remaining": 4321, "reset": 1_800_000_000},
			"graphql": map[string]any{"limit": 5000, "remaining": 4999, "reset": 1_800_000_060},
		}})
	})
	got, err := e.svc.RateLimit(context.Background(), e.project)
	if err != nil {
		t.Fatal(err)
	}
	want := Quota{Core: Bucket{Limit: 5000, Remaining: 4321, Reset: time.Unix(1_800_000_000, 0)},
		GraphQL: Bucket{Limit: 5000, Remaining: 4999, Reset: time.Unix(1_800_000_060, 0)}, Counters: 1}
	if !got.Core.Reset.Equal(want.Core.Reset) || !got.GraphQL.Reset.Equal(want.GraphQL.Reset) ||
		got.Core.Remaining != 4321 || got.GraphQL.Remaining != 4999 || got.Core.Limit != 5000 || got.Counters != 1 {
		t.Fatalf("RateLimit = %+v, want %+v", got, want)
	}
	minted, mints := e.api.Minted(), e.api.Mints()
	if len(auths) != 1 || !slices.Contains(minted, auths[0]) {
		t.Fatalf("the read carried %q, the fake minted %q", auths, minted)
	}
	i := slices.Index(minted, auths[0])
	perms, _ := mints[i]["permissions"].(map[string]any)
	if len(perms) != len(Permissions(ScopeRead)) || !maps.EqualFunc(Permissions(ScopeRead), perms, func(a string, b any) bool { return a == b }) {
		t.Fatalf("the read's token was minted with %v, want scope read %v", perms, Permissions(ScopeRead))
	}
}
