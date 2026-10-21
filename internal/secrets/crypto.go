package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrDecrypt means a stored row does not open. Its ciphertext was changed,
// moved from another row, or wrapped under another key version. The error
// names the row only.
var ErrDecrypt = errors.New("secrets: ciphertext does not open for this row")

const (
	dataKeyLen = 32 // AES-256 data key, fresh for each stored value
	nonceLen   = 12 // the standard GCM nonce
)

// sealed is one secrets row's encrypted columns.
type sealed struct {
	KeyID      string
	KeyVersion int64
	WrappedKey []byte // wrap nonce followed by the GCM-sealed data key
	Nonce      []byte
	Ciphertext []byte
}

// additionalData binds a ciphertext to its row: the layer, the project, the
// secret name, the key id and the key version (KTD17). Each field is length
// prefixed, so no two tuples share one encoding.
func additionalData(layer, project, name, keyID string, version int64) []byte {
	var b []byte
	for _, f := range []string{"loop-secret/v1", layer, project, name, keyID} {
		b = binary.BigEndian.AppendUint32(b, uint32(len(f)))
		b = append(b, f...)
	}
	return binary.BigEndian.AppendUint64(b, uint64(version))
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// seal encrypts plaintext under a fresh data key and wraps the data key
// under k.
func seal(k Key, project, name string, plaintext []byte) (sealed, error) {
	dek, err := randomBytes(dataKeyLen)
	if err != nil {
		return sealed{}, err
	}
	defer clear(dek)
	kek, err := newGCM(k.Material)
	if err != nil {
		return sealed{}, fmt.Errorf("secrets: key %s: %w", k.ID, err)
	}
	wrapNonce, err := randomBytes(nonceLen)
	if err != nil {
		return sealed{}, err
	}
	wrapped := kek.Seal(wrapNonce, wrapNonce, dek, additionalData("wrap", project, name, k.ID, k.Version))
	data, err := newGCM(dek)
	if err != nil {
		return sealed{}, err
	}
	nonce, err := randomBytes(nonceLen)
	if err != nil {
		return sealed{}, err
	}
	ct := data.Seal(nil, nonce, plaintext, additionalData("data", project, name, k.ID, k.Version))
	return sealed{KeyID: k.ID, KeyVersion: k.Version, WrappedKey: wrapped, Nonce: nonce, Ciphertext: ct}, nil
}

// open decrypts one row. It refuses a key_id the provider does not hold and
// a row whose key version differs from that key's version.
func open(kp KeyProvider, project, name string, s sealed) ([]byte, error) {
	k, err := kp.Lookup(s.KeyID)
	if err != nil {
		return nil, err
	}
	if k.Version != s.KeyVersion {
		return nil, fmt.Errorf("%w: %s/%s names key %s version %d, the host holds version %d",
			ErrDecrypt, project, name, s.KeyID, s.KeyVersion, k.Version)
	}
	if len(s.WrappedKey) < nonceLen || len(s.Nonce) != nonceLen {
		return nil, fmt.Errorf("%w: %s/%s", ErrDecrypt, project, name)
	}
	kek, err := newGCM(k.Material)
	if err != nil {
		return nil, fmt.Errorf("secrets: key %s: %w", k.ID, err)
	}
	dek, err := kek.Open(nil, s.WrappedKey[:nonceLen], s.WrappedKey[nonceLen:], additionalData("wrap", project, name, s.KeyID, s.KeyVersion))
	if err != nil {
		return nil, fmt.Errorf("%w: %s/%s", ErrDecrypt, project, name)
	}
	defer clear(dek)
	data, err := newGCM(dek)
	if err != nil {
		return nil, fmt.Errorf("%w: %s/%s", ErrDecrypt, project, name)
	}
	pt, err := data.Open(nil, s.Nonce, s.Ciphertext, additionalData("data", project, name, s.KeyID, s.KeyVersion))
	if err != nil {
		return nil, fmt.Errorf("%w: %s/%s", ErrDecrypt, project, name)
	}
	return pt, nil
}
