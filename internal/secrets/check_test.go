package secrets

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const ae8Manifest = `
secrets:
  - name: DATABASE_URL
    source: op://Loop/Postgres/url
  - name: STRIPE_KEY
    source: op://Loop/Stripe/key
    production: true
`

func TestCheckReportsPresenceWithoutValue(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	e.set(t, "alpha", "DATABASE_URL", "postgres://check_value_1")
	ok, err := e.secrets.Check(ctx, "alpha", "DATABASE_URL")
	if err != nil || !ok {
		t.Fatalf("Check present = %v, %v", ok, err)
	}
	ok, err = e.secrets.Check(ctx, "alpha", "STRIPE_KEY")
	if err != nil || ok {
		t.Fatalf("Check missing = %v, %v", ok, err)
	}
	// The same name in another project is a different secret.
	if ok, _ := e.secrets.Check(ctx, "beta", "DATABASE_URL"); ok {
		t.Fatal("Check crossed projects")
	}
}

// AE8: a manifest secret with no stored value is reported by name. The
// refusal and its inbox item belong to U8.
func TestMissingReportsUnsetManifestSecret(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	m := manifest(t, ae8Manifest)
	e.set(t, "alpha", "DATABASE_URL", "postgres://missing_value_1")
	missing, err := e.secrets.Missing(ctx, "alpha", m)
	if err != nil || len(missing) != 1 || missing[0] != "STRIPE_KEY" {
		t.Fatalf("Missing = %v, %v; want [STRIPE_KEY]", missing, err)
	}
	_, err = e.secrets.Values(ctx, "alpha", m)
	var me *MissingSecretsError
	if !errors.As(err, &me) || !errors.Is(err, ErrMissingSecret) || len(me.Names) != 1 || me.Names[0] != "STRIPE_KEY" {
		t.Fatalf("Values = %v, want MissingSecretsError naming STRIPE_KEY", err)
	}
	e.set(t, "alpha", "STRIPE_KEY", "sk_live_values_value_2")
	vals, err := e.secrets.Values(ctx, "alpha", m)
	if err != nil || len(vals) != 2 || vals["STRIPE_KEY"] != "sk_live_values_value_2" {
		t.Fatalf("Values = %d entries, %v", len(vals), err)
	}
}

func TestEnvFileWrittenPrivateAndRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	values := map[string]string{
		"PLAIN":     "sk_live_plain_value",
		"EQUALS":    "a=b==c",
		"NEWLINES":  "-----BEGIN KEY-----\nMIIabc\n-----END KEY-----\n",
		"QUOTES":    `it's "quoted" \ back`,
		"DOLLAR":    "pa$$word${HOME}`id`",
		"CRLF_TAB":  "a\r\nb\tc",
		"SPACES":    "  padded  ",
		"HASH":      "abc #not-a-comment",
		"SINGLEQ_N": "x'y\nz",
	}
	if err := WriteEnvFile(path, values); err != nil {
		t.Fatalf("WriteEnvFile: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	raw, _ := os.ReadFile(path)
	got, err := ParseEnv(raw)
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	if len(got) != len(values) {
		t.Fatalf("ParseEnv returned %d entries, want %d", len(got), len(values))
	}
	for k, v := range values {
		if got[k] != v {
			t.Fatalf("%s did not round-trip through .env", k)
		}
	}
	// A rewrite replaces the file and keeps the mode.
	if err := WriteEnvFile(path, map[string]string{"PLAIN": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := RemoveEnvFile(path); err != nil {
		t.Fatalf("RemoveEnvFile: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal(".env still exists")
	}
	if err := RemoveEnvFile(path); err != nil {
		t.Fatalf("RemoveEnvFile of an absent file: %v", err)
	}
	if err := WriteEnvFile(path, map[string]string{"BAD-NAME": "v"}); err == nil {
		t.Fatal("an unsafe name was written")
	}
	if err := WriteEnvFile(path, map[string]string{"NUL": "a\x00b"}); err == nil || strings.Contains(err.Error(), "a\x00b") {
		t.Fatalf("a NUL value: %v", err)
	}
}

func TestEnvFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, ".env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := WriteEnvFile(link, map[string]string{"A": "secret_value_x"}); err == nil {
		t.Fatal("WriteEnvFile followed a symlink")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("the symlink target changed")
	}
	// Removal deletes the link itself and never its target.
	if err := RemoveEnvFile(link); err != nil {
		t.Fatalf("RemoveEnvFile on a link: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("RemoveEnvFile touched the symlink target")
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatal("the link remains")
	}
}
