package brief

import (
	"path/filepath"

	"github.com/iantanwx/loop/internal/secrets"
)

// WriteEnv writes every manifest secret into the checkout's .env at mode
// 0600 (R34, KTD17). The caller listed .env in .git/info/exclude first.
func WriteEnv(checkout string, values map[string]string) error {
	return secrets.WriteEnvFile(filepath.Join(checkout, EnvFile), values)
}

// RemoveEnv deletes the checkout's .env. Every settlement calls it, a
// crashed run's kept checkout included. A missing file is no error.
func RemoveEnv(checkout string) error {
	return secrets.RemoveEnvFile(filepath.Join(checkout, EnvFile))
}
