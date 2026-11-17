package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/subscriptions"
)

func TestCodexResetUsesAuthenticatedSpecificCreditProtocol(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer own-token", r.Header.Get("Authorization"))
		assert.Equal(t, "own-workspace", r.Header.Get(requestcontext.ChatGPTAccountIDHeader))
		assert.Equal(t, codexOriginatorValue, r.Header.Get(codexOriginatorHeader))
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits":
			assert.Equal(t, http.MethodGet, r.Method)
			_, _ = io.WriteString(w, `{"available_count":2,"credits":[
			 {"id":"expiring","reset_type":"codex_rate_limits","status":"available","expires_at":"2030-01-02T03:04:05Z"},
			 {"id":"permanent","reset_type":"codex_rate_limits","status":"available","expires_at":null},
			 {"id":"used","reset_type":"codex_rate_limits","status":"redeemed"},
			 {"id":"other","reset_type":"other_product","status":"available"},
			 {"id":"busy","reset_type":"codex_rate_limits","status":"redeeming"}]}`)
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			assert.Equal(t, http.MethodPost, r.Method)
			var payload map[string]string
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&payload)) {
				return
			}
			assert.Equal(t, map[string]string{"credit_id": "expiring", "redeem_request_id": "durable-request"}, payload)
			_, _ = io.WriteString(w, `{"code":"reset","windows_reset":2}`)
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := NewClient("deployment-key", server.URL)
	client.SetCodexBaseURL(server.URL + "/backend-api/codex/")
	lease := subscriptions.Lease{AccessToken: "own-token", ProviderAccount: "own-workspace"}
	credits, err := client.CodexResetCredits(context.Background(), lease)
	require.NoError(t, err)
	require.Equal(t, []subscriptions.ResetCredit{{ID: "expiring", ExpiresAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}, {ID: "permanent"}}, credits)
	outcome, err := client.ConsumeCodexReset(context.Background(), lease, "expiring", "durable-request")
	require.NoError(t, err)
	require.Equal(t, subscriptions.ResetApplied, outcome)
}

func TestCodexResetRejectsUncertainInventoryAndRedemption(t *testing.T) {
	for _, body := range []string{`{}`, `not-json`, `{"credits":null}`, `{"credits":[{"expires_at":"invalid"}]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }))
			defer server.Close()
			client := NewClient("", server.URL)
			client.SetCodexBaseURL(server.URL)
			lease := subscriptions.Lease{AccessToken: "token", ProviderAccount: "account"}
			_, err := client.CodexResetCredits(context.Background(), lease)
			require.Error(t, err)
			_, err = client.ConsumeCodexReset(context.Background(), lease, "credit", "request")
			require.Error(t, err)
		})
	}
	for _, outcome := range []subscriptions.ResetOutcome{subscriptions.ResetApplied, subscriptions.ResetNoCredit, subscriptions.ResetAlreadyRedeemed, subscriptions.ResetNothingToReset} {
		t.Run(string(outcome), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintf(w, `{"code":%q}`, outcome) }))
			defer server.Close()
			client := NewClient("", server.URL)
			client.SetCodexBaseURL(server.URL)
			got, err := client.ConsumeCodexReset(context.Background(), subscriptions.Lease{AccessToken: "token", ProviderAccount: "account"}, "credit", "request")
			require.NoError(t, err)
			require.Equal(t, outcome, got)
		})
	}
}

func TestCodexResetRejectsOversizedResponse(t *testing.T) {
	body := `{"credits":[]}` + strings.Repeat(" ", 1<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	client := NewClient("", server.URL)
	client.SetCodexBaseURL(server.URL)
	_, err := client.CodexResetCredits(context.Background(), subscriptions.Lease{AccessToken: "token", ProviderAccount: "account"})
	require.ErrorContains(t, err, "codex reset response exceeds size limit")
}

func TestCodexResetQuotaRequiresAccountWideExhaustion(t *testing.T) {
	for _, tc := range []struct {
		body             string
		exhausted, fails bool
	}{
		{`{"rate_limit":{"allowed":false,"limit_reached":true}}`, true, false},
		{`{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":10}},"additional_rate_limits":[{"normal_model_slug":"gpt-6.1-sol","rate_limit":{"allowed":false,"limit_reached":true}}]}`, false, false},
		{`{}`, false, true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/wham/usage", r.URL.Path)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := NewClient("", server.URL)
			client.SetCodexBaseURL(server.URL)
			quotaExhausted, err := client.CodexQuotaExhausted(context.Background(), subscriptions.Lease{AccessToken: "token", ProviderAccount: "account"})
			require.Equal(t, tc.exhausted, quotaExhausted)
			require.Equal(t, tc.fails, err != nil)
		})
	}
}

func TestCodexResetQuotaDoesNotTreatPurchasedCreditsAsIncludedHeadroom(t *testing.T) {
	const quota = `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}},"credits":{"has_credits":true,"unlimited":false,"balance":"20.5"}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, quota)
	}))
	defer server.Close()
	client := NewClient("", server.URL)
	client.SetCodexBaseURL(server.URL)
	client.SetCodexExtraCreditsAllowed(true)
	ctx := requestcontext.WithSubscriptionExtraUsage(context.Background())
	exhausted, err := client.CodexQuotaExhausted(ctx, subscriptions.Lease{AccessToken: "token", ProviderAccount: "account"})
	require.NoError(t, err)
	require.True(t, exhausted, "purchased credits do not restore included quota for reset eligibility")
}

func TestCodexResetQuotaRequiresAccountCredentials(t *testing.T) {
	client := NewClient("", "")
	for _, lease := range []subscriptions.Lease{
		{},
		{AccessToken: "token"},
		{ProviderAccount: "account"},
	} {
		_, err := client.CodexQuotaExhausted(context.Background(), lease)
		require.ErrorContains(t, err, "codex reset requires authenticated account credentials")
	}
}
