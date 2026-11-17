package credentials

import "weave-os/router/internal/auth"

func KeyParts(apiKey []byte) (prefix, suffix string) {
	key := string(apiKey)
	if len(key) <= auth.APITokenPrefixLen+auth.APITokenSuffixLen {
		return key, ""
	}
	return key[:auth.APITokenPrefixLen], key[len(key)-auth.APITokenSuffixLen:]
}
