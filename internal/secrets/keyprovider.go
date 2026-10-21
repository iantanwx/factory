package secrets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Key errors. Callers test them with errors.Is.
var (
	// ErrUnknownKey means a wrapped key names a key_id this host does not
	// hold.
	ErrUnknownKey = errors.New("secrets: unknown key_id")
	// ErrKeyFilePermissions means the key file or its directory is open to
	// another user, is a symlink, or belongs to another user.
	ErrKeyFilePermissions = errors.New("secrets: key file permissions are too open")
)

// kekLen is the AES-256 key-encryption key length in bytes.
const kekLen = 32

// Key is one key-encryption key. Version is bound into every ciphertext it
// wraps, so a row cannot claim another version.
type Key struct {
	ID       string
	Version  int64
	Material []byte
}

// KeyProvider supplies key-encryption keys (KTD17). A new secret is wrapped
// by Current. An existing secret is opened by the key its row names, so a
// rotation or a move to another OS user changes no stored format.
type KeyProvider interface {
	Current() (Key, error)
	Lookup(id string) (Key, error)
}

// keyFileMagic starts the one line of a host key file:
//
//	loop-kek-v1 <key_id> <version> <base64 key>
const keyFileMagic = "loop-kek-v1"

// FileKeyProvider holds the key from one host key file. The file sits at
// mode 0400 in a 0700 directory, outside the database and outside every
// snapshot (KTD17).
type FileKeyProvider struct {
	key Key
}

// EnsureKeyFile creates the host key file at install time when it is
// missing, with a fresh random key. It never replaces an existing key, and
// it refuses an existing key file or directory with loose permissions.
func EnsureKeyFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("secrets: create key directory: %w", err)
	}
	if err := checkPrivate(dir, true); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		_, err := NewFileKeyProvider(path)
		return err
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secrets: stat key file: %w", err)
	}
	material := make([]byte, kekLen)
	if _, err := rand.Read(material); err != nil {
		return err
	}
	idb := make([]byte, 8)
	if _, err := rand.Read(idb); err != nil {
		return err
	}
	line := fmt.Sprintf("%s kek-%s 1 %s\n", keyFileMagic, hex.EncodeToString(idb), base64.StdEncoding.EncodeToString(material))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return fmt.Errorf("secrets: create key file: %w", err)
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("secrets: write key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("secrets: sync key file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("secrets: close key file: %w", err)
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

// NewFileKeyProvider reads the host key file at path. It refuses a key file
// that is a symlink, is not owned by this user, or has any permission bit
// beyond 0400, and a directory with any bit beyond 0700.
func NewFileKeyProvider(path string) (*FileKeyProvider, error) {
	if err := checkPrivate(filepath.Dir(path), true); err != nil {
		return nil, err
	}
	if err := checkPrivate(path, false); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("secrets: read key file: %w", err)
	}
	fields := strings.Fields(string(bytes.TrimSpace(data)))
	if len(fields) != 4 || fields[0] != keyFileMagic {
		return nil, fmt.Errorf("secrets: key file %s is not a %s file", path, keyFileMagic)
	}
	version, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil || version < 1 {
		return nil, fmt.Errorf("secrets: key file %s holds a bad version", path)
	}
	material, err := base64.StdEncoding.DecodeString(fields[3])
	if err != nil || len(material) != kekLen {
		return nil, fmt.Errorf("secrets: key file %s does not hold a %d-byte key", path, kekLen)
	}
	return &FileKeyProvider{key: Key{ID: fields[1], Version: version, Material: material}}, nil
}

// Current returns the file's key.
func (p *FileKeyProvider) Current() (Key, error) { return p.key, nil }

// Lookup returns the key with id, or ErrUnknownKey.
func (p *FileKeyProvider) Lookup(id string) (Key, error) {
	if id != p.key.ID {
		return Key{}, fmt.Errorf("%w: %s", ErrUnknownKey, id)
	}
	return p.key, nil
}

// checkPrivate refuses a path that another user can reach.
func checkPrivate(path string, dir bool) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("secrets: stat %s: %w", path, err)
	}
	allowed := fs.FileMode(0o400)
	if dir {
		allowed = 0o700
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%w: %s is a symlink", ErrKeyFilePermissions, path)
	case dir && !fi.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrKeyFilePermissions, path)
	case !dir && !fi.Mode().IsRegular():
		return fmt.Errorf("%w: %s is not a regular file", ErrKeyFilePermissions, path)
	case fi.Mode().Perm()&^allowed != 0:
		return fmt.Errorf("%w: %s has mode %04o, want %04o or stricter", ErrKeyFilePermissions, path, fi.Mode().Perm(), allowed)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s belongs to uid %d, not this user", ErrKeyFilePermissions, path, st.Uid)
	}
	return nil
}
