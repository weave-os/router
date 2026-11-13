package subscription_verification_check_test

import (
	"io"
	"net/http"
)

func serveIncludedCodexQuota(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.Path != "/wham/usage" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`)
	return true
}
