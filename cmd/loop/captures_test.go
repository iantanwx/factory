package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// takeFixture is the checkout of a ticket of factorio-x at a clean head. It
// commits the record, the config and the fixed PNG, and the capture command
// copies that PNG by a relative path, so a capture outside the checkout
// fails.
type takeFixture struct {
	checkout, out string
	log           string // the targets the capture command received, one a line
	png           []byte // the one PNG the capture command copies
}

// newTakeFixture makes the fixture. screens is the ticket's screens fact,
// and design false leaves the design block out of the config. The capture
// command fails as fail says at the target failAt: "exit", "text", "none",
// or "" to never fail.
func newTakeFixture(t *testing.T, screens string, design bool, fail, failAt string) takeFixture {
	t.Helper()
	testdata, err := filepath.Abs("testdata/captures")
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(testdata, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	f := takeFixture{checkout: t.TempDir(), out: t.TempDir(), log: filepath.Join(t.TempDir(), "targets"), png: read("fixed.png")}
	cfg := read("config.yaml")
	if design {
		capture := fmt.Sprintf("sh %q %q %q %q", filepath.Join(testdata, "capture.sh"), f.log, fail, failAt)
		cfg = bytes.ReplaceAll(cfg, []byte("CAPTURE"), []byte("'"+capture+"'"))
	} else {
		cfg = cfg[:bytes.Index(cfg, []byte("design:"))]
	}
	facts := map[string]string{"epic": "factorio-x"}
	if screens != "" {
		facts["screens"] = screens
	}
	brief, err := json.Marshal(map[string]any{"run_id": "r-1", "item": map[string]any{"id": "factorio-y", "kind": "ticket", "facts": facts}})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"docs/design/factorio-x/record.md": read("record.md"),
		".loop/config.yaml":                cfg,
		"fixed.png":                        f.png,
		".loop/brief.json":                 brief,
	} {
		p := filepath.Join(f.checkout, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, f.checkout, "init", "-q", "-b", "loop/factorio-y")
	// The server keeps the brief out of git, as in every run's checkout.
	if err := os.WriteFile(filepath.Join(f.checkout, ".git/info/exclude"), []byte(".loop/brief.json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, f.checkout, "add", "-A")
	git(t, f.checkout, "commit", "-q", "-m", "feat: show the fleet")
	t.Chdir(f.checkout)
	return f
}

// take runs `loop captures take --out <f.out>` in the working directory,
// and returns its exit and its envelope.
func (f takeFixture) take(t *testing.T) (int, map[string]any) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	exit := run(context.Background(), []string{"captures", "take", "--out", f.out}, strings.NewReader(""), &stdout, &stderr)
	var env map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\n%s", err, stdout.String())
	}
	return exit, env
}

// files returns each file in --out, by its name.
func (f takeFixture) files(t *testing.T) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(f.out)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(f.out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = b
	}
	return out
}

