//go:build acceptance

package acceptance

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"sync"

	"github.com/iantanwx/loop/internal/cli"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/release"
)

// Each run, room and coordinator the suite starts goes through
// CheckRunbook: the brief names a runbook, the runbook is in the checkout,
// and every command it names resolves against the schema, by the pack
// gate's own rules. The suite records each stage it saw start.

var (
	startedMu sync.Mutex
	started   = map[string]int{}

	regOnce sync.Once
	reg     *cli.Registry
	regErr  error
)

// registry is the schema the pack gate checks against: the CLI surface
// and the refusal codes of the client-side host verbs, as pack-check
// builds it.
func registry() (*cli.Registry, error) {
	regOnce.Do(func() {
		reg = cli.Surface()
		for _, c := range release.Refusals() {
			if err := reg.RegisterRefusal(c); err != nil {
				regErr = err
				return
			}
		}
	})
	return reg, regErr
}

// CheckRunbook reads the brief in a checkout, checks the runbook it names
// with the pack gate, and records the stage. It returns the stage.
func (w *World) CheckRunbook(dir string) string {
	w.T.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".loop", "brief.json"))
	if err != nil {
		w.T.Fatalf("no brief in %s: %v", dir, err)
	}
	var b struct {
		Stage   string `json:"stage"`
		Runbook string `json:"runbook"`
	}
	if err := json.Unmarshal(raw, &b); err != nil || b.Stage == "" || b.Runbook == "" {
		w.T.Fatalf("the brief in %s names no stage or runbook: %v %s", dir, err, raw)
	}
	text, err := os.ReadFile(filepath.Join(dir, b.Runbook))
	if err != nil {
		w.T.Fatalf("the %s runbook %s is not in the checkout: %v", b.Stage, b.Runbook, err)
	}
	r, err := registry()
	if err != nil {
		w.T.Fatal(err)
	}
	findings, err := pack.CheckText(b.Runbook, string(text), r)
	if err != nil {
		w.T.Fatal(err)
	}
	for _, f := range findings {
		w.T.Errorf("the %s runbook: %s", b.Stage, f)
	}
	startedMu.Lock()
	started[b.Stage]++
	startedMu.Unlock()
	return b.Stage
}

// Started is how many times each stage, room and the coordinator started
// in this process, as CheckRunbook saw them.
func Started() map[string]int {
	startedMu.Lock()
	defer startedMu.Unlock()
	return maps.Clone(started)
}
