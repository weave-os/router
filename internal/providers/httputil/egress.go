package httputil

import (
	"errors"
	"fmt"
	"net"
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

func isPublicIP(ip net.IP) bool {
	switch {
	case ip.IsLoopback(),
		ip.IsPrivate(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsMulticast(),
		ip.IsUnspecified():
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return true
	}
	// Neither range has a stdlib predicate: 0.0.0.0/8 is "this host on this
	// network" (RFC 1122) and resolves to a local service on Linux, and
	// 100.64.0.0/10 is carrier-grade NAT (RFC 6598).
	if ip4[0] == 0 {
		return false
	}
	return !(ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127)
}
