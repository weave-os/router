package auth

import (
	"net/http"
	"strings"
)

const RouterKeyHeader = "X-Weave-Router-Key"

func RoutingTokenFromHeaders(headers http.Header) string {
	if token := strings.TrimSpace(headers.Get(RouterKeyHeader)); token != "" {
		return token
	}
	if token := BearerToken(headers.Get("Authorization")); token != "" {
		return token
	}
	return strings.TrimSpace(headers.Get("x-api-key"))
}

func BearerToken(header string) string {
	const prefix = "Bearer "
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return ""
}
