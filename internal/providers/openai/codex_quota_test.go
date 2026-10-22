package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
	"weave-os/router/internal/requestcontext"

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
		{"usage_based_plan_without_rate_limit", `{"plan_type":"enterprise_cbp_usage_based","rate_limit":null,"spend_control":{"reached":false}}`, 0},
		{"reported_plan_without_windows", `{"plan_type":"business","rate_limit":{"allowed":true,"limit_reached":false}}`, 0},
		{"reported_plan_without_windows_reached", `{"plan_type":"business","rate_limit":{"allowed":false,"limit_reached":true}}`, 429},
		{"spend_control_reached", `{"plan_type":"enterprise_cbp_usage_based","rate_limit":null,"spend_control":{"reached":true}}`, 429},
		{"workspace_credits_depleted", `{"plan_type":"business","rate_limit":null,"rate_limit_reached_type":{"type":"workspace_owner_credits_depleted"}}`, 429},
		{"unknown_reached_type_ignored", `{"plan_type":"business","rate_limit":null,"rate_limit_reached_type":{"type":"something_new"}}`, 0},
		{"reported_plan_model_limit_without_rate_limit", `{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}},"additional_rate_limits":[{"normal_model_slug":"gpt-6.1-sol","rate_limit":null}]}`, 0},
		{"reported_plan_malformed_rate_limit", `{"plan_type":"business","rate_limit":{"allowed":true}}`, 503},
		{"model_specific_exhaustion", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}},"additional_rate_limits":[{"normal_model_slug":"gpt-6.1-sol","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}}]}`, 503},
		{"unrelated_model_limit", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}},"additional_rate_limits":[{"normal_model_slug":"other-model","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}}]}`, 0},
		{"missing_quota", `{}`, 503},
		{"malformed_quota", `not json`, 503},
		{"healthy", `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var inferenceCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/wham/usage" {
					require.Equal(t, http.MethodGet, r.Method)
					require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
					require.Equal(t, "test-account", r.Header.Get("ChatGPT-Account-ID"))
					require.Equal(t, codexOriginatorValue, r.Header.Get(codexOriginatorHeader))
					require.Equal(t, codexUserAgentValue, r.Header.Get(codexUserAgentHeader))
					_, _ = io.WriteString(w, tc.quota)
					return
				}
				inferenceCalls.Add(1)
				_, _ = io.WriteString(w, `{"id":"synthetic"}`)
			}))
			defer server.Close()
			client := NewClient("deployment-key", server.URL)
			client.SetCodexBaseURL(server.URL)
			err := client.Proxy(codexCtx("test-token", "test-account"), router.Decision{Model: "gpt-6.1-sol", Provider: providers.ProviderOpenAI}, providers.PreparedRequest{Endpoint: providers.EndpointResponses, Body: []byte(`{"model":"gpt-6.1-sol","input":"hi"}`)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			if tc.status == 0 {
				require.NoError(t, err)
				require.Equal(t, int32(1), inferenceCalls.Load())
			} else {
				var upstream *providers.UpstreamErrorResponse
				require.ErrorAs(t, err, &upstream)
				require.Equal(t, tc.status, upstream.Status)
				if tc.status == http.StatusTooManyRequests {
					require.Contains(t, string(upstream.Body), "Codex included quota is exhausted.")
				}
				require.Zero(t, inferenceCalls.Load())
			}
		})
	}
}

func TestCodexQuotaChecksFinalUpstreamModel(t *testing.T) {
	for _, credentialAlias := range []bool{false, true} {
		t.Run(map[bool]string{false: "catalog_map", true: "credential_alias"}[credentialAlias], func(t *testing.T) {
			var inferenceCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/wham/usage" {
					_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20}},"additional_rate_limits":[{"normal_model_slug":"wire-model","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}}]}`)
					return
				}
				inferenceCalls.Add(1)
				_, _ = io.WriteString(w, `{"id":"synthetic"}`)
			}))
			defer server.Close()
			client := NewClient("deployment-key", server.URL)
			client.SetCodexBaseURL(server.URL)
			ctx := codexCtx("test-token", "test-account")
			if credentialAlias {
				creds := *requestcontext.CredentialsFromContext(ctx)
				creds.ModelAliases = map[string]string{"gpt-6.1-sol": "wire-model"}
				ctx = context.WithValue(ctx, requestcontext.CredentialsContextKey{}, &creds)
			} else {
				client.modelIDMap = map[string]string{"gpt-6.1-sol": "wire-model"}
			}
			err := client.Proxy(ctx, router.Decision{Model: "gpt-6.1-sol", Provider: providers.ProviderOpenAI}, providers.PreparedRequest{Endpoint: providers.EndpointResponses, Body: []byte(`{"model":"gpt-6.1-sol","input":"hi"}`)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			var upstream *providers.UpstreamErrorResponse
			require.ErrorAs(t, err, &upstream)
			require.Equal(t, 503, upstream.Status)
			require.Contains(t, string(upstream.Body), "model_usage_limit_reached")
			require.Zero(t, inferenceCalls.Load())
		})
	}
}

