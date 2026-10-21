// Package hexid makes the lower-case hex strings that name and fingerprint
// things: random ids and sha256 digests.
package hexid

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// Random is n random bytes as lower-case hex. crypto/rand.Read never
// returns an error, so Random ignores it.
func Random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// SHA256 is the sha256 digest of data as lower-case hex.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
