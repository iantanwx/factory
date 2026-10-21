package github

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// defaultCooldown is the wait after a secondary rate limit that names no
// time. GitHub asks a client to wait at least one minute then.
const defaultCooldown = time.Minute

// Cooldown is the one rate-limit pause that every GitHub caller of one
// server shares (KTD16). A call made while it holds sends nothing, because a
// request sent sooner extends GitHub's limit. Ported from the policy of
// scripts/sdlc/github-retry.ts (alpha): a 403 or a 429 never retries.
type Cooldown struct {
	mu    sync.Mutex
	until time.Time
	now   func() time.Time
}

// NewCooldown returns a cooldown on the clock now, or time.Now when nil.
func NewCooldown(now func() time.Time) *Cooldown {
	if now == nil {
		now = time.Now
	}
	return &Cooldown{now: now}
}

// Check returns a RateLimitError while the cooldown holds.
func (c *Cooldown) Check() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now().Before(c.until) {
		return &RateLimitError{Until: c.until}
	}
	return nil
}

// Until is the end of the current cooldown, or the zero time.
func (c *Cooldown) Until() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now().Before(c.until) {
		return c.until
	}
	return time.Time{}
}

// extend moves the end of the cooldown to t when t is later.
func (c *Cooldown) extend(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.After(c.until) {
		c.until = t
	}
}

// Observe reads one answer. It starts or extends the cooldown when the
// answer is a rate limit, and it returns the RateLimitError the caller
// gets. It returns nil for any other answer.
func (c *Cooldown) Observe(status int, h http.Header, body string) error {
	limited := status == http.StatusTooManyRequests
	if status == http.StatusForbidden {
		limited = h.Get("Retry-After") != "" || h.Get("X-RateLimit-Remaining") == "0" ||
			strings.Contains(strings.ToLower(body), "rate limit")
	}
	if !limited {
		return nil
	}
	now := c.now()
	until := now.Add(defaultCooldown)
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s > 0 {
		until = now.Add(time.Duration(s) * time.Second)
	} else if h.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && time.Unix(reset, 0).After(now) {
			until = time.Unix(reset, 0)
		}
	}
	c.extend(until)
	return &RateLimitError{Until: c.Until()}
}
