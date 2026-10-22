package openai

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func TestCodexExtraCreditsExplicitOptIn(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "default_included_only", true: "opted_in"}[allowed], func(t *testing.T) {
			inference := 0
			client := NewClient("unused-deployment-key", "")
			client.SetCodexExtraCreditsAllowed(allowed)
			client.http = &http.Client{Transport: creditsRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/backend-api/wham/usage" {
					return creditsResponse(200, `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}},"credits":{"has_credits":true,"unlimited":false,"balance":"20.5"}}`), nil
				}
				inference++
				require.Equal(t, "chatgpt.com", r.URL.Host)
				require.Equal(t, "Bearer synthetic-token", r.Header.Get("Authorization"))
				return creditsResponse(200, `{"id":"synthetic"}`), nil
			})}
			err := creditsProxy(client, codexCtx("synthetic-token", "synthetic-account"))
			if allowed {
				require.NoError(t, err)
				require.Equal(t, 1, inference)
			} else {
				require.Error(t, err)
				require.Zero(t, inference)
			}
		})
	}
}

func TestCodexExtraCreditsAdmissionBoundaries(t *testing.T) {
	const spent = `{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}`
	const funded = `{"has_credits":true,"unlimited":false,"balance":"20.5"}`
	for _, tc := range []struct {
		name, quota, credits, extra string
		allowed                     bool
	}{
		{"finite", spent, funded, "", true},
		{"unlimited", spent, `{"has_credits":false,"unlimited":true}`, "", true},
		{"no_credits", spent, `{"has_credits":false,"balance":"20"}`, "", false},
		{"zero", spent, `{"has_credits":true,"balance":"0"}`, "", false},
		{"negative", spent, `{"has_credits":true,"balance":"-1"}`, "", false},
		{"nan", spent, `{"has_credits":true,"balance":"NaN"}`, "", false},
		{"infinite", spent, `{"has_credits":true,"balance":"+Inf"}`, "", false},
		{"missing_balance", spent, `{"has_credits":true}`, "", true},
		{"null_balance", spent, `{"has_credits":true,"balance":null}`, "", true},
		{"numeric_balance", spent, `{"has_credits":true,"balance":20}`, "", false},
		{"missing_credits", spent, `null`, "", false},
		{"unknown_quota", `null`, funded, "", false},
		{"missing_quota_windows", `{"allowed":false,"limit_reached":true}`, funded, "", false},
		{"invalid_window", `{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":-1}}`, funded, "", false},
		{"hidden_balance_spend_reached", spent, `{"has_credits":true,"balance":null}`, `,"spend_control":{"reached":true}`, false},
		{"hidden_balance_member_zero", spent, `{"has_credits":true}`, `,"spend_control":{"reached":false,"individual_limit":{"remaining":"0"}}`, false},
		{"hidden_balance_false", spent, `{"has_credits":false,"balance":null}`, "", false},
		{"empty_balance", spent, `{"has_credits":true,"balance":""}`, "", false},
		{"spend_reached", spent, funded, `,"spend_control":{"reached":true}`, false},
		{"spend_unknown", spent, funded, `,"spend_control":{}`, false},
		{"member_zero", spent, funded, `,"spend_control":{"reached":false,"individual_limit":{"remaining":"0"}}`, false},
		{"member_funded", spent, funded, `,"spend_control":{"reached":false,"individual_limit":{"remaining":"3"}}`, true},
		{"member_unknown", spent, funded, `,"spend_control":{"reached":false,"individual_limit":{}}`, false},
		{"duplicate_spend_last_reached", spent, funded, `,"spend_control":{"reached":false},"spend_control":{"reached":true}`, false},
		{"duplicate_spend_last_available", spent, funded, `,"spend_control":{"reached":true},"spend_control":{"reached":false}`, true},
		{"duplicate_reason_last_member", spent, funded, `,"rate_limit_reached_type":{"type":"rate_limit_reached"},"rate_limit_reached_type":{"type":"workspace_member_usage_limit_reached"}`, false},
		{"member_limit", spent, funded, `,"rate_limit_reached_type":{"type":"workspace_member_usage_limit_reached"}`, false},
		{"owner_limit", spent, funded, `,"rate_limit_reached_type":{"type":"workspace_owner_credits_depleted"}`, false},
		{"included_limit", spent, funded, `,"rate_limit_reached_type":{"type":"rate_limit_reached"}`, true},
		{"unknown_limit", spent, funded, `,"rate_limit_reached_type":{"type":"future_reason"}`, false},
		{"invalid_model_quota", spent, funded, `,"additional_rate_limits":[{"normal_model_slug":"gpt-6.1-sol","rate_limit":null}]`, false},
		{"spent_model_quota", spent, funded, `,"additional_rate_limits":[{"normal_model_slug":"gpt-6.1-sol","rate_limit":` + spent + `}]`, false},
		{"healthy_ignores_credit_schema", `{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":5}}`, `"future-schema"`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inference := 0
			client := NewClient("unused-deployment-key", "")
			client.SetCodexExtraCreditsAllowed(true)
			client.http = &http.Client{Transport: creditsRoundTripper(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "chatgpt.com", r.URL.Host)
				if r.URL.Path == "/backend-api/wham/usage" {
					return creditsResponse(200, fmt.Sprintf(`{"rate_limit":%s,"credits":%s%s}`, tc.quota, tc.credits, tc.extra)), nil
				}
				inference++
				return creditsResponse(200, `{"id":"synthetic"}`), nil
			})}
			err := creditsProxy(client, codexCtx("synthetic-token", "synthetic-account"))
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, 1, inference)
			} else {
				require.Error(t, err)
				require.Zero(t, inference)
			}
		})
	}
}

