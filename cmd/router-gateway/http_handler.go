package main

import (
	"net/http"
)

// gatewayHTTPHandler dispatches health checks locally and passes every other
// path through unchanged. ServeMux canonicalizes duplicate slashes, but an
// empty session id must reach session-cost validation and return 400.
func gatewayHTTPHandler(forwarder, readiness, startup http.Handler, capacity ...http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			switch r.URL.Path {
			case "/health":
				w.WriteHeader(http.StatusOK)
				return
			case "/readyz":
				readiness.ServeHTTP(w, r)
				return
			case "/startupz":
				startup.ServeHTTP(w, r)
				return
			case "/capacityz":
				if len(capacity) == 0 {
					w.WriteHeader(http.StatusServiceUnavailable)
				} else {
					capacity[0].ServeHTTP(w, r)
				}
				return
			}
		}
		forwarder.ServeHTTP(w, r)
	})
}
