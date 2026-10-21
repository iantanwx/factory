package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

func TestEncryptionRoundTrips(t *testing.T) {
	e := newTestEnv(t)
	values := map[string]string{
		"STRIPE_KEY":  "sk_live_51HxyzABCDEFGHIJ",
		"PEM":         "-----BEGIN KEY-----\nMIIabc\n-----END KEY-----\n",
		"WITH_EQUALS": "a=b=c; d",
	}
	for name, v := range values {
		e.set(t, "alpha", name, v+"\n") // Set trims one trailing newline from stdin
	}
	for name, v := range values {
		if got := e.get(t, "alpha", name); got != v {
			t.Fatalf("%s did not round-trip", name)
		}
	}
	names, err := e.secrets.Names(context.Background(), "alpha")
	if err != nil || len(names) != 3 {
		t.Fatalf("Names = %v, %v", names, err)
	}
}

func TestCiphertextBoundToItsRow(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	e.set(t, "alpha", "STRIPE_KEY", "sk_live_alpha_value_1")
	e.set(t, "alpha", "OTHER_KEY", "other_alpha_value_2")
	e.set(t, "beta", "STRIPE_KEY", "sk_live_beta_value_3")

	// Move alpha's STRIPE_KEY ciphertext into two other rows.
	copyRow := `UPDATE secrets SET key_id = s.key_id, key_version = s.key_version, wrapped_key = s.wrapped_key,
		nonce = s.nonce, ciphertext = s.ciphertext
		FROM (SELECT * FROM secrets WHERE project = 'alpha' AND name = 'STRIPE_KEY') AS s
		WHERE secrets.project = ? AND secrets.name = ?`
	e.exec(t, copyRow, "beta", "STRIPE_KEY")
	e.exec(t, copyRow, "alpha", "OTHER_KEY")
	for _, row := range [][2]string{{"beta", "STRIPE_KEY"}, {"alpha", "OTHER_KEY"}} {
		_, err := e.secrets.Get(ctx, row[0], row[1])
		if !errors.Is(err, ErrDecrypt) {
			t.Fatalf("moved ciphertext in %s/%s: %v, want ErrDecrypt", row[0], row[1], err)
		}
		if strings.Contains(err.Error(), "sk_live") {
			t.Fatal("error holds a value")
		}
	}
	if got := e.get(t, "alpha", "STRIPE_KEY"); got != "sk_live_alpha_value_1" {
		t.Fatal("source row changed")
	}

	// A changed key version is another row binding.
	e.exec(t, `UPDATE secrets SET key_version = key_version + 1 WHERE project = 'alpha' AND name = 'STRIPE_KEY'`)
	if _, err := e.secrets.Get(ctx, "alpha", "STRIPE_KEY"); err == nil {
		t.Fatal("a changed key version still decrypted")
	}
}

func TestUnknownKeyIDRefused(t *testing.T) {
	e := newTestEnv(t)
	e.set(t, "alpha", "STRIPE_KEY", "sk_live_alpha_value_1")
	e.exec(t, `UPDATE secrets SET key_id = 'kek-retired' WHERE project = 'alpha'`)
	if _, err := e.secrets.Get(context.Background(), "alpha", "STRIPE_KEY"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}

func TestSetRefuses(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	cases := map[string]struct{ project, name, value string }{
		"bad name":           {"alpha", "STRIPE-KEY", "value_long_enough"},
		"empty value":        {"alpha", "EMPTY", ""},
		"unregistered":       {"gamma", "STRIPE_KEY", "value_long_enough"},
		"oversize":           {"alpha", "BIG", strings.Repeat("x", MaxValueSize+1)},
		"newline only value": {"alpha", "NL", "\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := e.secrets.Set(ctx, c.project, c.name, strings.NewReader(c.value))
			if err == nil {
				t.Fatal("Set succeeded")
			}
			if c.value != "" && strings.Contains(err.Error(), c.value) {
				t.Fatal("error holds the value")
			}
		})
	}
}

// Two projects keep separate secrets, and a project removal purges only its
// own rows.
func TestProjectsKeepSeparateSecrets(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	e.set(t, "alpha", "STRIPE_KEY", "alpha_stripe_value")
	e.set(t, "beta", "STRIPE_KEY", "beta_stripe_value")
	if e.get(t, "alpha", "STRIPE_KEY") != "alpha_stripe_value" || e.get(t, "beta", "STRIPE_KEY") != "beta_stripe_value" {
		t.Fatal("projects share a secret")
	}
	if err := e.secrets.PurgeProject(ctx, config.Project{ID: "alpha"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e.secrets.Check(ctx, "alpha", "STRIPE_KEY"); ok {
		t.Fatal("purge left alpha's secret")
	}
	if ok, _ := e.secrets.Check(ctx, "beta", "STRIPE_KEY"); !ok {
		t.Fatal("purge of alpha removed beta's secret")
	}
}

// The App private key, the Mergify token and each tracker token come from
// the encrypted store. Host config on disk names them and holds no value.
func TestPlatformCredentialsComeFromStore(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	cfg := &config.HostConfig{
		StateDir: "/var/lib/loop",
		Credentials: config.Credentials{
			GitHubApp:          config.GitHubAppCredentials{AppID: 7, PrivateKeySecret: "GITHUB_APP_PRIVATE_KEY"},
			MergifyTokenSecret: "MERGIFY_TOKEN",
			Trackers:           map[string]config.TrackerCredentials{"linear": {TokenSecret: "LINEAR_TOKEN"}},
		},
	}
	values := map[string]string{
		"GITHUB_APP_PRIVATE_KEY": "-----BEGIN RSA PRIVATE KEY-----\nMIIEapp\n-----END RSA PRIVATE KEY-----",
		"MERGIFY_TOKEN":          "mergify_token_value_123",
		"LINEAR_TOKEN":           "lin_api_token_value_456",
	}
	if _, err := e.secrets.HostCredentials(ctx, cfg); !errors.Is(err, ErrMissingSecret) {
		t.Fatalf("HostCredentials before set: %v, want ErrMissingSecret", err)
	}
	for name, v := range values {
		e.set(t, config.HostScope, name, v)
	}
	creds, err := e.secrets.HostCredentials(ctx, cfg)
	if err != nil {
		t.Fatalf("HostCredentials: %v", err)
	}
	for _, ref := range cfg.CredentialRefs() {
		if string(creds[ref.Purpose]) != values[ref.Name] {
			t.Fatalf("credential %s did not come from the store", ref.Purpose)
		}
	}
	path := filepath.Join(t.TempDir(), config.HostConfigFile)
	if err := config.SaveHost(path, cfg); err != nil {
		t.Fatal(err)
	}
	onDisk, _ := os.ReadFile(path)
	for _, v := range values {
		if bytes.Contains(onDisk, []byte(v)) {
			t.Fatal("host config on disk holds a credential value")
		}
	}
	// The database file holds no plaintext either.
	raw, _ := os.ReadFile(e.db.Path())
	wal, _ := os.ReadFile(e.db.Path() + "-wal")
	for _, v := range values {
		if bytes.Contains(raw, []byte(v)) || bytes.Contains(wal, []byte(v)) {
			t.Fatal("the database holds a plaintext credential")
		}
	}
}
