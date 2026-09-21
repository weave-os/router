package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// DeploymentPrincipal identifies the upstream account an adapter authenticates
// as with its own boot-time key, as a non-secret fingerprint. Implemented only
// where a request may carry no credential of its own: the answer then depends
// on the adapter's deployment key, which no request-scoped value describes.
// An empty string means the adapter has no deployment key.
type DeploymentPrincipal interface {
	DeploymentPrincipal() string
}

// PrincipalFingerprint hashes the parts identifying one upstream account into
// a short opaque token. Safe to embed in values the client round-trips: the
// inputs are one-way and truncated.
func PrincipalFingerprint(parts ...string) string {
	if strings.Join(parts, "") == "" {
		return ""
	}
	h := sha256.New()
	for _, part := range parts {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}
