package httputil

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ErrRestrictedDestination is the dial error when an upstream resolves outside
// the public internet on a transport built for public destinations only.
var ErrRestrictedDestination = errors.New("upstream destination is not publicly routable")

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

// specialPurposePrefixes are the IANA special-purpose ranges that carry no
// stdlib predicate. Each is non-global by registry definition, and several
// route somewhere locally in practice: 0.0.0.0/8 reaches a local service on
// Linux, and fec0::/10 was site-local before deprecation and is still honored
// by some stacks. Listing the registry beats hand-picking the ranges an
// attacker might think of.
var specialPurposePrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // this host on this network (RFC 1122)
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT (RFC 6598)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay anycast (deprecated)
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking (RFC 2544)
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, incl. 255.255.255.255
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("100::/64"),        // discard-only
	netip.MustParsePrefix("2001::/23"),       // IETF protocol assignments, incl. Teredo
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4
	netip.MustParsePrefix("fec0::/10"),       // site-local (deprecated)
}

// isPublicIP reports whether ip is globally routable: outside the ranges the
// stdlib names, and outside the special-purpose registry above.
func isPublicIP(ip net.IP) bool {
	switch {
	case ip.IsLoopback(),
		ip.IsPrivate(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsUnspecified():
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	// A v4-mapped v6 address must be judged as the v4 address it names.
	addr = addr.Unmap()
	for _, prefix := range specialPurposePrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
