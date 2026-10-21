package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureKeyFileCreatesPrivateKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	path := filepath.Join(dir, "kek")
	if err := EnsureKeyFile(path); err != nil {
		t.Fatalf("EnsureKeyFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o400 {
		t.Fatalf("key file mode = %v, %v; want 0400", fi.Mode().Perm(), err)
	}
	di, err := os.Stat(dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("key dir mode = %v, %v; want 0700", di.Mode().Perm(), err)
	}
	kp, err := NewFileKeyProvider(path)
	if err != nil {
		t.Fatalf("NewFileKeyProvider: %v", err)
	}
	k, err := kp.Current()
	if err != nil || len(k.Material) != 32 || k.ID == "" || k.Version < 1 {
		t.Fatalf("Current = %+v, %v", k.ID, err)
	}
	// A second install keeps the existing key.
	if err := EnsureKeyFile(path); err != nil {
		t.Fatalf("second EnsureKeyFile: %v", err)
	}
	kp2, err := NewFileKeyProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := kp2.Current()
	if k2.ID != k.ID || string(k2.Material) != string(k.Material) {
		t.Fatal("a second install replaced the key")
	}
	if _, err := kp.Lookup("kek-other"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Lookup unknown = %v", err)
	}
}

func TestKeyFileWithLoosePermissionsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	path := filepath.Join(dir, "kek")
	if err := EnsureKeyFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileKeyProvider(path); !errors.Is(err, ErrKeyFilePermissions) {
		t.Fatalf("0644 key file: %v, want ErrKeyFilePermissions", err)
	}
	if err := EnsureKeyFile(path); !errors.Is(err, ErrKeyFilePermissions) {
		t.Fatalf("EnsureKeyFile over a 0644 key: %v", err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileKeyProvider(path); !errors.Is(err, ErrKeyFilePermissions) {
		t.Fatalf("0755 key dir: %v, want ErrKeyFilePermissions", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileKeyProvider(link); !errors.Is(err, ErrKeyFilePermissions) {
		t.Fatalf("symlinked key: %v, want ErrKeyFilePermissions", err)
	}
}

func TestKeyFileMalformedRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "kek")
	if err := os.WriteFile(path, []byte("loop-kek-v1 kek-x 1 c2hvcnQ=\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileKeyProvider(path); err == nil {
		t.Fatal("a short key loaded")
	}
}
