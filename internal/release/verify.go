package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Signatures use minisign's legacy "Ed" format: Ed25519 over the whole
// file, and a global Ed25519 signature over that signature and the
// trusted comment (KTD26). `minisign -V` verifies them with the public
// key file this package writes. The standard library holds no BLAKE2b,
// so this package neither writes nor reads the prehashed "ED" format.

const (
	sigAlg         = "Ed"
	trustedPrefix  = "trusted comment: "
	untrustedPrfx  = "untrusted comment: "
	secretKeyLabel = "loopsk1:"
	// trustedTag starts every trusted comment a loop release signs.
	trustedTag = "loop release"
)

// ErrSignature is a signature that does not verify against any trusted key.
var ErrSignature = errors.New("release: the signature does not verify")

// PublicKey is one minisign Ed25519 public key.
type PublicKey struct {
	ID  [8]byte
	Key ed25519.PublicKey
}

// SecretKey is the release signing key. It stays offline, off the devbox
// and out of CI (KTD26).
type SecretKey struct {
	ID  [8]byte
	Key ed25519.PrivateKey
}

// KeyID is the key id as minisign prints it: the little-endian id in
// upper-case hex.
func (k PublicKey) KeyID() string { return keyIDString(k.ID) }

func keyIDString(id [8]byte) string {
	return fmt.Sprintf("%016X", binary.LittleEndian.Uint64(id[:]))
}

// String is the two-line minisign public key file.
func (k PublicKey) String() string {
	raw := append(append([]byte(sigAlg), k.ID[:]...), k.Key...)
	return untrustedPrfx + "minisign public key " + k.KeyID() + "\n" + base64.StdEncoding.EncodeToString(raw) + "\n"
}

// String is the secret key file. It holds the Ed25519 seed unencrypted, so
// the operator keeps the file offline at mode 0600.
func (k SecretKey) String() string {
	raw := append(append([]byte{}, k.ID[:]...), k.Key.Seed()...)
	return untrustedPrfx + "loop release secret key " + keyIDString(k.ID) + "; keep this file offline\n" +
		secretKeyLabel + base64.StdEncoding.EncodeToString(raw) + "\n"
}

// Public returns the public half of the key.
func (k SecretKey) Public() PublicKey {
	return PublicKey{ID: k.ID, Key: k.Key.Public().(ed25519.PublicKey)}
}

// GenerateKey makes a new signing key with a random key id.
func GenerateKey() (PublicKey, SecretKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return PublicKey{}, SecretKey{}, err
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		return PublicKey{}, SecretKey{}, err
	}
	return PublicKey{ID: id, Key: pub}, SecretKey{ID: id, Key: priv}, nil
}

// ParsePublicKey reads a minisign public key: the two-line file, or its
// base64 line alone.
func ParsePublicKey(text string) (PublicKey, error) {
	line := ""
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, untrustedPrfx) {
			line = l
			break
		}
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil || len(raw) != 2+8+ed25519.PublicKeySize || string(raw[:2]) != sigAlg {
		return PublicKey{}, errors.New("release: not a minisign Ed25519 public key")
	}
	var k PublicKey
	copy(k.ID[:], raw[2:10])
	k.Key = ed25519.PublicKey(bytes.Clone(raw[10:]))
	return k, nil
}

// ParsePublicKeys reads every key in text: one minisign key per base64
// line. Blank lines and comment lines are skipped.
func ParsePublicKeys(text string) ([]PublicKey, error) {
	var keys []PublicKey
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, untrustedPrfx) {
			continue
		}
		k, err := ParsePublicKey(l)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// ParseSecretKey reads the secret key file String writes.
func ParseSecretKey(text string) (SecretKey, error) {
	for _, l := range strings.Split(text, "\n") {
		b64, ok := strings.CutPrefix(strings.TrimSpace(l), secretKeyLabel)
		if !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil || len(raw) != 8+ed25519.SeedSize {
			break
		}
		var k SecretKey
		copy(k.ID[:], raw[:8])
		k.Key = ed25519.NewKeyFromSeed(raw[8:])
		return k, nil
	}
	return SecretKey{}, errors.New("release: not a loop release secret key file")
}

// TrustedComment is the trusted comment of a release's checksum file. The
// signature binds it, so a signed older release cannot be served as a
// newer version.
func TrustedComment(version string) string {
	return trustedTag + " version=" + version
}

// TrustedVersion reads the version from a trusted comment.
func TrustedVersion(trusted string) (string, error) {
	if rest, ok := strings.CutPrefix(trusted, trustedTag+" "); ok {
		for _, f := range strings.Fields(rest) {
			if v, ok := strings.CutPrefix(f, "version="); ok && v != "" {
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("release: the trusted comment %q names no loop release version", trusted)
}

// Sign signs msg and returns the minisign signature file.
func Sign(k SecretKey, msg []byte, trusted string) ([]byte, error) {
	if strings.ContainsAny(trusted, "\r\n") {
		return nil, errors.New("release: a trusted comment is one line")
	}
	sig := ed25519.Sign(k.Key, msg)
	global := ed25519.Sign(k.Key, append(bytes.Clone(sig), trusted...))
	raw := append(append([]byte(sigAlg), k.ID[:]...), sig...)
	var b strings.Builder
	b.WriteString(untrustedPrfx + "signature from loop release key " + keyIDString(k.ID) + "\n")
	b.WriteString(base64.StdEncoding.EncodeToString(raw) + "\n")
	b.WriteString(trustedPrefix + trusted + "\n")
	b.WriteString(base64.StdEncoding.EncodeToString(global) + "\n")
	return []byte(b.String()), nil
}

// Verify checks a minisign signature file over msg against the trusted
// keys, and returns the trusted comment. It refuses an unknown key id, a
// prehashed signature, and any change to msg or to the trusted comment.
func Verify(keys []PublicKey, msg, sigFile []byte) (string, error) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(sigFile)), "\r\n", "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], untrustedPrfx) || !strings.HasPrefix(lines[2], trustedPrefix) {
		return "", fmt.Errorf("%w: the signature file is not in minisign format", ErrSignature)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil || len(raw) != 2+8+ed25519.SignatureSize {
		return "", fmt.Errorf("%w: the signature line is malformed", ErrSignature)
	}
	if string(raw[:2]) != sigAlg {
		return "", fmt.Errorf("%w: algorithm %q is not the legacy Ed25519 format", ErrSignature, raw[:2])
	}
	var id [8]byte
	copy(id[:], raw[2:10])
	sig := raw[10:]
	trusted := strings.TrimPrefix(lines[2], trustedPrefix)
	global, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil || len(global) != ed25519.SignatureSize {
		return "", fmt.Errorf("%w: the global signature line is malformed", ErrSignature)
	}
	for _, k := range keys {
		if k.ID != id {
			continue
		}
		if !ed25519.Verify(k.Key, msg, sig) {
			return "", fmt.Errorf("%w: the file signature of key %s fails", ErrSignature, k.KeyID())
		}
		if !ed25519.Verify(k.Key, append(bytes.Clone(sig), trusted...), global) {
			return "", fmt.Errorf("%w: the trusted comment signature of key %s fails", ErrSignature, k.KeyID())
		}
		return trusted, nil
	}
	return "", fmt.Errorf("%w: key %s is not a trusted release key", ErrSignature, keyIDString(id))
}
