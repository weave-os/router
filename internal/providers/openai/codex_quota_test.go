package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func TestCodexIncludedQuotaPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, quota string
		status      int
	}{
		{"exhausted_with_purchased_credits", `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}},"credits":{"has_credits":true,"balance":"57091"}}`, 429},
		{"exhausted_secondary", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20},"secondary_window":{"used_percent":100}}}`, 429},
		{"missing_window", `{"rate_limit":{"allowed":true,"limit_reached":false}}`, 503},
		{"model_specific_exhaustion", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}},"additional_rate_limits":[{"normal_model_slug":"gpt-6.1-sol","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}}]}`, 429},
		{"unrelated_model_limit", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}},"additional_rate_limits":[{"normal_model_slug":"other-model","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}}]}`, 0},
		{"missing_quota", `{}`, 503},
		{"malformed_quota", `not json`, 503},
		{"healthy", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inferenceCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/wham/usage" {
					require.Equal(t, http.MethodGet, r.Method)
					require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
					require.Equal(t, "test-account", r.Header.Get("ChatGPT-Account-ID"))
					_, _ = io.WriteString(w, tc.quota)
					return
				}
				inferenceCalls++
				_, _ = io.WriteString(w, `{"id":"synthetic"}`)
			}))
			defer server.Close()
			client := NewClient("deployment-key", server.URL)
			client.SetCodexBaseURL(server.URL)
			err := client.Proxy(codexCtx("test-token", "test-account"), router.Decision{Model: "gpt-6.1-sol", Provider: providers.ProviderOpenAI}, providers.PreparedRequest{Endpoint: providers.EndpointResponses, Body: []byte(`{"model":"gpt-6.1-sol","input":"hi"}`)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			if tc.status == 0 {
				require.NoError(t, err)
				require.Equal(t, 1, inferenceCalls)
			} else {
				var upstream *providers.UpstreamErrorResponse
				require.ErrorAs(t, err, &upstream)
				require.Equal(t, tc.status, upstream.Status)
				require.Zero(t, inferenceCalls)
			}
		})
	}
}
