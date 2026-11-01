package server

import (
	"context"
	"testing"
	"time"
)

// The fake GitHub holds one rate-limit counter, so the quota line counts
// one counter even when its reads straddle a wall-clock second.
func TestTheFakeGitHubQuotaIsOneCounterAcrossASecond(t *testing.T) {
	w := doctorWorld(t)
	w.gh.Sleep = func(context.Context, time.Duration) error {
		now := time.Now()
		time.Sleep(now.Truncate(time.Second).Add(time.Second).Sub(now) + 10*time.Millisecond)
		return nil
	}
	docs, err := w.gh.Doctor(context.Background(), w.srv.Projects)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := lineOf(t, docs, "github_quota")
	if !ok || l.Detail != "core: 5000 of 5000, graphql: 5000 of 5000, counters: 1" {
		t.Fatalf("github_quota = %+v, want detail %q", l, "core: 5000 of 5000, graphql: 5000 of 5000, counters: 1")
	}
}
