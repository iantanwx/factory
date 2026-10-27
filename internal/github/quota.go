package github

import (
	"context"
	"errors"
	"time"

	"github.com/iantanwx/loop/internal/config"
)

// QuotaReads and QuotaPause port READS and PAUSE_MS of gh-quota.ts (alpha).
// One installation can meet more than one counter, so `loop doctor` reads
// the limit several times and keeps the lowest.
const (
	QuotaReads = 6
	QuotaPause = 500 * time.Millisecond
)

// Bucket is one rate-limit resource of GET /rate_limit.
type Bucket struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset_at"`
}

// Quota is the lowest remaining of core and of graphql across the reads,
// and the count of counters met: the distinct core reset times.
type Quota struct {
	Core     Bucket `json:"core"`
	GraphQL  Bucket `json:"graphql"`
	Counters int    `json:"counters"`
}

// RateLimit makes one GET /rate_limit with the installation's read token.
// It never reads the operator's own token (ADR 0004 D8).
func (s *Service) RateLimit(ctx context.Context, p config.Project) (Quota, error) {
	type bucket struct {
		Limit     int   `json:"limit"`
		Remaining int   `json:"remaining"`
		Reset     int64 `json:"reset"`
	}
	var out struct {
		Resources struct {
			Core    bucket `json:"core"`
			GraphQL bucket `json:"graphql"`
		} `json:"resources"`
	}
	if err := s.repoGet(ctx, p, ScopeRead, "/rate_limit", &out); err != nil {
		return Quota{}, err
	}
	conv := func(b bucket) Bucket {
		return Bucket{Limit: b.Limit, Remaining: b.Remaining, Reset: time.Unix(b.Reset, 0).UTC()}
	}
	return Quota{Core: conv(out.Resources.Core), GraphQL: conv(out.Resources.GraphQL), Counters: 1}, nil
}

// Lowest keeps the lowest remaining of each bucket and counts the
// distinct reset times. It errors on an empty list.
func Lowest(reads []Quota) (Quota, error) {
	if len(reads) == 0 {
		return Quota{}, errors.New("github: no rate limit read to compare")
	}
	low := reads[0]
	resets := map[int64]bool{}
	for _, q := range reads {
		if q.Core.Remaining < low.Core.Remaining {
			low.Core = q.Core
		}
		if q.GraphQL.Remaining < low.GraphQL.Remaining {
			low.GraphQL = q.GraphQL
		}
		resets[q.Core.Reset.Unix()] = true
	}
	low.Counters = len(resets)
	return low, nil
}
