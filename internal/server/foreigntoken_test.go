package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iantanwx/loop/internal/cli"
)

// A test's CLI client never reads a .loop/token that sits above the test's
// working directory. A loop checkout of this repository holds one, and the
// test server never issued it.
func TestTestClientIgnoresTheCallersLoopToken(t *testing.T) {
	outer := t.TempDir()
	if err := cli.WriteTokenFile(outer, cli.TokenFile{Token: "foreign-token", RunID: "r-foreign", Kind: "run"}); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(outer, "internal", "server")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(pkg)
	dir := instanceDir(t)
	start(t, dir, Options{}, true)
	if env, code, _ := call(t, dir, time.Second, nil, "status"); code != 0 {
		t.Fatalf("status from under a foreign .loop/token = %d %v", code, env)
	}
}
