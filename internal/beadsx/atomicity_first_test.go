//go:build beads

package beadsx

// This file is the first test of U3. It runs the pinned bd and dolt by hand,
// with no adapter code, so the atomicity result stands on its own.

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestFirstAtomicityRaw(t *testing.T) {
	bd := envOr("LOOP_BD", "/home/operator/.cache/loop-dev/bin/bd")
	dolt := envOr("LOOP_DOLT", "/home/operator/.cache/loop-dev/bin/dolt")
	dir, err := os.MkdirTemp("", "bxa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	home := filepath.Join(dir, "h")
	data := filepath.Join(dir, "d")
	ws := filepath.Join(dir, "w")
	for _, d := range []string{filepath.Join(home, ".config", "bd"), data, ws} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "bd", "config.yaml"),
		[]byte("metrics:\n    disabled: true\n    notice_shown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"DOLT_ROOT_PATH=" + filepath.Join(dir, "r"), "DO_NOT_TRACK=1",
		"BEADS_ACTOR=loop", "BEADS_DOLT_AUTO_START=0", "PATH=/usr/bin:/bin",
	}
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	srv := exec.Command(dolt, "sql-server", "--socket="+sock, "-H", "127.0.0.1",
		"-P", strconv.Itoa(port), "--data-dir", data)
	srv.Dir = data
	srv.Env = env
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Process.Kill(); _, _ = srv.Process.Wait() })
	for i := 0; ; i++ {
		if c, err := net.Dial("unix", sock); err == nil {
			_ = c.Close()
			break
		}
		if i > 100 {
			t.Fatal("dolt did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	run := func(args ...string) ([]byte, int) {
		cmd := exec.Command(bd, args...)
		cmd.Dir = ws
		cmd.Env = append(append([]string(nil), env...), "BEADS_DIR="+filepath.Join(ws, ".beads"))
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		code := 0
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		t.Logf("bd %v -> %d %s", args, code, errb.String())
		return out.Bytes(), code
	}
	if _, c := run("init", "--server", "--external", "--server-socket", sock, "--prefix", "at",
		"--non-interactive", "-q", "--skip-agents", "--skip-hooks", "--role", "maintainer"); c != 0 {
		t.Fatal("init failed")
	}
	if _, c := run("config", "set", "status.custom", "implementing:wip"); c != 0 {
		t.Fatal("status config failed")
	}
	out, c := run("create", "atomic", "--json")
	if c != 0 {
		t.Fatal("create failed")
	}
	var created struct{ ID string }
	if err := json.Unmarshal(out, &created); err != nil {
		t.Fatal(err)
	}
	if _, c := run("update", created.ID, "--if-status", "open", "--status", "implementing",
		"--metadata", `{"loop.seq":"1","loop.key":"k1"}`, "--json"); c != 0 {
		t.Fatalf("conditional update exit %d", c)
	}
	out, _ = run("show", created.ID, "--json")
	var got []struct {
		Status   string
		Metadata map[string]any
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got) != 1 {
		t.Fatalf("show: %v %s", err, out)
	}
	if got[0].Status != "implementing" || got[0].Metadata["loop.seq"] != "1" || got[0].Metadata["loop.key"] != "k1" {
		t.Fatalf("one update did not set status and metadata together: %+v", got[0])
	}
	// A miss writes nothing and exits 13.
	if _, c := run("update", created.ID, "--if-status", "open", "--status", "closed",
		"--metadata", `{"loop.seq":"2"}`, "--json"); c != 13 {
		t.Fatalf("miss exit %d, want 13", c)
	}
	out, _ = run("show", created.ID, "--json")
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got[0].Status != "implementing" || got[0].Metadata["loop.seq"] != "1" {
		t.Fatalf("miss wrote something: %+v", got[0])
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
