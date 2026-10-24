package server

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/machine"
)

// fakeUpgrades records each upgrade the server starts.
type fakeUpgrades struct {
	mu       sync.Mutex
	versions []string
}

func (f *fakeUpgrades) start(_ context.Context, version string) (UpgradeStart, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if version == "bad" {
		return UpgradeStart{}, fmt.Errorf("%w: %q", ErrBadVersion, version)
	}
	f.versions = append(f.versions, version)
	return UpgradeStart{Unit: fmt.Sprintf("loop-upgrade-%d", len(f.versions)), Command: []string{"loop", "upgrade", version}}, nil
}

func (f *fakeUpgrades) started() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.versions...)
}

// factorio-8qm: the operator decides to upgrade, and the coordinator asks
// the server to run the upgrade with the operator's words. The server
// starts it outside every checkout. A stage run's token is refused, and
// the CLI's host verb keeps its refusal from inside a checkout.
func TestCoordinatorTokenStartsAServerUpgradeAndARunTokenIsRefused(t *testing.T) {
	w := newHumanWorld(t, "")
	ups := &fakeUpgrades{}
	w.opts.Upgrade = ups.start
	w.restart()

	env, code := w.coordinator("coordinator", "upgrade", "v0.0.45", "--body", `The operator: "upgrade the host"`)
	if code != 0 {
		t.Fatalf("coordinator upgrade = %d %v", code, env)
	}
	if got := ups.started(); len(got) != 1 || got[0] != "v0.0.45" {
		t.Fatalf("the server started upgrades %v, want one to v0.0.45", got)
	}
	data, _ := env["data"].(map[string]any)
	if data["started"] != true || data["version"] != "v0.0.45" || data["unit"] != "loop-upgrade-1" {
		t.Fatalf("the answer = %v", data)
	}
	if n := w.count(`SELECT count(*) FROM audit WHERE action = 'upgrade_requested' AND actor = 'coordinator:coord-1'
		AND detail LIKE '%upgrade the host%'`); n != 1 {
		t.Fatalf("%d audit rows hold the operator's decision", n)
	}

	// The latest release when no version is named.
	if env, code := w.coordinator("coordinator", "upgrade", "--body", `The operator: "take the latest"`); code != 0 {
		t.Fatalf("coordinator upgrade with no version = %d %v", code, env)
	}
	if got := ups.started(); len(got) != 2 || got[1] != "" {
		t.Fatalf("the server started upgrades %v, want a second to the latest", got)
	}

	if env, code := w.coordinator("coordinator", "upgrade", "bad", "--body", "the operator: upgrade"); code != 2 || env["code"] != cli.CodeUsage {
		t.Fatalf("a bad version = %d %v", code, env)
	}

	run := w.runToken("r-impl", "tk-1", machine.StageImplementer)
	env, code = w.as(run, "coordinator", "upgrade", "v0.0.45", "--body", "the run wants an upgrade")
	if code != 3 || env["code"] != cli.CodeCapabilityDenied || env["rule"] != "principal_not_allowed" {
		t.Fatalf("a run's upgrade = %d %v, want CAPABILITY_DENIED", code, env)
	}
	if got := ups.started(); len(got) != 2 {
		t.Fatalf("a refused upgrade started one: %v", got)
	}
}

// A server with no way to upgrade itself says so.
func TestCoordinatorUpgradeWithNoUpgraderIsNotImplemented(t *testing.T) {
	w := newHumanWorld(t, "")
	env, code := w.coordinator("coordinator", "upgrade", "--body", "the operator: upgrade")
	if code != 2 || env["code"] != cli.CodeNotImplemented {
		t.Fatalf("coordinator upgrade = %d %v", code, env)
	}
}
