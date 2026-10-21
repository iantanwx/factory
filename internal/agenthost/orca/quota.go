package orca

import (
	"context"
	"encoding/json"
	"fmt"
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
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		var w struct {
			UsedPercent *float64 `json:"usedPercent"`
		}
		if json.Unmarshal(row[k], &w) == nil && w.UsedPercent != nil {
			q.Windows = append(q.Windows, agenthost.QuotaWindow{Name: k, UsedPercent: *w.UsedPercent})
		}
	}
	q.OK = status == "ok" && len(q.Windows) > 0
	q.Note = fmt.Sprintf("status %s, %d window(s)", safeStatus(status), len(q.Windows))
	return q, nil
}

func safeStatus(s string) string {
	if safeCode.MatchString(s) {
		return s
	}
	return "unrecognized"
}
