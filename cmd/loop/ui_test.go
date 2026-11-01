package main

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// uiRepo makes a git repository that commits tracked, writes untracked
// without adding it, and makes the repository the working directory.
func uiRepo(t *testing.T, tracked, untracked map[string]string) {
	t.Helper()
	root := t.TempDir()
	write := func(files map[string]string) {
		for name, body := range files {
			p := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(tracked)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "chore: start"},
	} {
		c := exec.Command("git", args...)
		c.Dir = root
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write(untracked)
	t.Chdir(filepath.Join(root, "internal"))
}

// uiDetect runs `loop ui detect` in the working directory and returns its
// exit and the envelope's data, as JSON.
func uiDetect(t *testing.T) (int, map[string]any, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"ui", "detect"}, strings.NewReader(""), &stdout, &stderr)
	var env map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\n%s", err, stdout.String())
	}
	data, err := json.Marshal(env["data"])
	if err != nil {
		t.Fatal(err)
	}
	return exit, env, string(data)
}

var goFiles = map[string]string{"go.mod": "module example.com/x\n", "internal/x.go": "package x\n"}

func with(base map[string]string, extra map[string]string) map[string]string {
	out := maps.Clone(base)
	maps.Copy(out, extra)
	return out
}

// C24: `loop ui detect` prints the signals and the proposed block.
func TestC24UIDetectPrintsTheProposal(t *testing.T) {
	cases := []struct {
		name    string
		tracked map[string]string
		data    string
	}{
		{"react in apps/web/package.json",
			with(goFiles, map[string]string{"apps/web/package.json": `{"dependencies": {"react": "^18.0.0"}}`}),
			`{"proposed":{"kind":"web","paths":["apps/web/**"]},"signals":[{"detail":"react","kind":"package","path":"apps/web/package.json"}]}`},
		{"react and an ios directory",
			with(goFiles, map[string]string{
				"apps/web/package.json":     `{"dependencies": {"react": "^18.0.0"}}`,
				"ios/App/AppDelegate.swift": "import UIKit\n",
			}),
			`{"proposed":{"kind":"native","paths":["apps/web/**","ios/**"]},"signals":[{"detail":"react","kind":"package","path":"apps/web/package.json"},{"detail":"ios","kind":"mobile","path":"ios"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			uiRepo(t, c.tracked, nil)
			exit, env, data := uiDetect(t)
			if exit != 0 || env["ok"] != true || data != c.data {
				t.Fatalf("exit %d, data %s, want exit 0, data %s\n%v", exit, data, c.data, env)
			}
		})
	}
}

// C24: a repository with no tracked signal exits 1 with empty data. An
// untracked .tsx file shows no signal.
func TestC24UIDetectFindsNothingInATreeWithNoSignal(t *testing.T) {
	uiRepo(t, goFiles, map[string]string{"web/App.tsx": "export {}\n"})
	exit, env, data := uiDetect(t)
	if exit != 1 || env["ok"] != true || data != "[]" {
		t.Fatalf("exit %d, data %s, want exit 1, data []\n%v", exit, data, env)
	}
}

// C24: outside a git checkout the verb exits 2 with USAGE.
func TestC24UIDetectRefusesOutsideACheckout(t *testing.T) {
	t.Chdir(t.TempDir())
	exit, env, _ := uiDetect(t)
	if exit != 2 || env["code"] != "USAGE" || env["rule"] != "not_a_checkout" {
		t.Fatalf("exit %d, want 2 USAGE not_a_checkout\n%v", exit, env)
	}
}
