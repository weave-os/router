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

func TestCodexQuotaPreflightObservesWeeklyUsageBeforeExhaustion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"rate_limit":{"allowed":false,"limit_reached":true,"secondary_window":{"used_percent":100,"reset_at":1792670400,"limit_window_seconds":604800}}}`)
	}))
	defer server.Close()
	client := NewClient("deployment-key", server.URL)
	client.SetCodexBaseURL(server.URL)
	var observed http.Header
	ctx := providers.WithUpstreamHeaderObserver(codexCtx("synthetic-token", "synthetic-account"), func(_ context.Context, headers http.Header) {
		observed = headers
	})
	err := client.checkCodexIncludedQuota(ctx, "synthetic-model")
	var upstream *providers.UpstreamErrorResponse
	require.ErrorAs(t, err, &upstream)
	require.Equal(t, http.StatusTooManyRequests, upstream.Status)
	require.Equal(t, "100", observed.Get("x-codex-secondary-used-percent"))
	require.Equal(t, "1792670400", observed.Get("x-codex-secondary-reset-at"))
	require.Equal(t, "10080", observed.Get("x-codex-secondary-window-minutes"))
}

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
				var unavailable *providers.QuotaVerificationUnavailableError
				if tc.status == http.StatusServiceUnavailable && tc.name != "model_specific_exhaustion" {
					require.ErrorAs(t, err, &unavailable)
					require.Equal(t, providers.QuotaVerificationFailureInvalidResponse, unavailable.Failure)
					require.Equal(t, http.StatusOK, unavailable.UpstreamStatus)
				} else {
					require.False(t, errors.As(err, &unavailable))
				}
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
				var unavailable *providers.QuotaVerificationUnavailableError
				require.ErrorAs(t, err, &unavailable)
				require.Equal(t, providers.QuotaVerificationFailureEndpoint, unavailable.Failure)
				require.Equal(t, status, unavailable.UpstreamStatus)
			} else {
				require.Equal(t, status, upstream.Status)
				require.Contains(t, string(upstream.Body), "Reconnect")
				var unavailable *providers.QuotaVerificationUnavailableError
				require.False(t, errors.As(err, &unavailable))
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
			var unavailable *providers.QuotaVerificationUnavailableError
			require.ErrorAs(t, err, &unavailable)
			if timeout {
				require.Equal(t, providers.QuotaVerificationFailureTimeout, unavailable.Failure)
				require.Zero(t, unavailable.UpstreamStatus)
			} else {
				require.Equal(t, providers.QuotaVerificationFailureEndpoint, unavailable.Failure)
				require.Equal(t, http.StatusBadGateway, unavailable.UpstreamStatus)
			}
			require.Zero(t, inferenceCalls.Load())
			if timeout {
				require.GreaterOrEqual(t, time.Since(started), 2800*time.Millisecond)
				require.Less(t, time.Since(started), 5*time.Second)
			}
		})
	}
}

type quotaReadFailure struct {
	err    error
	cancel context.CancelFunc
}

func (body quotaReadFailure) Read([]byte) (int, error) {
	if body.cancel != nil {
		body.cancel()
	}
	return 0, body.err
}

func (quotaReadFailure) Close() error { return nil }

func TestCodexQuotaVerificationFailureClassification(t *testing.T) {
	for _, fixture := range []struct {
		name           string
		transportErr   error
		readErr        error
		status         int
		body           string
		failure        providers.QuotaVerificationFailure
		upstreamStatus int
	}{
		{name: "transport", transportErr: io.ErrUnexpectedEOF, failure: providers.QuotaVerificationFailureTransport},
		{name: "internal timeout", transportErr: context.DeadlineExceeded, failure: providers.QuotaVerificationFailureTimeout},
		{name: "endpoint 504", status: http.StatusGatewayTimeout, body: "private upstream body", failure: providers.QuotaVerificationFailureEndpoint, upstreamStatus: http.StatusGatewayTimeout},
		{name: "endpoint 429", status: http.StatusTooManyRequests, failure: providers.QuotaVerificationFailureEndpoint, upstreamStatus: http.StatusTooManyRequests},
		{name: "read failure", status: http.StatusOK, readErr: io.ErrUnexpectedEOF, failure: providers.QuotaVerificationFailureTransport, upstreamStatus: http.StatusOK},
		{name: "body timeout", status: http.StatusOK, readErr: context.DeadlineExceeded, failure: providers.QuotaVerificationFailureTimeout, upstreamStatus: http.StatusOK},
		{name: "invalid JSON", status: http.StatusOK, body: "private upstream body", failure: providers.QuotaVerificationFailureInvalidResponse, upstreamStatus: http.StatusOK},
		{name: "invalid model quota", status: http.StatusOK, body: `{"plan_type":"pro","additional_rate_limits":[{"normal_model_slug":"gpt-6.1-sol","rate_limit":{"allowed":true}}]}`, failure: providers.QuotaVerificationFailureInvalidResponse, upstreamStatus: http.StatusOK},
		{name: "invalid window", status: http.StatusOK, body: `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":101}}}`, failure: providers.QuotaVerificationFailureInvalidResponse, upstreamStatus: http.StatusOK},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			client := NewClient("synthetic-paid-key", "")
			inferenceCalls := 0
			client.http = &http.Client{Transport: creditsRoundTripper(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/backend-api/wham/usage" {
					inferenceCalls++
					return creditsResponse(http.StatusOK, "unexpected inference"), nil
				}
				if fixture.transportErr != nil {
					return nil, fixture.transportErr
				}
				response := creditsResponse(fixture.status, fixture.body)
				if fixture.readErr != nil {
					response.Body = quotaReadFailure{err: fixture.readErr}
				}
				return response, nil
			})}
			err := creditsProxy(client, codexCtx("synthetic-token", "synthetic-account"))
			var upstream *providers.UpstreamErrorResponse
			require.ErrorAs(t, err, &upstream)
			require.Equal(t, http.StatusServiceUnavailable, upstream.Status)
			require.JSONEq(t, `{"error":{"type":"api_error","message":"Included Codex quota could not be verified."}}`, string(upstream.Body))
			var unavailable *providers.QuotaVerificationUnavailableError
			require.ErrorAs(t, errors.Join(errors.New("synthetic outer failure"), err), &unavailable)
			require.Equal(t, fixture.failure, unavailable.Failure)
			require.Equal(t, fixture.upstreamStatus, unavailable.UpstreamStatus)
			require.NotContains(t, unavailable.Error(), "private upstream body")
			require.False(t, errors.Is(err, context.DeadlineExceeded), "internal preflight timeout is not caller expiry")
			require.True(t, providers.IsRetryable(err))
			require.Zero(t, inferenceCalls)
		})
	}
}

