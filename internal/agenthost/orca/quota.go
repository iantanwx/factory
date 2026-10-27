package orca

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/iantanwx/loop/internal/agenthost"
)

// Quota reads `orca account list --json` and narrows result.rateLimits for
// one harness. Ported from readQuota in alpha's apps/slothbot/src/quota/read.ts.
// That read never refreshes orca's reading, and the CLI offers no refresh,
// so UpdatedAt tells the caller how old the reading is. Orca's field names
// live here and nowhere else.
func (h *Host) Quota(ctx context.Context, hs agenthost.Harness) (agenthost.Quota, error) {
	q := agenthost.Quota{Harness: hs}
	if hs == agenthost.Claude && h.claudeProxied() {
		return q, fmt.Errorf("%w: Claude Code sends its requests to ANTHROPIC_BASE_URL, and orca reads only the logged-in account", agenthost.ErrQuotaUnavailable)
	}
	env, err := h.call(ctx, "account", "list", "--json")
	if err != nil {
		return q, fmt.Errorf("%w: %w", agenthost.ErrQuotaUnavailable, err)
	}
	var res struct {
		RateLimits map[string]json.RawMessage `json:"rateLimits"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil || res.RateLimits == nil {
		return q, fmt.Errorf("%w: orca account list printed no rate limits", agenthost.ErrQuotaUnavailable)
	}
	raw, ok := res.RateLimits[string(hs)]
	if !ok {
		return q, fmt.Errorf("%w: orca has no reading for %s", agenthost.ErrQuotaUnavailable, hs)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(raw, &row); err != nil || row == nil {
		return q, fmt.Errorf("%w: orca's reading for %s is unreadable", agenthost.ErrQuotaUnavailable, hs)
	}
	var status string
	_ = json.Unmarshal(row["status"], &status)
	var updated float64
	if json.Unmarshal(row["updatedAt"], &updated) == nil && updated > 0 {
		q.UpdatedAt = time.UnixMilli(int64(updated))
	}
	keys := slices.Sorted(maps.Keys(row))
	for _, k := range keys {
		var w struct {
			UsedPercent *float64 `json:"usedPercent"`
			ResetsAt    float64  `json:"resetsAt"`
		}
		if json.Unmarshal(row[k], &w) == nil && w.UsedPercent != nil {
			win := agenthost.QuotaWindow{Name: k, UsedPercent: *w.UsedPercent}
			if w.ResetsAt > 0 {
				win.ResetsAt = time.UnixMilli(int64(w.ResetsAt))
			}
			q.Windows = append(q.Windows, win)
		}
	}
	q.OK = status == "ok" && len(q.Windows) > 0
	q.Note = fmt.Sprintf("status %s, %d window(s)", safeStatus(status), len(q.Windows))
	return q, nil
}

// claudeProxied reports whether Claude Code's settings.json sends its
// requests to another endpoint, such as a proxy that spreads them over
// several accounts. Orca reads the quota of the logged-in account only, so
// its reading then describes no account that the runs spend. The loop then
// has no reading, and a usage limit that a run hits pauses the harness.
func (h *Host) claudeProxied() bool {
	var settings struct {
		Env map[string]string `json:"env"`
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(h.cfg.ClaudeRoot), "settings.json"))
	if err != nil || json.Unmarshal(data, &settings) != nil {
		return false
	}
	return settings.Env["ANTHROPIC_BASE_URL"] != ""
}

func safeStatus(s string) string {
	if safeCode.MatchString(s) {
		return s
	}
	return "unrecognized"
}
