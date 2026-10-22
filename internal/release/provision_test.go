package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/hexid"
)

// tarGz builds a gzip tar archive with one file.
func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pinWorld serves synthetic bd and dolt archives from a local server.
type pinWorld struct {
	srv   *httptest.Server
	files map[string][]byte
	pins  []Pin
	prov  Provisioner
}

func newPinWorld(t *testing.T, bdVersion string) *pinWorld {
	t.Helper()
	w := &pinWorld{files: map[string][]byte{}}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		data, ok := w.files[r.URL.Path]
		if !ok {
			http.NotFound(rw, r)
			return
		}
		_, _ = rw.Write(data)
	}))
	t.Cleanup(w.srv.Close)
	for _, p := range []struct{ name, version, member string }{
		{"bd", bdVersion, "bd"}, {"dolt", "2.2.0", "dolt-linux-amd64/bin/dolt"},
	} {
		bin := []byte("#!/bin/sh\necho " + p.name + " " + p.version + "\n")
		archive := tarGz(t, p.member, bin)
		urlPath := "/" + p.name + "-" + p.version + ".tar.gz"
		w.files[urlPath] = archive
		w.pins = append(w.pins, Pin{Name: p.name, Version: p.version, Platform: "linux/amd64",
			URL: urlPath, Member: p.member, ArchiveSHA256: hexid.SHA256(archive), BinarySHA256: hexid.SHA256(bin)})
	}
	w.prov = Provisioner{HTTP: w.srv.Client(), URL: func(p Pin) string { return w.srv.URL + p.URL }}
	return w
}

func TestProvisionFetchesAndKeeps(t *testing.T) {
	w := newPinWorld(t, "1.3.1")
	bin := filepath.Join(t.TempDir(), "bin")
	got, err := w.prov.Provision(context.Background(), w.pins, bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].Fetched || !got[1].Fetched {
		t.Fatalf("provisioned = %+v", got)
	}
	if drift := CheckPins(w.pins, bin); len(drift) != 0 {
		t.Fatalf("drift after provisioning: %v", drift)
	}
	info, err := os.Stat(filepath.Join(bin, "bd"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("bd mode: %v %v", info, err)
	}
	// A second run keeps the binaries in place and fetches nothing.
	w.files = map[string][]byte{}
	got, err = w.prov.Provision(context.Background(), w.pins, bin)
	if err != nil || got[0].Fetched || got[1].Fetched {
		t.Fatalf("second provision: %+v %v", got, err)
	}
}

func TestProvisionRefusesAChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	t.Run("archive", func(t *testing.T) {
		w := newPinWorld(t, "1.3.1")
		w.files[w.pins[1].URL] = tarGz(t, w.pins[1].Member, []byte("a different dolt"))
		bin := filepath.Join(t.TempDir(), "bin")
		_, err := w.prov.Provision(ctx, w.pins, bin)
		if !errors.Is(err, ErrPinMismatch) {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(bin); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a refused provisioning wrote a binary")
		}
	})
	t.Run("binary", func(t *testing.T) {
		w := newPinWorld(t, "1.3.1")
		// The archive matches its pin, and the binary inside does not.
		other := tarGz(t, w.pins[0].Member, []byte("another bd"))
		w.files[w.pins[0].URL] = other
		w.pins[0].ArchiveSHA256 = hexid.SHA256(other)
		_, err := w.prov.Provision(ctx, w.pins, filepath.Join(t.TempDir(), "bin"))
		if !errors.Is(err, ErrPinMismatch) || !strings.Contains(err.Error(), "binary") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("installed", func(t *testing.T) {
		w := newPinWorld(t, "1.3.1")
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "bd"), []byte("tampered"), 0o755); err != nil {
			t.Fatal(err)
		}
		drift := CheckPins(w.pins, bin)
		if len(drift) != 2 || !strings.Contains(drift[0], "is not the pinned bd") || !strings.Contains(drift[1], "missing") {
			t.Fatalf("drift = %v", drift)
		}
	})
}

func TestPinsForRefusesAnUnmeasuredPlatform(t *testing.T) {
	for _, p := range []string{"linux/amd64", "linux/arm64", "darwin/arm64"} {
		if _, err := PinsFor(pinTable, p); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
	}
	unmeasured := []Pin{{Name: "bd", Version: "1.3.1", Platform: "linux/riscv64", ArchiveSHA256: strings.Repeat("a", 64)}}
	if _, err := PinsFor(unmeasured, "linux/riscv64"); err == nil || !strings.Contains(err.Error(), "record both") {
		t.Fatalf("linux/riscv64: %v", err)
	}
	if _, err := PinsFor(pinTable, "windows/amd64"); err == nil {
		t.Fatal("windows has no pin")
	}
}

// TestPinTableMatchesTheEvidence keeps the embedded linux/amd64 hashes
// equal to the binaries the beads contract ran against.
func TestPinTableMatchesTheEvidence(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "evidence", "beads-contract.md"))
	if err != nil {
		t.Fatal(err)
	}
	pins, err := PinsFor(pinTable, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range pins {
		if !strings.Contains(string(doc), p.BinarySHA256) {
			t.Errorf("%s %s: the evidence does not record %s", p.Name, p.Version, p.BinarySHA256)
		}
	}
}
