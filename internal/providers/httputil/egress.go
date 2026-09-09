package httputil

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"weave-os/router/internal/providers"
)

// ErrRestrictedDestination is the dial error when an upstream resolves outside
// the public internet on a transport built for public destinations only.
var ErrRestrictedDestination = errors.New("upstream destination is not publicly routable")

const modelDiscoveryPrivateOriginsEnv = "ROUTER_MODEL_DISCOVERY_PRIVATE_ORIGINS"

type modelDiscoveryResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type modelDiscoveryPolicy struct {
	privateOrigins map[string]struct{}
	resolver       modelDiscoveryResolver
	dialContext    func(context.Context, string, string) (net.Conn, error)
}

type modelDiscoveryPrivateOriginKey struct{}

// ModelDiscoveryOption injects deterministic network dependencies for tests.
type ModelDiscoveryOption func(*modelDiscoveryPolicy)

// WithModelDiscoveryResolver replaces DNS resolution for a model-discovery client.
func WithModelDiscoveryResolver(resolver modelDiscoveryResolver) ModelDiscoveryOption {
	return func(policy *modelDiscoveryPolicy) { policy.resolver = resolver }
}

// WithModelDiscoveryDialer replaces direct IP dialing for a model-discovery client.
func WithModelDiscoveryDialer(dialContext func(context.Context, string, string) (net.Conn, error)) ModelDiscoveryOption {
	return func(policy *modelDiscoveryPolicy) { policy.dialContext = dialContext }
}

// NewDefaultModelDiscoveryClient returns a public-destination-only client.
func NewDefaultModelDiscoveryClient() *http.Client {
	client, err := NewModelDiscoveryClient("")
	if err != nil {
		panic(err)
	}
	return client
}

// NewModelDiscoveryClient returns a proxy-free client that validates every
// resolved address and dials one of those validated IPs directly.
func NewModelDiscoveryClient(privateOrigins string, opts ...ModelDiscoveryOption) (*http.Client, error) {
	allowedOrigins, err := parseModelDiscoveryPrivateOrigins(privateOrigins)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	policy := &modelDiscoveryPolicy{
		privateOrigins: allowedOrigins,
		resolver:       net.DefaultResolver,
		dialContext:    dialer.DialContext,
	}
	for _, opt := range opts {
		opt(policy)
	}
	transport := newTransport(10*time.Second, 10*time.Second, DefaultResponseHeaderTimeout, true)
	transport.DialContext = policy.dial
	transport.Proxy = nil
	return NewClient(&modelDiscoveryRoundTripper{transport: transport, privateOrigins: allowedOrigins}), nil
}

type modelDiscoveryRoundTripper struct {
	transport      http.RoundTripper
	privateOrigins map[string]struct{}
}

func (t *modelDiscoveryRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	origin, err := canonicalDiscoveryOrigin(request.URL)
	if err != nil {
		return nil, providers.ErrModelDiscoveryDestination
	}
	_, privateAllowed := t.privateOrigins[origin]
	ctx := context.WithValue(request.Context(), modelDiscoveryPrivateOriginKey{}, privateAllowed)
	return t.transport.RoundTrip(request.Clone(ctx))
}

func (p *modelDiscoveryPolicy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, providers.ErrModelDiscoveryDestination
	}
	addresses, err := p.resolve(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, providers.ErrModelDiscoveryTransport
	}
	privateAllowed, _ := ctx.Value(modelDiscoveryPrivateOriginKey{}).(bool)
	for _, address := range addresses {
		if !privateAllowed && !isPublicAddr(address) {
			return nil, providers.ErrModelDiscoveryDestination
		}
	}
	for _, address := range addresses {
		connection, dialErr := p.dialContext(ctx, network, net.JoinHostPort(address.Unmap().String(), port))
		if dialErr == nil {
			return connection, nil
		}
	}
	return nil, providers.ErrModelDiscoveryTransport
}

func (p *modelDiscoveryPolicy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if address, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{address}, nil
	}
	return p.resolver.LookupNetIP(ctx, "ip", host)
}

func isPublicAddr(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || address.IsLoopback() || address.IsPrivate() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	if address.Is4() {
		bytes := address.As4()
		// None of these have a stdlib predicate: 0.0.0.0/8 is "this host on
		// this network" (RFC 1122) and resolves to a local service on Linux,
		// 100.64.0.0/10 is carrier-grade NAT (RFC 6598), and 240.0.0.0/4 is
		// reserved (RFC 1112) — which also covers the 255.255.255.255
		// broadcast address.
		if bytes[0] == 0 || (bytes[0] == 100 && bytes[1] >= 64 && bytes[1] <= 127) || bytes[0] >= 240 {
			return false
		}
	}
	return true
}

func parseModelDiscoveryPrivateOrigins(raw string) (map[string]struct{}, error) {
	origins := make(map[string]struct{})
	for _, configuredOrigin := range strings.Split(raw, ",") {
		configuredOrigin = strings.TrimSpace(configuredOrigin)
		if configuredOrigin == "" {
			continue
		}
		parsed, err := url.Parse(configuredOrigin)
		if err != nil || strings.ContainsAny(configuredOrigin, "?#") || parsed.Path != "" || parsed.RawPath != "" {
			return nil, fmt.Errorf("invalid %s entry", modelDiscoveryPrivateOriginsEnv)
		}
		origin, err := canonicalDiscoveryOrigin(parsed)
		if err != nil {
			return nil, fmt.Errorf("invalid %s entry", modelDiscoveryPrivateOriginsEnv)
		}
		origins[origin] = struct{}{}
	}
	return origins, nil
}

func canonicalDiscoveryOrigin(parsed *url.URL) (string, error) {
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return "", providers.ErrModelDiscoveryDestination
	}
	host := strings.ToLower(parsed.Hostname())
	if strings.ContainsAny(host, "%*") {
		return "", providers.ErrModelDiscoveryDestination
	}
	port := parsed.Port()
	if strings.HasSuffix(parsed.Host, ":") {
		return "", providers.ErrModelDiscoveryDestination
	}
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		portNumber, conversionErr := strconv.Atoi(port)
		if conversionErr != nil || portNumber < 1 || portNumber > 65535 {
			return "", providers.ErrModelDiscoveryDestination
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

// restrictUpstreamEgressEnv forces the public-destination dial policy on or
// off regardless of deployment mode.
const restrictUpstreamEgressEnv = "ROUTER_RESTRICT_UPSTREAM_EGRESS"

// publicDestinationsOnly reports whether provider transports refuse non-public
// upstream addresses. Default is true for managed (per-tenant URLs reach only
// public endpoints) and false for self-hosted (in-cluster gateways are normal).
var publicDestinationsOnly = publicDestinationsOnlyFromEnv()

func publicDestinationsOnlyFromEnv() bool {
	if v := strings.TrimSpace(os.Getenv(restrictUpstreamEgressEnv)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return strings.EqualFold(strings.TrimSpace(os.Getenv("ROUTER_DEPLOYMENT_MODE")), "managed")
}

// restrictDestination runs in the dialer's Control hook, so a hostname is
// evaluated after DNS resolution rather than when its base URL was stored.
func restrictDestination(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrRestrictedDestination, address)
	}
	ip := net.ParseIP(host)
	if ip == nil || !isPublicIP(ip) {
		return fmt.Errorf("%w: %s", ErrRestrictedDestination, host)
	}
	return nil
}

func isPublicIP(ip net.IP) bool {
	// One destination policy for both dial paths: a range rejected for model
	// discovery must not be reachable through the inference transport either.
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	return isPublicAddr(address)
}
