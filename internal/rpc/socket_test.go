package rpc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBindRefusesADirectoryOthersCanReach(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if l, err := Bind(filepath.Join(dir, "loop.sock")); err == nil {
		_ = l.Close()
		t.Fatal("Bind in a 0755 directory must fail")
	}
}

func TestBindMakesAPrivateSocket(t *testing.T) {
	dir := privateDir(t)
	path := filepath.Join(dir, "loop.sock")
	l, err := Bind(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket mode = %v, want 0600", perm)
	}
}
