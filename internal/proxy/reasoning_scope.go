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
