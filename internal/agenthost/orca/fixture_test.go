package orca_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/agenthost/orca"
)

// C26: only a clone whose .git holds the marker is a lane fixture.
func TestC26OnlyAMarkedCloneIsALaneFixture(t *testing.T) {
	clone := t.TempDir()
	if orca.IsLaneFixture(clone) {
		t.Fatal("a directory with no .git is a lane fixture")
	}
	if err := os.Mkdir(filepath.Join(clone, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if orca.IsLaneFixture(clone) {
		t.Fatal("a clone with no marker is a lane fixture")
	}
	if err := os.Mkdir(filepath.Join(clone, ".git", "loop-orca-fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	if orca.IsLaneFixture(clone) {
		t.Fatal("a clone whose marker is a directory is a lane fixture")
	}
	if err := os.Remove(filepath.Join(clone, ".git", "loop-orca-fixture")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clone, ".git", "loop-orca-fixture"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !orca.IsLaneFixture(clone) {
		t.Fatal("a clone with the marker is no lane fixture")
	}
}
