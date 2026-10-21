package secrets

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iantanwx/loop/internal/config"
)

// fakeOp writes an op script that logs its argv and environment size, then
// prints the value for a known reference. It never runs the real op.
func fakeOp(t *testing.T, values map[string]string) (opPath, argLog string) {
	t.Helper()
	dir := t.TempDir()
	argLog = filepath.Join(dir, "argv.log")
	var cases strings.Builder
	for ref, v := range values {
		cases.WriteString("  '" + ref + "') printf '%s' '" + v + "' ;;\n")
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> '" + argLog + "'\n" +
		"[ \"$1\" = read ] && [ \"$2\" = --no-newline ] || { echo 'bad usage' >&2; exit 2; }\n" +
		"case \"$3\" in\n" + cases.String() +
		"  op://Loop/Leaky/*) printf 'partial_leaked_value_99' ; echo 'echoed partial_leaked_value_99' >&2 ; exit 1 ;;\n" +
		"  *) echo \"[ERROR] could not read secret '$3': item not found\" >&2 ; exit 1 ;;\n" +
		"esac\n"
	opPath = filepath.Join(dir, "op")
	if err := os.WriteFile(opPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return opPath, argLog
}

func manifest(t *testing.T, doc string) *config.Manifest {
	t.Helper()
	m, err := config.ParseManifest([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestImportStoresValuesAndLeaksNone(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	values := map[string]string{
		"op://Loop/Stripe/key":       "sk_live_imported_value_1",
		"op://Loop/Postgres/dev/url": "postgres://u:p4ss_imported@db/x",
		"op://Shared/Sentry/dsn":     "https://sentry_imported_value_3@o1.ingest",
	}
	opPath, argLog := fakeOp(t, values)
	var logs bytes.Buffer
	im := &Importer{Store: e.secrets, Vaults: []string{"Loop", "Shared"}, OpPath: opPath,
		Logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}

	m1 := manifest(t, `
secrets:
  - name: STRIPE_KEY
    source: op://Loop/Stripe/key
  - name: DATABASE_URL
    source: op://Loop/Postgres/dev/url
`)
	res, err := im.Import(ctx, "alpha", m1)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if strings.Join(res.Added, ",") != "op://Loop/Postgres/dev/url,op://Loop/Stripe/key" {
		t.Fatalf("first import Added = %v, want every reference", res.Added)
	}
	if e.get(t, "alpha", "STRIPE_KEY") != values["op://Loop/Stripe/key"] ||
		e.get(t, "alpha", "DATABASE_URL") != values["op://Loop/Postgres/dev/url"] {
		t.Fatal("import stored the wrong values")
	}

	// A second import lists only the reference added since the first.
	m2 := manifest(t, `
secrets:
  - name: STRIPE_KEY
    source: op://Loop/Stripe/key
  - name: DATABASE_URL
    source: op://Loop/Postgres/dev/url
  - name: SENTRY_DSN
    source: op://Shared/Sentry/dsn
`)
	res, err = im.Import(ctx, "alpha", m2)
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}
	if len(res.Added) != 1 || res.Added[0] != "op://Shared/Sentry/dsn" {
		t.Fatalf("second import Added = %v", res.Added)
	}
	// Another project's import history is its own.
	res, err = im.Import(ctx, "beta", m1)
	if err != nil || len(res.Added) != 2 {
		t.Fatalf("beta Added = %v, %v", res.Added, err)
	}

	argv, _ := os.ReadFile(argLog)
	if strings.Count(string(argv), "\n") != 7 {
		t.Fatalf("op ran %d times, want once per entry (7):\n%s", strings.Count(string(argv), "\n"), argv)
	}
	for _, v := range values {
		if strings.Contains(string(argv), v) {
			t.Fatal("a value reached op's argv")
		}
		if strings.Contains(logs.String(), v) {
			t.Fatal("a value reached the log")
		}
	}
	if !strings.Contains(logs.String(), "op://Loop/Stripe/key") {
		t.Fatalf("the log does not name the references it read:\n%s", logs.String())
	}
}

func TestImportRefusesUndeclaredVault(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	opPath, argLog := fakeOp(t, map[string]string{"op://Personal/Bank/pin": "should_never_be_read"})
	im := &Importer{Store: e.secrets, Vaults: []string{"Loop"}, OpPath: opPath}
	m := manifest(t, `
secrets:
  - name: STRIPE_KEY
    source: op://Loop/Stripe/key
  - name: BANK_PIN
    source: op://Personal/Bank/pin
`)
	_, err := im.Import(ctx, "alpha", m)
	if !errors.Is(err, ErrVaultNotDeclared) || !strings.Contains(err.Error(), "op://Personal/Bank/pin") {
		t.Fatalf("err = %v, want ErrVaultNotDeclared naming the reference", err)
	}
	if b, _ := os.ReadFile(argLog); len(b) != 0 {
		t.Fatal("op ran before the vault check refused the manifest")
	}
}

func TestImportOpFailureNamesReferenceNotValue(t *testing.T) {
	ctx := context.Background()
	e := newTestEnv(t)
	opPath, _ := fakeOp(t, map[string]string{"op://Loop/Stripe/key": "sk_live_ok_value_1"})
	im := &Importer{Store: e.secrets, Vaults: []string{"Loop"}, OpPath: opPath}
	for _, ref := range []string{"op://Loop/Missing/key", "op://Loop/Leaky/key"} {
		m := manifest(t, "secrets:\n  - name: STRIPE_KEY\n    source: op://Loop/Stripe/key\n  - name: OTHER\n    source: "+ref+"\n")
		_, err := im.Import(ctx, "alpha", m)
		if err == nil || !strings.Contains(err.Error(), ref) {
			t.Fatalf("err = %v, want an error naming %s", err, ref)
		}
		if strings.Contains(err.Error(), "partial_leaked_value_99") || strings.Contains(err.Error(), "sk_live_ok_value_1") {
			t.Fatalf("the error holds a value: %v", err)
		}
		// A failed import stores nothing, so STRIPE_KEY stays unset.
		if ok, _ := e.secrets.Check(ctx, "alpha", "STRIPE_KEY"); ok {
			t.Fatal("a failed import stored a value")
		}
	}
}
