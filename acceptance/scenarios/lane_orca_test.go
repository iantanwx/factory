//go:build acceptance && orca

// The scenario binary of the real-orca lane (ADR 0008). Under these tags
// TestMain runs the lane gate before anything else, so a missing
// LOOP_ORCA_* value, a plan above the cap or a spent quota window stops
// the binary before it calls orca for anything but the quota.
package scenarios

import (
	"context"
	"errors"
	"os"

	"github.com/iantanwx/loop/acceptance"
	"github.com/iantanwx/loop/internal/agenthost"
	"github.com/iantanwx/loop/internal/agenthost/agenthosttest"
)

func init() {
	laneGate = func() int {
		harness := agenthost.Claude
		if h := os.Getenv("LOOP_ORCA_HARNESS"); h != "" {
			harness = agenthost.Harness(h)
		}
		return agenthosttest.LaneGate{
			Getenv:  os.Getenv,
			Plan:    agenthosttest.LanePlan(),
			Harness: harness,
			Quota: func() (agenthost.QuotaReader, error) {
				rh, err := acceptance.OrcaHost(0)
				if err != nil {
					return nil, err
				}
				q, ok := rh.Agents.(agenthost.QuotaReader)
				if !ok {
					return nil, errors.New("the orca host reads no quota")
				}
				return q, nil
			},
			Out: os.Stdout,
		}.Run(context.Background())
	}
}
