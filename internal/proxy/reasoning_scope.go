package proxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

// reasoningReplayScope fingerprints the upstream an attempt dispatches to:
// provider, wire model, endpoint, and the account behind the credential. An
// upstream decrypts a reasoning item only under the account and model that
// produced it — Snowflake Cortex answers "encrypted reasoning was created for
// a different account or model", xAI "Encrypted content could not be
// decrypted or parsed" — and a rejected turn ends the client session rather
// than degrading it. Carrying the fingerprint inside the signature the router
// mints lets the next turn replay the reasoning only where it still decrypts.
//
// The account is identified by its stable principal, never by the bearer:
// Cortex decrypted a Grok item under a freshly minted workload-identity token
// (verified against prod's Snowflake account, 2026-09-21), so keying on the
// per-request bearer would drop reasoning the upstream would have accepted.
// With no request-scoped credential the answer rests on the adapter's own
// deployment key, so its principal stands in.
func (s *Service) reasoningReplayScope(ctx context.Context, d router.Decision) string {
	h := sha256.New()
	write := func(part string) {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	endpoint := normalizeUpstreamEndpoint(requestcontext.EffectiveBaseURL(ctx, ""))
	write(d.Provider)
	write(requestcontext.EffectiveUpstreamModel(ctx, d.Model))
	write(endpoint)

	// A principal already derived from key material is appended rather than
	// digested: the scope the client round-trips must not put key material
	// through a fast hash, whatever the truncation.
	digest := func(derived string) string {
		return hex.EncodeToString(h.Sum(nil)[:8]) + derived
	}

	creds := requestcontext.CredentialsFromContext(ctx)
	if creds == nil {
		write("deployment")
		return digest(s.deploymentPrincipal(d.Provider))
	}
	write(creds.Source)
	principal, secret := creds.UpstreamPrincipal()
	if secret {
		return digest(providers.CredentialPrincipal(endpoint, principal))
	}
	write(principal)
	return digest("")
}

// deploymentPrincipal reports the provider adapter's own account fingerprint,
// or "" for an adapter that does not publish one.
func (s *Service) deploymentPrincipal(provider string) string {
	if s == nil {
		return ""
	}
	client, err := s.clients.Client(provider)
	if err != nil {
		return ""
	}
	identified, ok := client.(providers.DeploymentPrincipal)
	if !ok {
		return ""
	}
	return identified.DeploymentPrincipal()
}

// normalizeUpstreamEndpoint reduces an endpoint to what distinguishes one
// upstream account from another, so a cosmetic difference (case, trailing
// slash, default port) does not spuriously invalidate a replay.
func normalizeUpstreamEndpoint(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !defaultPortForScheme(u.Scheme, port) {
		host += ":" + port
	}
	return strings.ToLower(u.Scheme) + "://" + host + strings.TrimRight(u.Path, "/")
}

func defaultPortForScheme(scheme, port string) bool {
	switch strings.ToLower(scheme) {
	case "https":
		return port == "443"
	case "http":
		return port == "80"
	}
	return false
}
