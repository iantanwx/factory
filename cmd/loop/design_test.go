package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// captureFixture is a design room's checkout with the record of
// factorio-x, and a prototype worktree whose HEAD the record pins. The
// prototype commits the fixed PNG, and the capture command copies it by a
// relative path, so a capture outside the prototype fails.
type captureFixture struct {
	room, prototype string
	log             string // the targets the capture command received, one a line
	png             []byte // the one PNG the capture command copies
}

// git runs git in dir and returns its trimmed output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.email=t@example.com", "-c", "user.name=t"}, args...)...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newCaptureFixture makes the fixture. The capture command fails as fail
// says at the target failAt: "exit", "text", or "" to never fail. A brief
// with no design block holds no capture command.
func newCaptureFixture(t *testing.T, design bool, fail, failAt string) captureFixture {
	t.Helper()
	testdata, err := filepath.Abs("testdata/design")
	if err != nil {
		t.Fatal(err)
	}
	png, err := os.ReadFile(filepath.Join(testdata, "fixed.png"))
	if err != nil {
		t.Fatal(err)
	}
	f := captureFixture{room: t.TempDir(), prototype: t.TempDir(), log: filepath.Join(t.TempDir(), "targets"), png: png}
	for name, body := range map[string][]byte{"index.html": []byte("<p>fleet</p>\n"), "fixed.png": png} {
		if err := os.WriteFile(filepath.Join(f.prototype, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, f.prototype, "init", "-q", "-b", "loop/factorio-x-prototype")
	git(t, f.prototype, "add", "-A")
	git(t, f.prototype, "commit", "-q", "-m", "feat: draw the fleet")
	pin := git(t, f.prototype, "rev-parse", "HEAD")

	record, err := os.ReadFile(filepath.Join(testdata, "record.md"))
	if err != nil {
		t.Fatal(err)
	}
	b := map[string]any{"run_id": "room-1", "item": map[string]any{"id": "factorio-x", "kind": "epic"}}
	if design {
		b["design"] = map[string]string{
			"run":     "true",
			"capture": fmt.Sprintf("sh %q %q %q %q", filepath.Join(testdata, "capture.sh"), f.log, fail, failAt),
		}
	}
	brief, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"docs/design/factorio-x/record.md": bytes.ReplaceAll(record, []byte("PIN"), []byte(pin)),
		".loop/brief.json":                 brief,
	} {
		p := filepath.Join(f.room, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, f.room, "init", "-q", "-b", "loop/factorio-x-design")
	git(t, f.room, "add", "docs")
	git(t, f.room, "commit", "-q", "-m", "docs: record the fleet design")
	t.Chdir(f.room)
	return f
}

// capture runs `loop design capture` with args in the working directory,
// and returns its exit and its envelope.
func capture(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), append([]string{"design", "capture"}, args...), strings.NewReader(""), &stdout, &stderr)
	var env map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\n%s", err, stdout.String())
	}
	return exit, env
}

