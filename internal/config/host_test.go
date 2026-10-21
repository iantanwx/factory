package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validHost = `
state_dir: /var/lib/loop
store:
  path: /var/lib/loop/loop.db
  identity: devbox-1
key_file: /etc/loop/keys/kek
agent_host:
  kind: orca
  binary: /usr/local/bin/orca
harnesses:
  - name: claude
    binary: /usr/local/bin/claude
    config_dir: /home/op/.claude
credentials:
  github_app:
    app_id: 12345
    private_key_secret: GITHUB_APP_PRIVATE_KEY
  mergify_token_secret: MERGIFY_TOKEN
  trackers:
    linear:
      token_secret: LINEAR_TOKEN
vaults: [Loop, Shared]
throughput:
  implementer: {cap: 4, schedule: "3,18,33,48 * * * *"}
  code-reviewer: {cap: 8, schedule: "*/5 * * * *"}
  doc-reviewer: {cap: 10, schedule: "26,56 * * * *"}
  reviewers: {cap: 10}
  amender: {cap: 2, schedule: "16,46 * * * *"}
  spec-author: {cap: 2, schedule: "25,55 * * * *"}
  decomposer: {cap: 2, schedule: "10,40 * * * *"}
  fixer: {cap: 1, schedule: "14,44 * * * *"}
  reproducer: {cap: 2, schedule: "12,42 * * * *"}
  intake: {cap: 8, schedule: "20,50 * * * *"}
  merge-handoff: {cap: 1, schedule: "*/5 * * * *"}
  dependency-advance: {cap: 1, schedule: "*/5 * * * *"}
  decision-timers: {cap: 1, schedule: "* * * * *"}
run_budget: 2h
quiet_period: 15m
projects:
  - id: alpha
    repo: iantanwx/alpha
    path: /srv/loop/alpha
`

func TestParseHostValid(t *testing.T) {
	cfg, err := ParseHost([]byte(validHost))
	if err != nil {
		t.Fatalf("ParseHost: %v", err)
	}
	if cfg.RunBudget != 2*time.Hour || cfg.QuietPeriod != 15*time.Minute {
		t.Fatalf("durations = %v, %v", cfg.RunBudget, cfg.QuietPeriod)
	}
	if !cfg.VaultDeclared("Loop") || cfg.VaultDeclared("Private") {
		t.Fatalf("vault declaration is wrong: %v", cfg.Vaults)
	}
	refs := cfg.CredentialRefs()
	want := []CredentialRef{
		{Purpose: "github_app.private_key", Name: "GITHUB_APP_PRIVATE_KEY"},
		{Purpose: "mergify_token", Name: "MERGIFY_TOKEN"},
		{Purpose: "trackers.linear.token", Name: "LINEAR_TOKEN"},
	}
	if len(refs) != len(want) {
		t.Fatalf("CredentialRefs = %v, want %v", refs, want)
	}
	for i := range want {
		if refs[i] != want[i] {
			t.Fatalf("CredentialRefs[%d] = %v, want %v", i, refs[i], want[i])
		}
	}
}

func TestParseHostRefusesUnknownField(t *testing.T) {
	_, err := ParseHost([]byte(validHost + "organisation: acme\n"))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "organisation") {
		t.Fatalf("err = %v, want an unknown-field refusal that names organisation", err)
	}
}

// Plaintext host config holds no credential value (KTD15, KTD17). A value
// field is unknown, and a reference that is not a store name is refused.
// Neither refusal echoes the value.
func TestParseHostRefusesCredentialValues(t *testing.T) {
	const pem = "-----BEGIN RSA PRIVATE KEY-----MIIEowIBAAKCAQEA"
	cases := map[string]string{
		"value field": strings.Replace(validHost, "private_key_secret: GITHUB_APP_PRIVATE_KEY",
			"private_key: \""+pem+"\"", 1),
		"value in reference": strings.Replace(validHost, "MERGIFY_TOKEN", "mgf_9f8e7d6c5b4a39281706", 1),
		"pem in reference": strings.Replace(validHost, "token_secret: LINEAR_TOKEN",
			"token_secret: \""+pem+"\"", 1),
		"value in wrong type": strings.Replace(validHost, "app_id: 12345", "app_id: \"lin_api_SECRETVALUE\"", 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseHost([]byte(doc))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			for _, secret := range []string{pem, "MIIEow", "mgf_9f8e7d6c5b4a39281706", "lin_api", "SECRETV"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error echoes part of a credential value")
				}
			}
		})
	}
}

func TestParseHostRefusesBadValues(t *testing.T) {
	cases := map[string]string{
		"relative state dir":  strings.Replace(validHost, "state_dir: /var/lib/loop", "state_dir: var/lib", 1),
		"negative budget":     strings.Replace(validHost, "run_budget: 2h", "run_budget: -1h", 1),
		"duplicate vault":     strings.Replace(validHost, "[Loop, Shared]", "[Loop, Loop]", 1),
		"duplicate key":       validHost + "run_budget: 3h\n",
		"two documents":       validHost + "---\nstate_dir: /x\n",
		"organisation repo":   strings.Replace(validHost, "repo: iantanwx/alpha", "repo: https://github.com/iantanwx/alpha", 1),
		"reserved project id": strings.Replace(validHost, "id: alpha", "id: _host", 1),
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseHost([]byte(doc)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSaveHostAtomicAndPrivate(t *testing.T) {
	cfg, err := ParseHost([]byte(validHost))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, HostConfigFile)
	if err := SaveHost(path, cfg); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	back, err := LoadHost(path)
	if err != nil {
		t.Fatalf("LoadHost: %v", err)
	}
	if back.Store.Identity != "devbox-1" || len(back.Projects) != 1 || back.RunBudget != 2*time.Hour {
		t.Fatalf("round trip lost data: %+v", back)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory holds %d entries, want only host.yaml", len(entries))
	}
}

func TestSaveHostRefusesSymlink(t *testing.T) {
	cfg, _ := ParseHost([]byte(validHost))
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, HostConfigFile)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := SaveHost(link, cfg); err == nil {
		t.Fatal("SaveHost wrote through a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("symlink target changed")
	}
}
