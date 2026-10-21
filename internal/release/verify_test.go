package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, sec := mustKey(t)
	msg := []byte("abc  loop-linux-amd64\n")
	sig, err := Sign(sec, msg, TrustedComment("v1.2.3"))
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := Verify([]PublicKey{pub}, msg, sig)
	if err != nil {
		t.Fatal(err)
	}
	if v, err := TrustedVersion(trusted); err != nil || v != "v1.2.3" {
		t.Fatalf("trusted version = %q, %v", v, err)
	}
	// The key files parse back to the same key.
	pub2, err := ParsePublicKey(pub.String())
	if err != nil || !bytes.Equal(pub2.Key, pub.Key) || pub2.ID != pub.ID {
		t.Fatalf("public key round trip: %v", err)
	}
	sec2, err := ParseSecretKey(sec.String())
	if err != nil || !bytes.Equal(sec2.Key, sec.Key) {
		t.Fatalf("secret key round trip: %v", err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	pub, sec := mustKey(t)
	other, _ := mustKey(t)
	msg := []byte("checksums\n")
	sig, err := Sign(sec, msg, TrustedComment("v1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func() ([]PublicKey, []byte, []byte){
		"changed message": func() ([]PublicKey, []byte, []byte) { return []PublicKey{pub}, []byte("checksumz\n"), sig },
		"unknown key":     func() ([]PublicKey, []byte, []byte) { return []PublicKey{other}, msg, sig },
		"no key":          func() ([]PublicKey, []byte, []byte) { return nil, msg, sig },
		"changed trusted comment": func() ([]PublicKey, []byte, []byte) {
			return []PublicKey{pub}, msg, bytes.Replace(sig, []byte("version=v1.0.0"), []byte("version=v9.0.0"), 1)
		},
		"garbage": func() ([]PublicKey, []byte, []byte) { return []PublicKey{pub}, msg, []byte("not a signature") },
	}
	for name, c := range cases {
		keys, m, s := c()
		if _, err := Verify(keys, m, s); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

// TestSignatureIsMinisignLegacy checks the wire format minisign reads: an
// "Ed" algorithm, the key id, an Ed25519 signature over the file, and a
// global signature over the signature and the trusted comment.
func TestSignatureIsMinisignLegacy(t *testing.T) {
	pub, sec := mustKey(t)
	msg := []byte("hello\n")
	sig, err := Sign(sec, msg, "loop release version=v0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(sig)), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "untrusted comment: ") || !strings.HasPrefix(lines[2], "trusted comment: ") {
		t.Fatalf("layout:\n%s", sig)
	}
	raw, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(raw) != 74 || string(raw[:2]) != "Ed" || !bytes.Equal(raw[2:10], pub.ID[:]) {
		t.Fatalf("signature line: %v %d", err, len(raw))
	}
	if !ed25519.Verify(pub.Key, msg, raw[10:]) {
		t.Fatal("the file signature is not plain Ed25519 over the file")
	}
	global, _ := base64.StdEncoding.DecodeString(lines[3])
	if !ed25519.Verify(pub.Key, append(raw[10:], []byte("loop release version=v0.1.0")...), global) {
		t.Fatal("the global signature does not cover signature and trusted comment")
	}
	pubLines := strings.Split(strings.TrimSpace(pub.String()), "\n")
	praw, _ := base64.StdEncoding.DecodeString(pubLines[1])
	if len(praw) != 42 || string(praw[:2]) != "Ed" {
		t.Fatalf("public key line: %d", len(praw))
	}
}

func mustKey(t *testing.T) (PublicKey, SecretKey) {
	t.Helper()
	pub, sec, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return pub, sec
}
