package auth

import (
	"net/http"
	"strings"
)

// RouterKeyHeader preserves upstream Authorization when a separate router key is supplied.
const RouterKeyHeader = "X-Weave-Router-Key"

// RoutingTokenFromHeaders preserves the existing dedicated-header, bearer, x-api-key precedence.
func RoutingTokenFromHeaders(headers http.Header) string {
	if token := strings.TrimSpace(headers.Get(RouterKeyHeader)); token != "" {
		return token
	}
	if token := BearerToken(headers.Get("Authorization")); token != "" {
		return token
	}
	return strings.TrimSpace(headers.Get("x-api-key"))
}

// BearerToken parses the scheme case-insensitively without accepting an empty token.
func BearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}
