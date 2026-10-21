package beadsx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Guard refuses a workspace that can change a bead without the server, or
// that sends data off the host. It returns an error that wraps ErrRefused
// and names every finding.
//
// bd 1.3.1 has no setting that turns on a timed resume or a lease reclaim.
// A defer with a date wakes to "open" on the next ready read, and `bd
// reclaim` reverts a claim with a stale lease to "open". Each writes "open"
// over a custom status (KTD3). So the guard refuses the state that arms
// them: a deferred issue, an issue with defer_until, or an issue with a
// lease. The adapter itself never defers, claims, reads `bd ready` or
// reclaims.
func Guard(ctx context.Context, b *BD) error {
	var findings []string
	cfg, err := b.config(ctx)
	if err != nil {
		return err
	}
	want := map[string]string{"metrics.disabled": "true", "dolt_mode": "server"}
	for _, kv := range workspaceConfig {
		want[kv[0]] = kv[1]
	}
	for k, v := range want {
		if got, ok := cfg[k]; ok && got != v || !ok && mustReport(k) {
			findings = append(findings, fmt.Sprintf("config %s is %q, want %q", k, got, v))
		}
	}
	for _, k := range []string{"sync.remote", "backup.url", "backup.git-repo", "federation.peers"} {
		if v := cfg[k]; v != "" {
			findings = append(findings, fmt.Sprintf("config %s is set to %q", k, v))
		}
	}
	// The isolated user config must keep metrics off on its own, because a
	// bd command run by hand without DO_NOT_TRACK reads only this file.
	if hb, err := os.ReadFile(filepath.Join(b.cfg.Home, ".config", "bd", "config.yaml")); err != nil ||
		!strings.Contains(string(hb), "disabled: true") {
		findings = append(findings, "the isolated bd user config does not turn metrics off")
	}

	out, err := b.run(ctx, "dolt", "remote", "list", "--json")
	if err != nil {
		return err
	}
	var remotes []map[string]any
	if err := decode("dolt", out, &remotes); err != nil {
		return err
	}
	if len(remotes) > 0 {
		findings = append(findings, fmt.Sprintf("the database has %d Dolt remote(s)", len(remotes)))
	}

	out, err = b.run(ctx, "list", "--json", "--all", "--limit", "0")
	if err != nil {
		return err
	}
	var issues []wireIssue
	if err := decode("list", out, &issues); err != nil {
		return err
	}
	for _, w := range issues {
		if w.Status == "deferred" {
			findings = append(findings, fmt.Sprintf("issue %s is deferred", w.ID))
		}
		if w.DeferUntil != nil {
			findings = append(findings, fmt.Sprintf("issue %s has defer_until", w.ID))
		}
		if w.LeaseExpiresAt != nil {
			findings = append(findings, fmt.Sprintf("issue %s holds a claim lease that bd reclaim can revert", w.ID))
		}
	}
	if len(findings) > 0 {
		return fmt.Errorf("%w: %s", ErrRefused, strings.Join(findings, "; "))
	}
	return nil
}

// mustReport names the keys bd 1.3.1 always reports in `bd config show`.
// The guard treats a missing key that bd always reports as a finding.
func mustReport(k string) bool {
	switch k {
	case "metrics.disabled", "dolt_mode", "events-journal", "export.auto", "import.auto", "backup.enabled":
		return true
	}
	return false
}