func TestCodexExtraCreditsPreservesProviderRejection(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			inference := 0
			client := NewClient("unused-deployment-key", "")
			client.SetCodexExtraCreditsAllowed(true)
			client.http = &http.Client{Transport: creditsRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/backend-api/wham/usage" {
					return creditsResponse(200, `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}},"credits":{"has_credits":true,"balance":"2"}}`), nil
				}
				inference++
				return creditsResponse(status, `{"error":{"type":"insufficient_quota"}}`), nil
			})}
			var upstream *providers.UpstreamErrorResponse
			require.ErrorAs(t, creditsProxy(client, codexCtx("synthetic-token", "synthetic-account")), &upstream)
			require.Equal(t, status, upstream.Status)
			require.Equal(t, 1, inference, "provider refusal must not be bypassed")
		})
	}
}

type creditsRoundTripper func(*http.Request) (*http.Response, error)

func (f creditsRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func creditsResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func creditsProxy(client *Client, ctx context.Context) error {
	return client.Proxy(ctx, router.Decision{Model: "gpt-6.1-sol", Provider: providers.ProviderOpenAI}, providers.PreparedRequest{Endpoint: providers.EndpointResponses, Body: []byte(`{"model":"gpt-6.1-sol","input":"synthetic"}`)}, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
}

func TestCodexExtraCreditsPreservesWindowlessPlans(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, tc := range []struct {
			name, body string
			allowed    bool
		}{
			{"usage_based", `{"plan_type":"enterprise_cbp_usage_based","rate_limit":null,"spend_control":{"reached":false}}`, true},
			{"windowless_included", `{"plan_type":"business","rate_limit":{"allowed":true,"limit_reached":false}}`, true},
			{"windowless_spent", `{"plan_type":"business","rate_limit":{"allowed":false,"limit_reached":true},"credits":{"has_credits":true,"balance":"20"}}`, false},
			{"workspace_limit", `{"plan_type":"business","rate_limit":null,"spend_control":{"reached":true},"credits":{"has_credits":true,"balance":"20"}}`, false},
			{"workspace_credits_depleted", `{"plan_type":"business","rate_limit":null,"rate_limit_reached_type":{"type":"workspace_owner_credits_depleted"},"credits":{"has_credits":true,"balance":"20"}}`, false},
		} {
			t.Run(fmt.Sprintf("%t/%s", enabled, tc.name), func(t *testing.T) {
				calls := 0
				client := NewClient("unused-deployment-key", "")
				client.SetCodexExtraCreditsAllowed(enabled)
				client.http = &http.Client{Transport: creditsRoundTripper(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/backend-api/wham/usage" {
						return creditsResponse(200, tc.body), nil
					}
					calls++
					return creditsResponse(200, `{"id":"synthetic"}`), nil
				})}
				err := creditsProxy(client, codexCtx("synthetic-token", "synthetic-account"))
				if tc.allowed {
					require.NoError(t, err)
					require.Equal(t, 1, calls)
				} else {
					require.Error(t, err)
					require.Zero(t, calls)
				}
			})
		}
	}
}
