package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
)

func TestSubscriptionPlanUsesOnlyOrganizationType(t *testing.T) {
	for _, scenario := range []struct {
		organization organizationType
		want         providers.SubscriptionPlan
	}{
		{organizationPro, providers.SubscriptionPlanPro},
		{organizationMax, providers.SubscriptionPlanMax},
		{organizationTeam, providers.SubscriptionPlanTeam},
		{organizationEnterprise, providers.SubscriptionPlanEnterprise},
		{"", providers.SubscriptionPlanUnknown},
		{organizationType("future-plan"), providers.SubscriptionPlanUnknown},
	} {
		t.Run(string(scenario.want)+"/"+string(scenario.organization), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/oauth/profile", r.URL.Path)
				require.Equal(t, "Bearer synthetic-token", r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"organization":{"organization_type":%q,"name":"Enterprise","billing_type":"usage"},"subscription_type":"enterprise","overage_in_use":true}`, scenario.organization)
			}))
			defer upstream.Close()

			plan, err := NewClient("", upstream.URL+"/v1").SubscriptionPlan(context.Background(), []byte("synthetic-token"))

			require.NoError(t, err)
			require.Equal(t, scenario.want, plan)
		})
	}
}

func TestSubscriptionPlanProfileFailures(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("synthetic-sensitive-response"))
			}))
			defer upstream.Close()
			plan, err := NewClient("", upstream.URL).SubscriptionPlan(context.Background(), []byte("synthetic-token"))
			require.Error(t, err)
			require.NotContains(t, err.Error(), "synthetic-sensitive-response")
			require.NotContains(t, err.Error(), "synthetic-token")
			require.Equal(t, providers.SubscriptionPlanUnknown, plan)
		})
	}
}

func TestSubscriptionPlanDoesNotForwardTokenOnRedirect(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	_, err := NewClient("", upstream.URL).SubscriptionPlan(context.Background(), []byte("synthetic-token"))
	require.Error(t, err)
	require.False(t, leaked)
}
