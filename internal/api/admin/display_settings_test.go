package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type fakeAccountLister struct {
	accounts []*auth.SubscriptionAccount
	err      error
	owner    auth.SubscriptionOwner
}

func (f *fakeAccountLister) ListSubscriptionAccounts(_ context.Context, owner auth.SubscriptionOwner) ([]*auth.SubscriptionAccount, error) {
	f.owner = owner
	return f.accounts, f.err
}

// TestDisplaySettingsHandler covers the flag passthrough and unauthenticated rejection.
func TestDisplaySettingsHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("reports the installation flag", func(t *testing.T) {
		for _, hide := range []bool{true, false} {
			engine := gin.New()
			engine.GET("/v1/display-settings", func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{ID: "inst-1", HideTerminalSurfaces: hide})
			}, admin.DisplaySettingsHandler(&fakeAccountLister{}))

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, hide, body.HideTerminalSurfaces)
		}
	})

	t.Run("unauthenticated without an installation", func(t *testing.T) {
		engine := gin.New()
		engine.GET("/v1/display-settings", admin.DisplaySettingsHandler(&fakeAccountLister{}))

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run("lists the caller's disconnected plans once per provider", func(t *testing.T) {
		lister := &fakeAccountLister{accounts: []*auth.SubscriptionAccount{
			{Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateReconnectRequired},
			{Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateActive, Enabled: true},
			{Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateReconnectRequired},
			{Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateReconnectRequired},
		}}
		body := getDisplaySettings(t, lister)
		require.Equal(t, []auth.SubscriptionProvider{auth.SubscriptionProviderCodex}, body.DisconnectedPlans)
		require.Equal(t, "subscriber-1", lister.owner.SubscriberID)
	})

	t.Run("fails open to no disconnected plans", func(t *testing.T) {
		body := getDisplaySettings(t, &fakeAccountLister{err: errors.New("database down")})
		require.Empty(t, body.DisconnectedPlans)
		require.Empty(t, body.PlanStatuses)
	})
	t.Run("a connected account supersedes an old disconnected account", func(t *testing.T) {
		for _, state := range []auth.SubscriptionAccountState{
			auth.SubscriptionAccountStateActive,
			auth.SubscriptionAccountStateUnknown,
			auth.SubscriptionAccountStateCooldown,
			auth.SubscriptionAccountStateExhausted,
		} {
			t.Run(string(state), func(t *testing.T) {
				body := getDisplaySettings(t, &fakeAccountLister{accounts: []*auth.SubscriptionAccount{
					{Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateReconnectRequired},
					{Provider: auth.SubscriptionProviderClaude, State: state, Enabled: true},
				}})
				require.Empty(t, body.DisconnectedPlans)
			})
		}
	})
	t.Run("reports cooldown separately from disconnected credentials", func(t *testing.T) {
		reset := time.Date(2030, time.January, 10, 18, 0, 0, 0, time.UTC)
		body := getDisplaySettings(t, &fakeAccountLister{accounts: []*auth.SubscriptionAccount{
			{Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateReconnectRequired},
			{Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateCooldown, Enabled: true, CooldownUntil: &reset},
		}})
		require.Equal(t, []auth.SubscriptionProvider{auth.SubscriptionProviderCodex}, body.DisconnectedPlans)
		require.Len(t, body.PlanStatuses, 2)
		require.Equal(t, auth.SubscriptionProviderClaude, body.PlanStatuses[0].Provider)
		require.Equal(t, auth.SubscriptionAccountStateCooldown, body.PlanStatuses[0].State)
		require.Equal(t, &reset, body.PlanStatuses[0].CooldownUntil)
	})
}

type displaySettingsBody struct {
	DisconnectedPlans []auth.SubscriptionProvider `json:"disconnected_plans"`
	PlanStatuses      []struct {
		Provider      auth.SubscriptionProvider     `json:"provider"`
		State         auth.SubscriptionAccountState `json:"state"`
		CooldownUntil *time.Time                    `json:"cooldown_until"`
	} `json:"plan_statuses"`
}

func getDisplaySettings(t *testing.T, lister *fakeAccountLister) displaySettingsBody {
	t.Helper()
	engine := gin.New()
	engine.GET("/v1/display-settings", func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1"})
		c.Set("router_api_key", &auth.APIKey{ID: "key-1", CredentialSubjectID: "subscriber-1"})
	}, admin.DisplaySettingsHandler(lister))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body displaySettingsBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}
