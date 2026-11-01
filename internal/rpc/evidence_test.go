package rpc

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/iantanwx/loop/internal/cli"
)

// C24: loop qa report --evidence sends the bytes of each file under the
// directory, by its path from the directory, through the socket. A
// screenshot over gRPC's default bound of 4 MiB still reaches the verb.
func TestC24QAReportSendsTheBytesOfEachEvidenceFile(t *testing.T) {
	h := newHarness(t)
	var got map[string][]byte
	h.handle("qa report", func(_ context.Context, call *Call) cli.Result {
		files, err := cli.EvidenceFiles(call.Inv)
		if err != nil {
			return cli.Reject(cli.CodeUsage, "bad_evidence", err.Error())
		}
		got = files
		return cli.Found(map[string]any{"files": len(files)})
	})
	h.start()
	checkout := h.issue("run-qa", cli.PrincipalRun, "T-A")
	dir := t.TempDir()
	shot := bytes.Repeat([]byte("\x89PNG"), 5<<20/4)
	want := map[string][]byte{"web/save-390.png": shot, "web/console.log": []byte("GET /strategies 200\n")}
	for p, data := range want {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(report, []byte(`{"head":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env, code := h.run(checkout, "qa", "report", "T-A", "--head", "x", "--file", report, "--evidence", dir)
	if code != 0 {
		t.Fatalf("qa report = exit %d %v, want 0", code, env)
	}
	if !maps.EqualFunc(got, want, bytes.Equal) {
		t.Fatalf("the verb got %d files, want exactly %d with their bytes", len(got), len(want))
	}
}
