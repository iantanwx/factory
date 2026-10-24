package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/hexid"
	"github.com/iantanwx/loop/internal/pack"
	"github.com/iantanwx/loop/internal/server"
)

func (w *instanceWorld) next(version string) Metadata {
	m := w.meta
	m.Version = version
	return m
}

// serveSkill serves archive as the skill archive of the pin name.
func (w *releaseWorld) serveSkill(name string, archive []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files["/skills/"+name] = archive
}

func (w *releaseWorld) remove(version, asset string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.files, "/"+version+"/"+asset)
}

// pinFile is a third-party.json that holds pins.
func pinFile(t *testing.T, pins ...pack.ThirdParty) []byte {
	t.Helper()
	data, err := json.Marshal(map[string][]pack.ThirdParty{"skills": pins})
	if err != nil {
		t.Fatal(err)
	}
	return data
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
				sums := Checksums{AssetName("linux", "amd64"): hexid.SHA256(evil), MetadataFile: hexid.SHA256(w.next("v1.1.0").Encode())}
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
		// C15: the target's skills are checked before anything changes.
		{name: "a skill fails its tree hash", code: CodeChecksumMismatch, rule: "skill_mismatch", version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				rw.thirdParty = pinFile(t, demoPin(treeHash(t, demoTree(demoSkill))))
				rw.serveSkill("demo", skillArchive(t, demoMembers(demoSkill+"\nAn edit upstream.\n")...))
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
			}},
		{name: "a skill archive cannot be read", code: CodeReleaseUnavailable, rule: "skill_fetch_failed", version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				rw.thirdParty = pinFile(t, demoPin(treeHash(t, demoTree(demoSkill))))
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
			}},
		{name: "the release has no third-party.json", code: "NOT_FOUND", rule: "no_release", version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
				rw.remove("v1.1.0", "third-party.json")
			}},
		{name: "third-party.json differs from checksums.txt", code: CodeChecksumMismatch, rule: "third_party_checksum", version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
				rw.replace("v1.1.0", "third-party.json", []byte(`{"skills": []} `))
			}},
		{name: "third-party.json holds a bad pin", code: CodeChecksumMismatch, rule: "bad_third_party", version: "v1.1.0",
			setup: func(t *testing.T, w *instanceWorld, rw *releaseWorld) {
				rw.thirdParty = []byte(`{"skills": [{"name": "Demo"}]}`)
				rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
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

// TestC15UpgradePlacesTheTargetSkills: a release whose skills all match
// upgrades, and versions/<v>/skills/ then holds each one. The set lands
// after the stop and before current moves.
func TestC15UpgradePlacesTheTargetSkills(t *testing.T) {
	w := newInstance(t)
	rw := newReleaseWorld(t)
	pin := demoPin(treeHash(t, demoTree(demoSkill)))
	rw.thirdParty = pinFile(t, pin)
	rw.serveSkill("demo", skillArchive(t, demoMembers(demoSkill)...))
	rw.publish(w.next("v1.1.0"), []byte("binary v1.1.0"), nil, "")
	skill := filepath.Join(w.dir, "versions", "v1.1.0", "skills", "demo", "SKILL.md")
	var atStop, atStart string
	look := func(into *string) {
		cur, _ := w.layout.CurrentVersion()
		_, err := os.Stat(skill)
		*into = fmt.Sprintf("current %s, skill placed %t", cur, err == nil)
	}
	w.svc.onStop = func() { look(&atStop) }
	w.svc.onStart = func() { look(&atStart) }

	rep, err := w.upgrader(rw).Run(context.Background(), "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if atStop != "current v1.0.0, skill placed false" || atStart != "current v1.1.0, skill placed true" {
		t.Fatalf("at stop: %s; at start: %s", atStop, atStart)
	}
	if !slices.Equal(rep.Skills, []string{"demo"}) {
		t.Fatalf("report skills = %q", rep.Skills)
	}
	if data, err := os.ReadFile(skill); err != nil || string(data) != demoSkill {
		t.Fatalf("SKILL.md = %q %v", data, err)
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(skill), "scripts", "run"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("scripts/run: %v %v", info, err)
	}
	if drift := CheckSkills([]pack.ThirdParty{pin}, filepath.Join(w.dir, "versions", "v1.1.0", "skills")); len(drift) != 0 {
		t.Fatalf("drift = %q", drift)
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
