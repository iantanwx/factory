package release

import _ "embed" // trusted_keys.pub

//go:embed trusted_keys.pub
var trustedKeysFile string

// TrustedKeys are the release keys this binary embeds.
func TrustedKeys() ([]PublicKey, error) { return ParsePublicKeys(trustedKeysFile) }
