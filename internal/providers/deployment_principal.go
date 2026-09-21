package providers

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"

	lru "github.com/hashicorp/golang-lru/v2"
)

// DeploymentPrincipal identifies the upstream account an adapter authenticates
// as with its own boot-time key, as a non-secret fingerprint. Implemented only
// where a request may carry no credential of its own: the answer then depends
// on the adapter's deployment key, which no request-scoped value describes.
// An empty string means the adapter has no deployment key.
type DeploymentPrincipal interface {
	DeploymentPrincipal() string
}

const (
	principalKDFIterations = 100_000
	principalKDFSalt       = "weave-router/upstream-principal/v1\x00"
)

var principalCache, _ = lru.New[string, string](1024)

// CredentialPrincipal names the upstream account behind a credential that has
// no identity other than its own key material, as a short opaque token. The
// token travels to the client inside the minted reasoning envelope, so the
// derivation is a password-grade KDF rather than a digest: a bare hash of an
// API key is an offline oracle for the key. Memoized — the cost is per key,
// not per request — and endpoint-salted, so the same key on two endpoints is
// two accounts.
func CredentialPrincipal(endpoint, key string) string {
	if key == "" {
		return ""
	}
	cacheKey := endpoint + "\x00" + key
	if principal, ok := principalCache.Get(cacheKey); ok {
		return principal
	}
	derived, err := pbkdf2.Key(sha256.New, key, []byte(principalKDFSalt+endpoint), principalKDFIterations, 16)
	if err != nil {
		return ""
	}
	principal := hex.EncodeToString(derived[:8])
	principalCache.Add(cacheKey, principal)
	return principal
}
