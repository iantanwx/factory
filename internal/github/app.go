package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// App is the loop's GitHub App identity. The private key comes from the
// encrypted store (KTD15) and lives only in memory.
type App struct {
	ID  int64
	key *rsa.PrivateKey
}

// ParseApp reads the App's PEM private key, in PKCS #1 or PKCS #8 form.
// The error never quotes the key.
func ParseApp(id int64, pemKey []byte) (*App, error) {
	if id <= 0 {
		return nil, errors.New("github: the App id must be a positive number")
	}
	block, _ := pem.Decode(pemKey)
	if block == nil {
		return nil, errors.New("github: the App private key is not PEM")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("github: the App private key does not parse as PKCS #1")
		}
		key = k
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("github: the App private key does not parse as PKCS #8")
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("github: the App private key is not an RSA key")
		}
		key = rk
	default:
		return nil, fmt.Errorf("github: the App private key has PEM type %q; want an RSA private key", block.Type)
	}
	return &App{ID: id, key: key}, nil
}

// JWT signs a short-lived token that proves the server holds the App's
// key. iat is backdated a minute, because GitHub refuses a token issued in
// the future, and the lifetime stays inside GitHub's ten-minute maximum.
// Ported from appJwt in scripts/sdlc/gh-app.ts:102 (alpha).
func (a *App) JWT(now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(540 * time.Second).Unix(),
		"iss": strconv.FormatInt(a.ID, 10),
	})
	if err != nil {
		return "", err
	}
	body := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign the App token: %w", err)
	}
	return body + "." + enc.EncodeToString(sig), nil
}
