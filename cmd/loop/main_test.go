package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestBinary builds the real binary once, the way the Makefile does, and
// checks the process boundary: one JSON envelope on stdout and the
// envelope's exit as the process exit.
func TestBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "loop")
	build := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-X github.com/iantanwx/loop/internal/cli.Version=v9.9.9-test", "-o", bin, ".")
	build.Env = append(build.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cases := []struct {
		args []string
		exit int
		code string
	}{
		{[]string{"schema", "--json"}, 0, ""},
		{[]string{"version"}, 0, ""},
		{[]string{"work", "peek", "--bogus"}, 2, "USAGE"},
		{[]string{"work", "list"}, 2, "NOT_IMPLEMENTED"},
	}
	for _, tc := range cases {
		cmd := exec.Command(bin, tc.args...)
		stdout, err := cmd.Output()
		exit := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exit = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		var env map[string]any
		if err := json.Unmarshal(stdout, &env); err != nil {
			t.Fatalf("%v: stdout is not one JSON envelope: %v\n%s", tc.args, err, stdout)
		}
		if exit != tc.exit || env["exit"] != float64(tc.exit) {
			t.Errorf("%v: process exit %d, envelope exit %v; want %d", tc.args, exit, env["exit"], tc.exit)
		}
		if tc.code != "" && env["code"] != tc.code {
			t.Errorf("%v: code %v, want %s", tc.args, env["code"], tc.code)
		}
		if tc.args[0] == "version" {
			if v := env["data"].(map[string]any)["version"]; v != "v9.9.9-test" {
				t.Errorf("ldflags version = %v", v)
			}
		}
	}
}
