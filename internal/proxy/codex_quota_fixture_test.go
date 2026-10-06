package proxy

import (
	"io"
	"net/http"
)

func serveSyntheticCodexQuota(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/wham/usage" {
		return false
	}
	_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}}}`)
	return true
}
