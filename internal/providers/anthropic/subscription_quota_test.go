package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionQuotaReadsIncludedWindows(t *testing.T) {
	weekly := time.Date(2030, 1, 8, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, body string
		want       SubscriptionQuota
		wantErr    bool
	}{
		{"available", `{"five_hour":{"utilization":12.0,"resets_at":"2030-01-01T05:00:00Z"},"seven_day":{"utilization":40.0,"resets_at":"2030-01-08T00:00:00Z"}}`, SubscriptionQuota{Available: true}, false},
		{"weekly_spent", `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":100.0,"resets_at":"2030-01-08T00:00:00Z"}}`, SubscriptionQuota{ResetAt: weekly}, false},
		{"model_window_spent_is_ignored", `{"five_hour":{"utilization":5},"seven_day":{"utilization":5},"seven_day_opus":{"utilization":100}}`, SubscriptionQuota{Available: true}, false},
		{"extra_usage_spent_is_ignored", `{"five_hour":{"utilization":5},"seven_day":{"utilization":5},"extra_usage":{"is_enabled":true,"utilization":100}}`, SubscriptionQuota{Available: true}, false},
		{"no_windows", `{}`, SubscriptionQuota{}, true},
		{"malformed", `not json`, SubscriptionQuota{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "/api/oauth/usage", r.URL.Path)
				assert.Equal(t, "Bearer synthetic-token", r.Header.Get("Authorization"))
				assert.Contains(t, r.Header.Get("anthropic-beta"), oauthBetaToken)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()

			quota, err := NewClient("", upstream.URL+"/v1").SubscriptionQuota(context.Background(), []byte("synthetic-token"))

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, quota)
		})
	}
}

func TestSubscriptionQuotaRejectsFailedStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("synthetic-sensitive-response"))
	}))
	defer upstream.Close()

	_, err := NewClient("", upstream.URL+"/v1").SubscriptionQuota(context.Background(), []byte("synthetic-token"))

	require.Error(t, err)
	require.NotContains(t, err.Error(), "synthetic-sensitive-response")
}
