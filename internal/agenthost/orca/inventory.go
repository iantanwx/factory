package orca

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/iantanwx/loop/internal/agenthost"
)

// InventoryLimit is the row limit of one `terminal list`. Alpha's
// reclaim-native-state.ts reads with the same limit.
const InventoryLimit = 10000

// Inventory runs `orca terminal list --limit N --json` and proves it
// complete. The proof ports ownerPresent in alpha's admission-native.ts and
// worktreeListing in alpha's orca.ts: the page is not truncated, the total
// equals the rows, every row names a handle and an incarnation, no handle
// repeats, and orca omitted no host. Anything less is incomplete, and an
// incomplete inventory never makes a session Absent.
func (h *Host) Inventory(ctx context.Context) (agenthost.Inventory, error) {
	env, err := h.call(ctx, "terminal", "list", "--limit", fmt.Sprint(InventoryLimit), "--json")
	if err != nil {
		return agenthost.Inventory{Why: err.Error()}, err
	}
	var res struct {
		Terminals  []terminal `json:"terminals"`
		TotalCount *int       `json:"totalCount"`
		Truncated  *bool      `json:"truncated"`
		HostScope  *struct {
			HostIDs        []string `json:"hostIds"`
			OmittedHostIDs []string `json:"omittedHostIds"`
		} `json:"hostScope"`
	}
	if err := json.Unmarshal(env.Result, &res); err != nil {
		err = fmt.Errorf("%w: orca terminal list printed an unreadable result", agenthost.ErrHostUnavailable)
		return agenthost.Inventory{Why: err.Error()}, err
	}
	inv := agenthost.Inventory{Epoch: env.Meta.RuntimeID}
	seen := map[string]bool{}
	valid := true
	for _, t := range res.Terminals {
		if t.Handle == "" || t.IncarnationID == "" || t.ExecutionHostID == "" || seen[t.Handle] {
			valid = false
			continue
		}
		seen[t.Handle] = true
		inv.Rows = append(inv.Rows, agenthost.Row{
			ID: t.id(), Checkout: t.WorktreePath, Pty: t.PtyID, Agent: t.AgentIdentity,
			Connected: t.Connected && !t.Orphaned,
		})
	}
	inv.Why = incompleteness(res.TotalCount, res.Truncated, len(res.Terminals), valid, inv.Epoch)
	if inv.Why == "" && (res.HostScope == nil || len(res.HostScope.HostIDs) == 0 || len(res.HostScope.OmittedHostIDs) > 0) {
		inv.Why = "orca returned incomplete host coverage"
	}
	inv.Complete = inv.Why == ""
	return inv, nil
}

func incompleteness(total *int, truncated *bool, rows int, valid bool, epoch string) string {
	switch {
	case total == nil || truncated == nil:
		return "orca printed no total or truncation flag"
	case *truncated:
		return fmt.Sprintf("orca truncated the list at %d of %d", rows, *total)
	case *total != rows:
		return fmt.Sprintf("orca listed %d rows of a total of %d", rows, *total)
	case !valid:
		return "a row names no handle, incarnation or host, or repeats a handle"
	case epoch == "":
		return "orca printed no runtime id"
	}
	return ""
}
