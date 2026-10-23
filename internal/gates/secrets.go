package gates

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// Secrets scans the whole history with gitleaks. A secret committed earlier
// on the branch and reverted at the tip is still published, so the scan
// reads every commit, not the tip. The project's .gitleaks.toml, when it
// exists, sets the allow list.
//
// It runs in CI only, where the workflow installs gitleaks, as tickertown
// runs it in a CI job of its own. Elsewhere it says so and passes.
func Secrets(ctx context.Context, r *Repo) ([]Finding, error) {
	if os.Getenv("CI") != "true" {
		_, _ = r.Out.Write([]byte("the secrets gate runs in CI only\n"))
		return nil, nil
	}
	bin, err := exec.LookPath("gitleaks")
	if err != nil {
		return nil, errors.New("gitleaks is not on PATH; install it from https://github.com/gitleaks/gitleaks/releases")
	}
	args := []string{"detect", "--source", r.Root, "--redact", "--verbose", "--no-banner", "--exit-code", "1"}
	if _, err := os.Stat(filepath.Join(r.Root, ".gitleaks.toml")); err == nil {
		args = append(args, "--config", filepath.Join(r.Root, ".gitleaks.toml"))
	}
	c := exec.CommandContext(ctx, bin, args...)
	c.Dir, c.Stdout, c.Stderr = r.Root, r.Out, r.Out
	if err := c.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return []Finding{{Rule: "secret_in_history", Message: "gitleaks found a secret; the lines above name each one"}}, nil
		}
		return nil, err
	}
	return nil, nil
}
