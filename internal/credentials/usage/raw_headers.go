package usage

import (
	"net/http"
	"strings"
)

const anthropicUnifiedPrefix = "anthropic-ratelimit-unified-"

var codexLimitPrefixes = []string{
	"x-codex-primary-",
	"x-codex-secondary-",
	"x-codex-credits-",
}

func RawAnthropicUnifiedHeaders(h http.Header) map[string]string {
	return rawHeadersWithPrefixes(h, anthropicUnifiedPrefix)
}

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
