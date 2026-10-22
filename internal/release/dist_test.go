package release

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeBinaries(t *testing.T, dir, version string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range Platforms {
		goos, goarch, _ := strings.Cut(p, "/")
		writeFile(t, filepath.Join(dir, AssetName(goos, goarch)), "binary "+version+" "+p)
	}
}

// TestSignedDistIsAcceptedByUpgrade is the release path end to end: CI
// writes the dist unsigned, the operator signs it offline, and upgrade
// accepts the signature.
func TestSignedDistIsAcceptedByUpgrade(t *testing.T) {
	ctx := context.Background()
	w := newInstance(t)
	base := t.TempDir()
	dist := filepath.Join(base, "v1.1.0")
	writeBinaries(t, dist, "v1.1.0")
	if err := WriteDist(ctx, dist, DistOptions{Version: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dist, SignatureFile)); err == nil {
		t.Fatal("WriteDist signed the release")
	}
	pub, sec := mustKey(t)
	if err := SignDist(dist, sec); err != nil {
		t.Fatal(err)
	}
	if v, err := VerifyDist(dist, []PublicKey{pub}); err != nil || v != "v1.1.0" {
		t.Fatalf("verify = %q %v", v, err)
	}

	// The release pins this binary's bd and dolt; the test instance runs
	// synthetic ones, so it pins those.
	meta, err := ReadMetadata(filepath.Join(dist, MetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Pins) != len(pinTable) || meta.Schema < 1 {
		t.Fatalf("metadata = %+v", meta)
	}
	meta.Pins = w.pins.pins
	writeFile(t, filepath.Join(dist, MetadataFile), string(meta.Encode()))
	if err := WriteDistChecksums(dist); err != nil {
		t.Fatal(err)
	}
	if err := SignDist(dist, sec); err != nil {
		t.Fatal(err)
	}

	u := &Upgrader{Host: Host{Layout: w.layout, Service: w.svc, Server: w.server},
		Source: Source{Base: base}, Keys: []PublicKey{pub}, Platform: testPlatform, Prov: w.pins.prov, Self: w.meta}
	rep, err := u.Run(ctx, "v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Changed || rep.To != "v1.1.0" {
		t.Fatalf("report = %+v", rep)
	}

	// A tampered binary fails verification.
	writeFile(t, filepath.Join(dist, AssetName("darwin", "arm64")), "evil")
	if _, err := VerifyDist(dist, []PublicKey{pub}); err == nil {
		t.Fatal("a tampered dist verified")
	}
	if err := SignDist(dist, sec); err == nil {
		t.Fatal("sign accepted a dist whose assets differ from their checksums")
	}
}

func TestEmbeddedKeysParse(t *testing.T) {
	if _, err := TrustedKeys(); err != nil {
		t.Fatal(err)
	}
}

// TestCIReleasesAfterBothJobs: the release job runs on main only, after
// gates and acceptance pass. It reads the key from its one secret, signs
// and verifies against the trusted keys, and publishes after that (KTD26).
func TestCIReleasesAfterBothJobs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		Jobs map[string]struct {
			If    string   `yaml:"if"`
			Needs []string `yaml:"needs"`
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	job, ok := wf.Jobs["release"]
	if !ok {
		t.Fatal("ci.yml has no release job")
	}
	if !strings.Contains(job.If, "refs/heads/main") || !slices.Contains(job.Needs, "gates") || !slices.Contains(job.Needs, "acceptance") {
		t.Fatalf("the release job runs without main and both checks: if %q, needs %v", job.If, job.Needs)
	}
	if n := strings.Count(string(raw), "secrets."); n != 1 || !strings.Contains(string(raw), "${{ secrets.LOOP_RELEASE_KEY }}") {
		t.Fatalf("ci.yml reads %d secrets; it reads LOOP_RELEASE_KEY only", n)
	}
	signed, published := -1, -1
	for i, st := range job.Steps {
		if strings.Contains(st.Run, "make release-sign") {
			signed = i
			if st.Env["LOOP_RELEASE_KEY"] == "" {
				t.Error("the signing step does not get the key")
			}
		}
		if strings.Contains(st.Run, "gh release create") {
			published = i
			if strings.Contains(st.Run, "--draft") || !strings.Contains(st.Run, "install.sh") {
				t.Errorf("the release is not published with install.sh: %s", st.Run)
			}
		}
		if strings.Contains(st.Run, "set -x") {
			t.Errorf("a step may print the key: %s", st.Run)
		}
	}
	if signed < 0 || published < 0 || signed > published {
		t.Fatalf("sign step %d, publish step %d: the job must sign before it publishes", signed, published)
	}
}
