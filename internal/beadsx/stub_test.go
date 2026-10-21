package beadsx_test

// Tests on stub binaries. They need no pinned binary, so they run on every
// `go test`.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
)

const versionOK = `if [ "$1" = version ]; then echo '{"data":{"version":"1.3.1"},"schema_version":1}'; exit 0; fi`

// stub writes an executable shell script and returns its path.
func stub(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bd")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func stubClient(t *testing.T, body string) *beadsx.BD {
	t.Helper()
	dir := t.TempDir()
	b, err := beadsx.NewBD(context.Background(), beadsx.BDConfig{
		Binary: stub(t, versionOK+"\n"+body), Workspace: dir, Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNewBDRefusesOtherVersion(t *testing.T) {
	for _, v := range []string{"1.3.0", "1.3.10", "1.3.1-rc1"} {
		bin := stub(t, `echo '{"data":{"version":"`+v+`"},"schema_version":1}'`)
		dir := t.TempDir()
		_, err := beadsx.NewBD(context.Background(), beadsx.BDConfig{Binary: bin, Workspace: dir, Home: dir})
		if !errors.Is(err, beadsx.ErrVersion) {
			t.Fatalf("bd %s: %v, want ErrVersion", v, err)
		}
	}
}

func TestCheckDoltVersionRefusesOther(t *testing.T) {
	bin := stub(t, `echo "dolt version 2.1.9"`)
	if err := beadsx.CheckDoltVersion(context.Background(), bin, nil, t.TempDir()); !errors.Is(err, beadsx.ErrVersion) {
		t.Fatalf("dolt 2.1.9: %v", err)
	}
	bin = stub(t, `echo "dolt version 2.2.0"`)
	if err := beadsx.CheckDoltVersion(context.Background(), bin, nil, t.TempDir()); err != nil {
		t.Fatalf("dolt 2.2.0: %v", err)
	}
}

func TestMalformedOutputIsAnError(t *testing.T) {
	cases := map[string]string{
		"NotJSON":        `echo 'Issue lp-1 ok'`,
		"OtherSchema":    `echo '{"data":[{"id":"lp-1"}],"schema_version":2}'`,
		"NoSchema":       `echo '[{"id":"lp-1"}]'`,
		"WrongShape":     `echo '{"data":{"id":"lp-1"},"schema_version":1}'`,
		"WrongIssue":     `echo '{"data":[{"id":"lp-2"}],"schema_version":1}'`,
		"Truncated":      `printf '{"data":[{"id":"lp-1"'`,
		"MetadataNotMap": `echo '{"data":[{"id":"lp-1","metadata":[1]}],"schema_version":1}'`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			b := stubClient(t, body)
			if _, err := b.Get(context.Background(), "lp-1"); !errors.Is(err, beadsx.ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
	b := stubClient(t, `echo '{"seq":2,"op":"update","issue_id":"lp-1"}'; echo '{"seq":1,"op":"update","issue_id":"lp-1"}'`)
	if _, err := b.Tail(context.Background(), 0, 0); !errors.Is(err, beadsx.ErrMalformed) {
		t.Fatalf("journal out of order: %v", err)
	}
	b = stubClient(t, `echo 'garbage'`)
	if _, err := b.Events(context.Background(), "lp-1"); !errors.Is(err, beadsx.ErrMalformed) {
		t.Fatalf("journal garbage: %v", err)
	}
}

func TestExitMapping(t *testing.T) {
	cases := []struct {
		name, body string
		want       error
	}{
		{"Miss", `echo '{"data":{"error":"1 of 1 issues failed to update"},"schema_version":1}' >&2; exit 13`, beadsx.ErrCASMiss},
		{"Down", `echo 'Error: failed to open database: Dolt server unreachable at /x.sock: dial unix /x.sock: connect: connection refused' >&2; exit 1`, beadsx.ErrUnavailable},
		{"NotFound", `echo '{"data":{"error":"resolving issue: no issue found matching \"lp-1\""},"schema_version":1}' >&2; exit 1`, beadsx.ErrNotFound},
		{"Invalid", `echo '{"data":{"error":"invalid status \"x\""},"schema_version":1}'; exit 1`, beadsx.ErrInvalid},
		{"Other", `echo 'boom' >&2; exit 2`, beadsx.ErrFailed},
		{"Signal", `kill -9 $$`, beadsx.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := stubClient(t, tc.body)
			_, err := b.Update(context.Background(), "lp-1", beadsx.UpdateSpec{IfStatus: "open", Status: "closed"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			var be *beadsx.BDError
			if !errors.As(err, &be) || be.Op != "update" {
				t.Fatalf("err %T is not a *BDError for update", err)
			}
			if tc.want != beadsx.ErrCASMiss && errors.Is(err, beadsx.ErrCASMiss) {
				t.Fatal("only exit 13 is a miss")
			}
		})
	}
}

func TestContextCancelKillsBD(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	// The stub starts a grandchild, so the test also proves the group kill.
	b := stubClient(t, `echo $$ > `+pids+`; sleep 60 & echo $! >> `+pids+`; wait`)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			if data, err := os.ReadFile(pids); err == nil && strings.Count(string(data), "\n") >= 2 {
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	start := time.Now()
	_, err := b.Get(ctx, "lp-1")
	if !errors.Is(err, beadsx.ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want ErrUnavailable and context.Canceled", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
	data, _ := os.ReadFile(pids)
	for _, f := range strings.Fields(string(data)) {
		pid, _ := strconv.Atoi(f)
		deadline := time.Now().Add(5 * time.Second)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				t.Fatalf("pid %d survived the cancel", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestDeadlineIsUnavailable(t *testing.T) {
	b := stubClient(t, `sleep 60`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := b.Update(ctx, "lp-1", beadsx.UpdateSpec{IfStatus: "open", Status: "closed"})
	if !errors.Is(err, beadsx.ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, beadsx.ErrCASMiss) {
		t.Fatalf("err = %v", err)
	}
}

func TestSupervisorConfigChecks(t *testing.T) {
	if _, err := beadsx.NewSupervisor(beadsx.DoltConfig{Binary: "dolt", Dir: "rel"}); !errors.Is(err, beadsx.ErrInvalid) {
		t.Fatalf("relative dir: %v", err)
	}
	long := "/" + strings.Repeat("d", 120)
	if _, err := beadsx.NewSupervisor(beadsx.DoltConfig{Binary: "dolt", Dir: long}); !errors.Is(err, beadsx.ErrInvalid) {
		t.Fatalf("long socket: %v", err)
	}
}

func TestEnsureHomeTurnsMetricsOff(t *testing.T) {
	home := t.TempDir()
	p := filepath.Join(home, ".config", "bd", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("metrics:\n    disabled: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := beadsx.EnsureHome(home); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "disabled: true") {
		t.Fatalf("config.yaml = %q", b)
	}
}