// pictures returns each picture in the record's directory, by its name.
func (f captureFixture) pictures(t *testing.T) map[string][]byte {
	t.Helper()
	dir := filepath.Join(f.room, "docs/design/factorio-x")
	names, err := filepath.Glob(filepath.Join(dir, "*.png"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		out[filepath.Base(n)] = b
	}
	return out
}

// targets returns the targets the capture command received, in order.
func (f captureFixture) targets(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// C21: each picture is a capture of the prototype at the pinned commit. A
// capture command that copies one fixed PNG puts that PNG's bytes in every
// state's picture, and receives each state's target.
func TestC21CaptureWritesEachStatePictureFromThePrototype(t *testing.T) {
	f := newCaptureFixture(t, true, "", "")
	exit, env := capture(t, "--prototype", f.prototype)
	if exit != 0 || env["ok"] != true {
		t.Fatalf("exit %d, want 0\n%v", exit, env)
	}
	got := f.pictures(t)
	for _, name := range []string{"s1-1.png", "s1-2.png", "s1-3.png"} {
		if !bytes.Equal(got[name], f.png) {
			t.Errorf("%s holds %d bytes, want the fixed PNG's %d bytes", name, len(got[name]), len(f.png))
		}
	}
	if len(got) != 3 {
		t.Errorf("the record's directory holds %d pictures, want 3", len(got))
	}
	if want := []string{"/fleet?empty", "/fleet", "/fleet?sort=name"}; fmt.Sprint(f.targets(t)) != fmt.Sprint(want) {
		t.Errorf("targets %v, want %v", f.targets(t), want)
	}
}

// C21: --state captures that one state alone.
func TestC21CaptureOfOneState(t *testing.T) {
	f := newCaptureFixture(t, true, "", "")
	exit, env := capture(t, "--prototype", f.prototype, "--state", "S1.2")
	if exit != 0 || env["ok"] != true {
		t.Fatalf("exit %d, want 0\n%v", exit, env)
	}
	got := f.pictures(t)
	if len(got) != 1 || !bytes.Equal(got["s1-2.png"], f.png) {
		t.Fatalf("pictures %v, want s1-2.png alone with the fixed PNG", keys(got))
	}
	if want := []string{"/fleet"}; fmt.Sprint(f.targets(t)) != fmt.Sprint(want) {
		t.Fatalf("targets %v, want %v", f.targets(t), want)
	}
}

// C21: each refusal exits 3 with SHAPE_REFUSED and its rule, and writes no
// picture. The hint of capture_failed names the state.
func TestC21CaptureRefusalsWriteNoPicture(t *testing.T) {
	cases := []struct {
		name         string
		design       bool
		fail, failAt string
		change       func(t *testing.T, f captureFixture)
		rule, hint   string
	}{
		{name: "a new commit on the prototype", design: true, rule: "pin_mismatch",
			change: func(t *testing.T, f captureFixture) {
				git(t, f.prototype, "commit", "-q", "--allow-empty", "-m", "feat: more")
			}},
		{name: "a change to a tracked file", design: true, rule: "prototype_dirty",
			change: func(t *testing.T, f captureFixture) {
				if err := os.WriteFile(filepath.Join(f.prototype, "index.html"), []byte("<p>changed</p>\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "a new file", design: true, rule: "prototype_dirty",
			change: func(t *testing.T, f captureFixture) {
				if err := os.WriteFile(filepath.Join(f.prototype, "new.css"), []byte("p {}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "a brief with no design block", rule: "no_design_config"},
		{name: "a state line with no target", design: true, rule: "record_invalid",
			change: func(t *testing.T, f captureFixture) {
				p := filepath.Join(f.room, "docs/design/factorio-x/record.md")
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, bytes.Replace(b, []byte(", target `/fleet`"), nil, 1), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "a capture command that exits non-zero", design: true, fail: "exit", failAt: "/fleet", rule: "capture_failed", hint: "S1.2"},
		{name: "a capture command that writes no PNG", design: true, fail: "text", failAt: "/fleet?sort=name", rule: "capture_failed", hint: "S1.3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newCaptureFixture(t, c.design, c.fail, c.failAt)
			if c.change != nil {
				c.change(t, f)
			}
			exit, env := capture(t, "--prototype", f.prototype)
			if exit != 3 || env["code"] != "SHAPE_REFUSED" || env["rule"] != c.rule {
				t.Fatalf("exit %d, want 3 SHAPE_REFUSED %s\n%v", exit, c.rule, env)
			}
			if hint, _ := env["hint"].(string); !strings.Contains(hint, c.hint) {
				t.Errorf("hint %q names no %s", hint, c.hint)
			}
			if got := f.pictures(t); len(got) != 0 {
				t.Errorf("the refusal wrote pictures %v", keys(got))
			}
		})
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