func TestCodexQuotaPreflightPreservesAuthRejection(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var inferenceCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/wham/usage" {
					w.WriteHeader(status)
					_, _ = io.WriteString(w, "private upstream response")
					return
				}
				inferenceCalls.Add(1)
			}))
			defer server.Close()
			client := NewClient("deployment-key", server.URL)
			client.SetCodexBaseURL(server.URL)
			err := client.Proxy(codexCtx("test-token", "test-account"), router.Decision{Model: "gpt-6.1-sol", Provider: providers.ProviderOpenAI}, providers.PreparedRequest{Endpoint: providers.EndpointResponses, Body: []byte(`{"model":"gpt-6.1-sol","input":"hi"}`)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			var upstream *providers.UpstreamErrorResponse
			require.ErrorAs(t, err, &upstream)
			if status == http.StatusNotFound {
				require.Equal(t, 503, upstream.Status)
			} else {
				require.Equal(t, status, upstream.Status)
				require.Contains(t, string(upstream.Body), "Reconnect")
			}
			require.NotContains(t, string(upstream.Body), "private upstream response")
			require.Zero(t, inferenceCalls.Load())
		})
	}
}

func TestCodexQuotaEndpointUnavailableNeverInfers(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "http_failure", true: "deadline"}[timeout], func(t *testing.T) {
			var inferenceCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/wham/usage" {
					if timeout {
						<-r.Context().Done()
					} else {
						w.WriteHeader(http.StatusBadGateway)
					}
					return
				}
				inferenceCalls.Add(1)
			}))
			defer server.Close()
			client := NewClient("deployment-key", server.URL)
			client.SetCodexBaseURL(server.URL)
			started := time.Now()
			err := client.Proxy(codexCtx("test-token", "test-account"), router.Decision{Model: "gpt-6.1-sol", Provider: providers.ProviderOpenAI}, providers.PreparedRequest{Endpoint: providers.EndpointResponses, Body: []byte(`{"model":"gpt-6.1-sol","input":"hi"}`)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
			var upstream *providers.UpstreamErrorResponse
			require.ErrorAs(t, err, &upstream)
			require.Equal(t, 503, upstream.Status)
			require.True(t, providers.IsRetryable(err))
			require.Zero(t, inferenceCalls.Load())
			if timeout {
				require.GreaterOrEqual(t, time.Since(started), 2800*time.Millisecond)
				require.Less(t, time.Since(started), 5*time.Second)
			}
		})
	}
}

func TestCodexReachedTypeKeepsWindowReset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"plan_type":"plus","rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":40},"secondary_window":{"used_percent":100,"reset_at":1792600000}},"rate_limit_reached_type":{"type":"rate_limit_reached"}}`)
	}))
	defer server.Close()
	client := NewClient("deployment-key", server.URL)
	client.SetCodexBaseURL(server.URL)
	err := client.checkCodexIncludedQuota(codexCtx("test-token", "test-account"), "gpt-6.1-sol")
	var upstream *providers.UpstreamErrorResponse
	require.True(t, errors.As(err, &upstream))
	require.Equal(t, http.StatusTooManyRequests, upstream.Status)
	require.Contains(t, string(upstream.Body), "1792600000")
}
