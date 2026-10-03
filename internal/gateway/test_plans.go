package gateway

import (
	"net/http"
	"strings"
)

func testPlanSurface(r *http.Request) bool {
	if r.Method == http.MethodGet {
		return r.URL.Path == "/v1/test-plan/validate" || r.URL.Path == "/v1/models" || r.URL.Path == "/v1/router/hmm-roster" || r.URL.Path == "/v1/display-settings"
	}
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/messages", "/v1/messages/count_tokens", "/v1/route", "/v1/route/preview", "/v1/chat/completions", "/v1/responses":
		return true
	}
	return strings.HasPrefix(r.URL.Path, "/v1beta/models/") &&
		(strings.HasSuffix(r.URL.Path, ":generateContent") || strings.HasSuffix(r.URL.Path, ":streamGenerateContent"))
}
