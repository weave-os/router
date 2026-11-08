package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
			}, admin.DisplaySettingsHandler(&fakeAccountLister{}, nil, nil))

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

	t.Run("hides surfaces for an active blind experiment subject in either arm", func(t *testing.T) {
		for _, state := range []auth.BlindExperimentState{
			{Active: true, Arm: auth.BlindExperimentArmRouterOn},
			{Active: true, Arm: auth.BlindExperimentArmPassthrough},
			{Active: false, Arm: auth.BlindExperimentArmRouterOn},
		} {
			engine := gin.New()
			engine.GET("/v1/display-settings", func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{ID: "inst-1"})
			}, admin.DisplaySettingsHandler(&fakeAccountLister{}, func(c *gin.Context) context.Context {
				return context.WithValue(c.Request.Context(), auth.BlindExperimentContextKey{}, state)
			}, nil))

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, state.Active, body.HideTerminalSurfaces, "arm %s active %v", state.Arm, state.Active)
		}
	})

	t.Run("hides unidentified callers while the installation runs an experiment", func(t *testing.T) {
		for _, tc := range []struct {
			name              string
			userID            string
			experimentEnabled bool
			wantHidden        bool
		}{
			{name: "unidentified caller, experiment enabled", experimentEnabled: true, wantHidden: true},
			{name: "unidentified caller, no experiment"},
			{name: "identified non-subject, experiment enabled", userID: "user-1", experimentEnabled: true},
		} {
			engine := gin.New()
			engine.GET("/v1/display-settings", func(c *gin.Context) {
				c.Set("router_installation", &auth.Installation{ID: "inst-1"})
			}, admin.DisplaySettingsHandler(&fakeAccountLister{}, func(c *gin.Context) context.Context {
				if tc.userID == "" {
					return c.Request.Context()
				}
				return context.WithValue(c.Request.Context(), auth.UserIDContextKey{}, tc.userID)
			}, fakeExperimentChecker{enabled: tc.experimentEnabled}))

			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			var body struct {
				HideTerminalSurfaces bool `json:"hide_terminal_surfaces"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			require.Equal(t, tc.wantHidden, body.HideTerminalSurfaces, tc.name)
		}
	})

	t.Run("unauthenticated without an installation", func(t *testing.T) {
		engine := gin.New()
		engine.GET("/v1/display-settings", admin.DisplaySettingsHandler(&fakeAccountLister{}, nil, nil))

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run("lists the caller's disconnected plans once per provider", func(t *testing.T) {
		lister := &fakeAccountLister{accounts: []*auth.SubscriptionAccount{
			{Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateReconnectRequired},
			{Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateActive},
			{Provider: auth.SubscriptionProviderClaude, State: auth.SubscriptionAccountStateReconnectRequired},
			{Provider: auth.SubscriptionProviderCodex, State: auth.SubscriptionAccountStateReconnectRequired},
		}}
		body := getDisplaySettings(t, lister)
		require.Equal(t, []string{"claude", "codex"}, body.DisconnectedPlans)
		require.Equal(t, "subscriber-1", lister.owner.SubscriberID)
	})

	t.Run("does not list plans that are usable or only out of quota", func(t *testing.T) {
		var accounts []*auth.SubscriptionAccount
		for _, provider := range []auth.SubscriptionProvider{auth.SubscriptionProviderClaude, auth.SubscriptionProviderCodex} {
			for _, state := range []auth.SubscriptionAccountState{
				auth.SubscriptionAccountStateActive,
				auth.SubscriptionAccountStateExhausted,
				auth.SubscriptionAccountStateCooldown,
				auth.SubscriptionAccountStateDisabled,
				auth.SubscriptionAccountStateUnknown,
			} {
				accounts = append(accounts, &auth.SubscriptionAccount{Provider: provider, State: state})
			}
		}
		body := getDisplaySettings(t, &fakeAccountLister{accounts: accounts})
		require.Equal(t, []string{}, body.DisconnectedPlans)
	})

	t.Run("fails open to no disconnected plans", func(t *testing.T) {
		body := getDisplaySettings(t, &fakeAccountLister{err: errors.New("database down")})
		require.Equal(t, []string{}, body.DisconnectedPlans)
	})
}

func getDisplaySettings(t *testing.T, lister *fakeAccountLister) struct {
	DisconnectedPlans []string `json:"disconnected_plans"`
} {
	t.Helper()
	engine := gin.New()
	engine.GET("/v1/display-settings", func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1"})
		c.Set("router_api_key", &auth.APIKey{ID: "key-1", CredentialSubjectID: "subscriber-1"})
	}, admin.DisplaySettingsHandler(lister, nil, nil))

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/display-settings", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		DisconnectedPlans []string `json:"disconnected_plans"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

type fakeExperimentChecker struct{ enabled bool }

func (f fakeExperimentChecker) InstallationBlindExperimentEnabled(context.Context, string) bool {
	return f.enabled
}