// targets returns the targets the capture command received, in order.
func (f takeFixture) targets(t *testing.T) []string {
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

// C9: at a clean head, the verb writes <out>/<id>.png for each cited state
// with the capture's bytes, and passes each state's target, in the order
// of the screens fact.
func TestC9CapturesTakeWritesOneCaptureForEachCitedState(t *testing.T) {
	f := newTakeFixture(t, "S1.2,S1.1", true, "", "")
	exit, env := f.take(t)
	if exit != 0 || env["ok"] != true {
		t.Fatalf("exit %d, want 0\n%v", exit, env)
	}
	got := f.files(t)
	for _, name := range []string{"S1.2.png", "S1.1.png"} {
		if !bytes.Equal(got[name], f.png) {
			t.Errorf("%s holds %d bytes, want the fixed PNG's %d bytes", name, len(got[name]), len(f.png))
		}
	}
	if len(got) != 2 {
		t.Errorf("--out holds %v, want S1.2.png and S1.1.png alone", keys(got))
	}
	if want := []string{"/fleet", "/fleet?empty"}; fmt.Sprint(f.targets(t)) != fmt.Sprint(want) {
		t.Errorf("targets %v, want %v", f.targets(t), want)
	}
}

// C9: each refusal exits 3 with SHAPE_REFUSED and its rule, and leaves
// --out with no file. The hint of capture_failed and state_not_in_record
// names the state.
func TestC9CapturesTakeRefusalsLeaveOutEmpty(t *testing.T) {
	cases := []struct {
		name         string
		screens      string
		design       bool
		fail, failAt string
		change       func(t *testing.T, f takeFixture)
		rule, hint   string
	}{
		{name: "a change to a tracked file", screens: "S1.1", design: true, rule: "checkout_dirty",
			change: func(t *testing.T, f takeFixture) {
				if err := os.WriteFile(filepath.Join(f.checkout, "fixed.png"), []byte("changed"), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "a new file", screens: "S1.1", design: true, rule: "checkout_dirty",
			change: func(t *testing.T, f takeFixture) {
				if err := os.WriteFile(filepath.Join(f.checkout, "new.css"), []byte("p {}\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "a cited state the record lacks", screens: "S1.1,S9.9", design: true, rule: "state_not_in_record", hint: "S9.9"},
		{name: "a ticket that cites no state", design: true, rule: "no_screens"},
		{name: "a config with no design block", screens: "S1.1", rule: "no_design_config"},
		{name: "a state line with no target", screens: "S1.1", design: true, rule: "record_invalid",
			change: func(t *testing.T, f takeFixture) {
				p := filepath.Join(f.checkout, "docs/design/factorio-x/record.md")
				b, err := os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, bytes.Replace(b, []byte(", target `/fleet`"), nil, 1), 0o644); err != nil {
					t.Fatal(err)
				}
				git(t, f.checkout, "commit", "-q", "-am", "docs: drop a target")
			}},
		{name: "a capture command that exits non-zero", screens: "S1.1,S1.2", design: true, fail: "exit", failAt: "/fleet", rule: "capture_failed", hint: "S1.2"},
		{name: "a capture command that writes text", screens: "S1.1,S1.3", design: true, fail: "text", failAt: "/fleet?sort=name", rule: "capture_failed", hint: "S1.3"},
		{name: "a capture command that writes no file", screens: "S1.1,S1.2", design: true, fail: "none", failAt: "/fleet", rule: "capture_failed", hint: "S1.2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTakeFixture(t, c.screens, c.design, c.fail, c.failAt)
			if c.change != nil {
				c.change(t, f)
			}
			exit, env := f.take(t)
			if exit != 3 || env["code"] != "SHAPE_REFUSED" || env["rule"] != c.rule {
				t.Fatalf("exit %d, want 3 SHAPE_REFUSED %s\n%v", exit, c.rule, env)
			}
			if hint, _ := env["hint"].(string); !strings.Contains(hint, c.hint) {
				t.Errorf("hint %q names no %s", hint, c.hint)
			}
			if got := f.files(t); len(got) != 0 {
				t.Errorf("the refusal left files %v in --out", keys(got))
			}
		})
	}
}

// C28: in a room checkout, whose brief holds role room and the ticket's
// item facts with screens S1.1, the verb writes <out>/S1.1.png.
func TestC28CapturesTakeInAReviewRoomCheckout(t *testing.T) {
	f := newTakeFixture(t, "S1.1", true, "", "")
	room, err := json.Marshal(map[string]any{"run_id": "room-1", "role": "room", "stage": "review-room",
		"item": map[string]any{"id": "factorio-y", "kind": "ticket", "facts": map[string]string{"epic": "factorio-x", "screens": "S1.1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.checkout, ".loop/brief.json"), room, 0o644); err != nil {
		t.Fatal(err)
	}
	if exit, env := f.take(t); exit != 0 {
		t.Fatalf("exit %d, want 0\n%v", exit, env)
	}
	got := f.files(t)
	if len(got) != 1 || !bytes.Equal(got["S1.1.png"], f.png) {
		t.Fatalf("--out holds %v, want S1.1.png with the fixed PNG alone", keys(got))
	}
}
