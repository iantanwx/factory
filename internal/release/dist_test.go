package release

import (
	"context"
	"os"
	"path/filepath"
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

// TestReleaseWorkflowHoldsNoKey: the workflow builds an unsigned draft and
// holds no signing key (KTD26).
func TestReleaseWorkflowHoldsNoKey(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var wf struct {
		On struct {
			Push struct {
				Tags []string `yaml:"tags"`
			} `yaml:"push"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatal(err)
	}
	if len(wf.On.Push.Tags) == 0 {
		t.Fatal("the workflow does not start on a tag")
	}
	// The header comment explains the offline step; the checks read only
	// what the workflow runs.
	var code []string
	for _, l := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			code = append(code, l)
		}
	}
	text := strings.Join(code, "\n")
	for _, banned := range []string{"secrets.", "RELEASE_KEY", "release-sign", "minisig", " sign "} {
		if strings.Contains(text, banned) {
			t.Errorf("the release workflow holds %q", banned)
		}
	}
	draft := false
	for _, job := range wf.Jobs {
		for _, s := range job.Steps {
			if strings.Contains(s.Run, "gh release create") {
				draft = strings.Contains(s.Run, "--draft")
			}
			if strings.Contains(s.Run, "gh release edit") || strings.Contains(s.Run, "--draft=false") {
				t.Error("the workflow publishes the release")
			}
		}
	}
	if !draft {
		t.Fatal("the workflow does not create a draft release")
	}
}
