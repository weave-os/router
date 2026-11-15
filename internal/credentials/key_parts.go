package credentials

import "weave-os/router/internal/auth"

// KeyParts returns the prefix and suffix of a credential that telemetry may
// record. A key too short to split is returned whole as the prefix.
func KeyParts(apiKey []byte) (prefix, suffix string) {
	key := string(apiKey)
	if len(key) <= auth.APITokenPrefixLen+auth.APITokenSuffixLen {
		return key, ""
	}
	return key[:auth.APITokenPrefixLen], key[len(key)-auth.APITokenSuffixLen:]
}
