package beadsx_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The fake in hang mode stands for a server that never gets ready. It keeps
// running until SIGKILL, so a restart that starts it waits in waitReady
// until Stop ends that wait. TestStopDuringARestartDoesNotWaitForTheStart
// rests on this.
func TestFakeDoltHangKeepsRunning(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fake-hang"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fakeDoltBinary(t), "sql-server", "--socket="+filepath.Join(dir, "s.sock"))
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Fatalf("the hanging fake exited within %v of its start: %v\n%s", time.Second, err, out.String())
	case <-time.After(time.Second):
	}
	_ = cmd.Process.Kill()
	<-done
}
