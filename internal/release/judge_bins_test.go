package release

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// fakeBin puts an executable named name in dir.
func fakeBin(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

// C21: judge_binaries passes when each binary of the live panel is on the
// path, and fails naming each one that is not. A host with no project
// gets no line. The check runs no binary.
func TestC21JudgeBinariesLineFollowsThePanel(t *testing.T) {
	ctx := context.Background()
	layout := Layout{Dir: t.TempDir()}
	path := t.TempDir()
	t.Setenv("PATH", path)
	d := &Doctor{Layout: layout, Home: t.TempDir(),
		Remote: func(context.Context) ([]DoctorLine, error) { return nil, nil }}

	if err := config.SaveHost(layout.HostConfig(), &config.HostConfig{StateDir: layout.Dir, Throughput: config.DefaultThroughput()}); err != nil {
		t.Fatal(err)
	}
	if l, ok := byCheck(d.Run(ctx))["judge_binaries"]; ok {
		t.Fatalf("a host with no project got %+v", l)
	}

	if err := config.SaveHost(layout.HostConfig(), &config.HostConfig{StateDir: layout.Dir, Throughput: config.DefaultThroughput(),
		Projects: []config.Project{{ID: "alpha", Repo: "o/alpha", Path: "/src/alpha"}}}); err != nil {
		t.Fatal(err)
	}
	fakeBin(t, path, "claude")
	l := byCheck(d.Run(ctx))["judge_binaries"]
	if l.Check == "" || l.OK || !strings.Contains(l.Detail, "codex") || strings.Contains(l.Detail, "claude") {
		t.Fatalf("judge_binaries with codex missing = %+v", l)
	}

	fakeBin(t, path, "codex")
	if l := byCheck(d.Run(ctx))["judge_binaries"]; !l.OK {
		t.Fatalf("judge_binaries with both on the path = %+v", l)
	}
}
