package providers

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"

	lru "github.com/hashicorp/golang-lru/v2"
)

type DeploymentPrincipal interface {
	DeploymentPrincipal() string
}

const (
	principalKDFIterations = 100_000
	principalKDFSalt       = "weave-router/upstream-principal/v1\x00"
)

var principalCache, _ = lru.New[string, string](1024)

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