func TestCodexQuotaRequestBuildFailureIsClassified(t *testing.T) {
	client := NewClient("synthetic-paid-key", "")
	client.SetCodexBaseURL(":invalid")
	err := client.checkCodexIncludedQuota(context.Background(), "gpt-6.1-sol")
	var unavailable *providers.QuotaVerificationUnavailableError
	require.ErrorAs(t, err, &unavailable)
	require.Equal(t, providers.QuotaVerificationFailureRequest, unavailable.Failure)
	require.Zero(t, unavailable.UpstreamStatus)
}

func TestCodexQuotaParentCancellationIsNotRetryable(t *testing.T) {
	for _, readingBody := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport", true: "body"}[readingBody], func(t *testing.T) {
			ctx, cancel := context.WithCancel(codexCtx("synthetic-token", "synthetic-account"))
			defer cancel()
			client := NewClient("synthetic-paid-key", "")
			client.http = &http.Client{Transport: creditsRoundTripper(func(*http.Request) (*http.Response, error) {
				if !readingBody {
					cancel()
					return nil, context.Canceled
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: quotaReadFailure{err: context.Canceled, cancel: cancel}}, nil
			})}
			err := creditsProxy(client, ctx)
			require.ErrorIs(t, err, context.Canceled)
			require.False(t, providers.IsRetryable(err))
			var unavailable *providers.QuotaVerificationUnavailableError
			require.False(t, errors.As(err, &unavailable))
		})
	}
	ctx, cancel := context.WithDeadline(codexCtx("synthetic-token", "synthetic-account"), time.Unix(1, 0))
	defer cancel()
	client := NewClient("synthetic-paid-key", "")
	client.http = &http.Client{Transport: creditsRoundTripper(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	err := client.checkCodexIncludedQuota(ctx, "gpt-6.1-sol")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, providers.IsRetryable(err))
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
