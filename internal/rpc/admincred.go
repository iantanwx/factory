package rpc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/iantanwx/loop/internal/cli"
)

// EnsureAdminCredential writes a random host-admin credential to a 0600
// file in the instance directory when none exists, and returns the
// credential (KTD12). It never replaces an existing credential, and it
// refuses one with loose permissions. `loop install` calls it.
func EnsureAdminCredential(instance string) (string, error) {
	if err := ensurePrivateDir(instance); err != nil {
		return "", err
	}
	path := cli.AdminCredentialPath(instance)
	if _, err := os.Lstat(path); err == nil {
		return cli.ReadAdminCredential(instance)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	cred := "loopadmin_" + hex.EncodeToString(raw[:])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("rpc: create the admin credential: %w", err)
	}
	if _, err := f.WriteString(cred + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return cred, nil
}

// adminMatches compares a presented credential with the instance's file
// in constant time. A missing or unreadable file matches nothing.
func adminMatches(instance, presented string) bool {
	if presented == "" {
		return false
	}
	want, err := cli.ReadAdminCredential(instance)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(presented)) == 1
}
