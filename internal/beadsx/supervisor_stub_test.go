package beadsx_test

// Supervisor tests on a fake dolt. The test binary is the fake: TestMain
// runs it when LOOP_FAKE_DOLT is set. The fake serves the MySQL handshake
// on the socket and ignores SIGTERM, so only a SIGKILL stops it.

import (
	"context"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/beadsx"
)

func TestMain(m *testing.M) {
	if os.Getenv("LOOP_FAKE_DOLT") == "1" {
		fakeDolt(os.Args[1:])
		return
	}
	os.Exit(m.Run())
}

// fakeDolt answers `version`, records `sql`, and serves `sql-server`. It
// writes what it received into its working directory, which the
// supervisor sets to the data directory.
func fakeDolt(args []string) {
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "version":
		_, _ = os.Stdout.WriteString("dolt version " + beadsx.PinnedDolt + "\n")
	case "sql":
		in, _ := io.ReadAll(os.Stdin)
		_ = os.WriteFile("fake-sql-stdin", in, 0o600)
		_ = os.WriteFile("fake-sql-args", []byte(strings.Join(args, " ")), 0o600)
	case "sql-server":
		signal.Ignore(syscall.SIGTERM)
		_ = os.WriteFile("fake-root-password", []byte(os.Getenv("DOLT_ROOT_PASSWORD")), 0o600)
		if _, err := os.Stat("fake-hang"); err == nil {
			select {}
		}
		var socket string
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--socket="); ok {
				socket = v
			}
		}
		l, err := net.Listen("unix", socket)
		if err != nil {
			os.Exit(1)
		}
		for {
			c, err := l.Accept()
			if err != nil {
				os.Exit(1)
			}
			_, _ = c.Write([]byte{1, 0, 0, 0, 10})
			_ = c.Close()
		}
	}
}

// fakeDoltDir makes a short state directory, because a unix socket path
// has a length limit.
func fakeDoltDir(t *testing.T) beadsx.Layout {
	t.Helper()
	dir, err := os.MkdirTemp("", "bx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return beadsx.Layout{StateDir: dir}
}

func fakeDoltBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return stub(t, "LOOP_FAKE_DOLT=1 exec '"+self+"' \"$@\"")
}

func startFake(t *testing.T, layout beadsx.Layout) *beadsx.Supervisor {
	t.Helper()
	sup, err := beadsx.NewSupervisor(beadsx.DoltConfig{Binary: fakeDoltBinary(t), Dir: layout.DoltDir(),
		MinBackoff: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = sup.Stop(ctx)
	})
	return sup
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d survived Stop", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A server that ignores SIGTERM gets SIGKILL when the stop context ends.
func TestStopKillsADoltThatIgnoresSIGTERM(t *testing.T) {
	sup := startFake(t, fakeDoltDir(t))
	pid := sup.PID()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	begin := time.Now()
	if err := sup.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("Stop took %s", took)
	}
	waitGone(t, pid)
}

// A stop during a restart ends the restart's wait for readiness at once.
func TestStopDuringARestartDoesNotWaitForTheStart(t *testing.T) {
	layout := fakeDoltDir(t)
	sup := startFake(t, layout)
	first := sup.PID()
	if err := os.WriteFile(filepath.Join(layout.DoltDir(), "data", "fake-hang"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = syscall.Kill(first, syscall.SIGKILL)
	deadline := time.Now().Add(10 * time.Second)
	for sup.PID() == 0 || sup.PID() == first {
		if time.Now().After(deadline) {
			t.Fatal("the supervisor started no new server")
		}
		time.Sleep(10 * time.Millisecond)
	}
	second := sup.PID()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	begin := time.Now()
	if err := sup.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("Stop took %s during a restart", took)
	}
	waitGone(t, second)
}

// The first start makes a root password, keeps it at 0600 outside Dolt's
// data directory, and hands it to the server in its environment. A later
// start keeps the same password.
func TestStartMakesAndKeepsTheRootPassword(t *testing.T) {
	layout := fakeDoltDir(t)
	sup := startFake(t, layout)
	if sup.PasswordFile() != layout.DoltPassword() {
		t.Fatalf("password file %s, layout names %s", sup.PasswordFile(), layout.DoltPassword())
	}
	st, err := os.Stat(layout.DoltPassword())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("password file: %v, mode %v", err, st.Mode().Perm())
	}
	pw := readTrim(t, layout.DoltPassword())
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(pw) {
		t.Fatal("the password file holds no 64-digit hex password")
	}
	if got := readTrim(t, filepath.Join(layout.DoltDir(), "data", "fake-root-password")); got != pw {
		t.Fatal("the server did not get the password in DOLT_ROOT_PASSWORD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = sup.Stop(ctx)
	startFake(t, layout)
	if readTrim(t, layout.DoltPassword()) != pw {
		t.Fatal("a second start changed the password")
	}
}

// A data directory that a server used before holds root in its privileges
// file, and Dolt then ignores DOLT_ROOT_PASSWORD. The first start sets the
// password there through stdin, never through argv.
func TestStartSetsThePasswordOfAnExistingRoot(t *testing.T) {
	layout := fakeDoltDir(t)
	cfg := filepath.Join(layout.DoltDir(), "data", ".doltcfg")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, "privileges.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	startFake(t, layout)
	pw := readTrim(t, layout.DoltPassword())
	data := filepath.Join(layout.DoltDir(), "data")
	stdin := readTrim(t, filepath.Join(data, "fake-sql-stdin"))
	if stdin != "ALTER USER 'root'@'localhost' IDENTIFIED BY '"+pw+"';" {
		t.Fatal("dolt sql did not get the ALTER USER for the stored password on stdin")
	}
	if strings.Contains(readTrim(t, filepath.Join(data, "fake-sql-args")), pw) {
		t.Fatal("the password reached argv")
	}
}

func TestBDReadsThePasswordFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := beadsx.NewBD(context.Background(), beadsx.BDConfig{Binary: stub(t, versionOK), Workspace: dir, Home: dir,
		PasswordFile: filepath.Join(dir, "absent")}); err == nil {
		t.Fatal("NewBD ran with a missing password file")
	}
}

func readTrim(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
