package config

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

const validManifest = `
secrets:
  - name: STRIPE_KEY
    source: op://Loop/Stripe/secret key
    production: true
  - name: DATABASE_URL
    source: op://Loop/Postgres/dev/url
`

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if len(m.Secrets) != 2 || !m.Secrets[0].Production || m.Secrets[1].Production {
		t.Fatalf("manifest = %+v", m)
	}
	if v := m.Secrets[0].Vault(); v != "Loop" {
		t.Fatalf("Vault = %q", v)
	}
	if names := m.Names(); len(names) != 2 || names[0] != "STRIPE_KEY" {
		t.Fatalf("Names = %v", names)
	}
}

// An entry with no source is set by hand with loop secret set.
func TestParseManifestTakesAnEntryWithNoSource(t *testing.T) {
	m, err := ParseManifest([]byte(validManifest + "  - name: HAND_SET\n"))
	if err != nil || m.Secrets[len(m.Secrets)-1].Name != "HAND_SET" {
		t.Fatalf("parse = %v", err)
	}
}

// An entry names the directory whose .env holds it and the stages whose
// runs receive it (parity 9.3, 11.2). An entry with neither goes into the
// root .env of every run.
func TestManifestEntryNamesDirAndStages(t *testing.T) {
	doc := validManifest + "  - name: WEB_KEY\n    dir: apps/web\n    stages: [implementer, review-room]\n"
	m, err := ParseManifest([]byte(doc))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if e := m.Secrets[2]; e.Dir != "apps/web" || !slices.Equal(e.Stages, []string{"implementer", "review-room"}) {
		t.Fatalf("entry = %+v", e)
	}
	if e := m.Secrets[0]; e.Dir != "" || e.Stages != nil {
		t.Fatalf("an entry with no dir or stages = %+v", e)
	}
}

// ForStage keeps each entry with no stages and each entry that names the
// stage, in manifest order.
func TestManifestForStageKeepsOnlyThatStagesNames(t *testing.T) {
	m, err := ParseManifest([]byte("secrets:\n  - name: ALL_KEY\n  - name: IMPL_KEY\n    stages: [implementer]\n" +
		"  - name: REVIEW_KEY\n    stages: [code-reviewer, review-room]\n"))
	if err != nil {
		t.Fatal(err)
	}
	for stage, want := range map[string][]string{
		"implementer":   {"ALL_KEY", "IMPL_KEY"},
		"code-reviewer": {"ALL_KEY", "REVIEW_KEY"},
		"review-room":   {"ALL_KEY", "REVIEW_KEY"},
		"intake-room":   {"ALL_KEY"},
	} {
		if got := m.ForStage(stage).Names(); !slices.Equal(got, want) {
			t.Fatalf("ForStage(%s) = %v, want %v", stage, got, want)
		}
	}
}

func TestParseManifestRefusesValueField(t *testing.T) {
	doc := validManifest + "  - name: API_TOKEN\n    value: tok_live_abcdef123456\n"
	_, err := ParseManifest([]byte(doc))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "value") {
		t.Fatalf("error does not name the value field: %v", err)
	}
	if strings.Contains(err.Error(), "tok_live") {
		t.Fatal("error echoes the value")
	}
}

func TestParseManifestRefuses(t *testing.T) {
	cases := map[string]string{
		"unknown field":   validManifest + "version: 2\n",
		"unknown entry":   validManifest + "  - name: X_TOKEN\n    source: op://Loop/X/t\n    scope: prod\n",
		"unsafe name":     strings.Replace(validManifest, "STRIPE_KEY", "STRIPE-KEY", 1),
		"digit name":      strings.Replace(validManifest, "STRIPE_KEY", "1KEY", 1),
		"duplicate name":  strings.Replace(validManifest, "DATABASE_URL", "STRIPE_KEY", 1),
		"not op source":   strings.Replace(validManifest, "op://Loop/Stripe/secret key", "vault://Loop/Stripe/key", 1),
		"short op source": strings.Replace(validManifest, "op://Loop/Stripe/secret key", "op://Loop/Stripe", 1),
		"empty segment":   strings.Replace(validManifest, "op://Loop/Stripe/secret key", "op://Loop//key", 1),
		"query":           strings.Replace(validManifest, "op://Loop/Stripe/secret key", "op://Loop/Stripe/key?attribute=otp", 1),
		"absolute dir":    validManifest + "  - name: WEB_KEY\n    dir: /etc\n",
		"parent dir":      validManifest + "  - name: WEB_KEY\n    dir: ../other\n",
		"inner parent":    validManifest + "  - name: WEB_KEY\n    dir: apps/../../other\n",
		"unclean dir":     validManifest + "  - name: WEB_KEY\n    dir: apps/web/\n",
		"dot dir":         validManifest + "  - name: WEB_KEY\n    dir: .\n",
		"backslash dir":   validManifest + "  - name: WEB_KEY\n    dir: apps\\web\n",
		"git dir":         validManifest + "  - name: WEB_KEY\n    dir: .git/hooks\n",
		"empty stages":    validManifest + "  - name: WEB_KEY\n    stages: []\n",
		"twice a stage":   validManifest + "  - name: WEB_KEY\n    stages: [implementer, implementer]\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(doc)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
