package usage

import (
	"net/http"
	"strings"
)

// anthropicUnifiedPrefix is the common prefix of every Anthropic unified
// rate-limit header.
const anthropicUnifiedPrefix = "anthropic-ratelimit-unified-"

// codexLimitPrefixes cover the Codex backend's quota windows and credit
// balance. A bare x-codex- prefix would also capture conversation identifiers
// such as x-codex-parent-thread-id, which do not belong in limit telemetry.
var codexLimitPrefixes = []string{
	"x-codex-primary-",
	"x-codex-secondary-",
	"x-codex-credits-",
}

// RawAnthropicUnifiedHeaders returns every anthropic-ratelimit-unified-*
// header verbatim as a name->value map (lowercased names), or nil if none are
// present.
//
// Exists alongside ParseAnthropicUnifiedHeaders because Phase 0 telemetry needs
// the full header set verbatim to verify billing vocabulary before any cost math
// depends on it. Keeping unverified fields out of Snapshot means a header-shape
// change here can only affect the telemetry column, never CostFactor/Exhausted.
func RawAnthropicUnifiedHeaders(h http.Header) map[string]string {
	return rawHeadersWithPrefixes(h, anthropicUnifiedPrefix)
}

// RawCodexLimitHeaders returns the Codex quota-window and credit headers
// verbatim as a name->value map (lowercased names), or nil if none are present.
// Like RawAnthropicUnifiedHeaders it feeds telemetry only, never routing.
func RawCodexLimitHeaders(h http.Header) map[string]string {
	return rawHeadersWithPrefixes(h, codexLimitPrefixes...)
}

func rawHeadersWithPrefixes(h http.Header, prefixes ...string) map[string]string {
	var out map[string]string
	for name, vals := range h {
		if len(vals) == 0 {
			continue
		}
		lower := strings.ToLower(name)
		if !hasAnyPrefix(lower, prefixes) {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[lower] = vals[0]
	}
	return out
}

func hasAnyPrefix(value string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
