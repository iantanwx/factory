package release

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/server"
)

func (w *instanceWorld) next(version string) Metadata {
	m := w.meta
	m.Version = version
	return m
}

func wantRefusal(t *testing.T, err error, code, rule string) {
	t.Helper()
	var re *Error
	if !errors.As(err, &re) || re.Code != code || (rule != "" && re.Rule != rule) {
		t.Fatalf("want %s %s, got %v", code, rule, err)
	}
}

func TestUpgradeRefusalsChangeNothing(t *testing.T) {
	cases := []struct {
		name, code, rule string
		version          string
		setup            func(t *testing.T, w *instanceWorld, rw *releaseWorld)
	}{
		{name: "bad signature", code: CodeSignatureInvalid, version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				_, other := mustKey(t)
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), &other, "")
			}},
		{name: "tampered checksum file", code: CodeSignatureInvalid, version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
				evil := []byte("evil binary")
				sums := Checksums{AssetName("linux", "amd64"): sha256Hex(evil), MetadataFile: sha256Hex(w.next("v1.1.0").Encode())}
				rw.replace("v1.1.0", AssetName("linux", "amd64"), evil)
				rw.replace("v1.1.0", ChecksumsFile, sums.Bytes())
			}},
		{name: "binary differs from its checksum", code: CodeChecksumMismatch, version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
				rw.replace("v1.1.0", AssetName("linux", "amd64"), []byte("evil binary"))
			}},
		{name: "older release served as newer", code: CodeVersionRefused, rule: "trusted_version_mismatch", version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				// v1.0.5 was validly signed. An attacker serves it as v1.1.0
				// with release.json rewritten.
				rw.publish(w.next("v1.1.0"), []byte("binary v1.0.5"), nil, TrustedComment("v1.0.5"))
			}},
		{name: "below the highest installed", code: CodeVersionRefused, rule: "below_highest", version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				if err := w.layout.Record("v1.2.0"); err != nil {
					t.Fatal(err)
				}
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
			}},
		{name: "minimum from-version", code: CodeVersionRefused, rule: "below_min_from", version: "v2.0.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				m := w.next("v2.0.0")
				m.MinFrom = "v1.5.0"
				rw.publish(m, []byte("binary v2.0.0"), nil, "")
			}},
		{name: "pinned dolt differs", code: CodeChecksumMismatch, version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				m := w.next("v1.1.0")
				m.Pins = slices.Clone(m.Pins)
				m.Pins[1].Version, m.Pins[1].BinarySHA256 = "2.3.0", strings.Repeat("0", 64)
				rw.publish(m, []byte("binary v1.1.0"), nil, "")
			}},
		{name: "no such release", code: "NOT_FOUND", version: "v9.0.0",
			setup: func(*testing.T, *instanceWorld, *releaseWorld) {}},
		{name: "not a version", code: "USAGE", version: "latest",
			setup: func(*testing.T, *instanceWorld, *releaseWorld) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newInstance(t)
			rw := newReleaseWorld(t)
			tc.setup(t, w, rw)
			before := tree(t, w.dir)
			_, err := w.upgrader(rw).Run(context.Background(), tc.version)
			wantRefusal(t, err, tc.code, tc.rule)
			if ev := w.svc.list(); len(ev) != 0 {
				t.Fatalf("a refused upgrade acted: %q", ev)
			}
			if after := tree(t, w.dir); after != before {
				t.Fatalf("a refused upgrade changed the instance:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestUpgradeDrainsSnapshotsSwapsAndRestarts(t *testing.T) {
	w := newInstance(t)
	rw := newReleaseWorld(t)
	rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
	var atStop, atStart struct {
		current   string
		snapshots int
	}
	look := func(into *struct {
		current   string
		snapshots int
	}) {
		into.current, _ = w.layout.CurrentVersion()
		entries, _ := os.ReadDir(w.layout.Snapshots())
		into.snapshots = len(entries)
	}
	w.svc.onStop = func() { look(&atStop) }
	w.svc.onStart = func() { look(&atStart) }

	rep, err := w.upgrader(rw).Run(context.Background(), "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.svc.list(); !slices.Equal(got, []string{"drain", "stop", "start"}) {
		t.Fatalf("order = %q", got)
	}
	if atStop.current != "v1.0.0" || atStop.snapshots != 0 {
		t.Fatalf("at stop: %+v", atStop)
	}
	if atStart.current != "v1.1.0" || atStart.snapshots != 1 {
		t.Fatalf("at start: %+v", atStart)
	}
	if rep.From != "v1.0.0" || rep.To != "v1.1.0" || rep.Snapshot == "" || !rep.Started {
		t.Fatalf("report = %+v", rep)
	}
	if _, err := os.Stat(rep.Snapshot); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(w.dir, "current", BinaryName)); string(data) != "binary v1.1.0" {
		t.Fatalf("current/loop = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(w.dir, server.PreviousVersionFile)); strings.TrimSpace(string(data)) != "v1.0.0" {
		t.Fatalf("previous_version = %q", data)
	}
	if st, _ := w.layout.ReadState(); st.Highest != "v1.1.0" {
		t.Fatalf("highest = %q", st.Highest)
	}
	// The signed assets stay beside the binary for audit.
	for _, f := range []string{MetadataFile, ChecksumsFile, SignatureFile} {
		if _, err := os.Stat(filepath.Join(w.layout.VersionDir("v1.1.0"), f)); err != nil {
			t.Error(err)
		}
	}
	// The same version again is nothing to do.
	if rep, err := w.upgrader(rw).Run(context.Background(), "v1.1.0"); err != nil || rep.Changed {
		t.Fatalf("a repeat upgrade: %+v %v", rep, err)
	}
}

func TestUpgradeSkipsTheDrainWhenTheServerIsDown(t *testing.T) {
	w := newInstance(t)
	w.server.up = false
	rw := newReleaseWorld(t)
	rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
	rep, err := w.upgrader(rw).Run(context.Background(), "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Drain.Skipped == "" || slices.Contains(w.svc.list(), "drain") {
		t.Fatalf("drain = %+v, events %q", rep.Drain, w.svc.list())
	}
}

func TestUpgradeWithAPinBumpProvisionsAndIsMarkedNoRollback(t *testing.T) {
	w := newInstance(t)
	rw := newReleaseWorld(t)
	bump := newPinWorld(t, "1.4.0")
	m := w.next("v1.1.0")
	m.Pins = []Pin{bump.pins[0], w.pins.pins[1]}
	rw.publish(m, []byte("binary v1.1.0"), nil, "")
	u := w.upgrader(rw)
	u.Prov = bump.prov
	rep, err := u.Run(context.Background(), "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(CheckPins(m.Pins, filepath.Join(w.dir, BinDir))) != 0 {
		t.Fatal("the bumped bd is not in place")
	}
	if len(rep.NoRollback) == 0 {
		t.Fatalf("a pin bump must be no-rollback: %+v", rep)
	}
}
