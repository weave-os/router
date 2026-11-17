package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/server/middleware"
)

type flagOverridesProbe struct {
	status    int
	body      string
	overrides flags.Overrides
}

func runFlagOverridesHeader(t *testing.T, installation *auth.Installation, orgOverridesDisabled bool, header string) flagOverridesProbe {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if installation != nil {
			c.Set("router_installation", installation)
			if !orgOverridesDisabled {
				c.Request = c.Request.WithContext(flags.WithOverrides(c.Request.Context(), installation.FlagOverrides))
			}
		}
		c.Next()
	})
	engine.Use(middleware.WithFlagOverridesHeader((&auth.Service{}).WithFlagOverridesDisabled(orgOverridesDisabled)))
	var observed flags.Overrides
	engine.POST("/v1/messages", func(c *gin.Context) {
		observed, _ = flags.OverridesFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
	if header != "" {
		req.Header.Set(middleware.FlagOverridesHeader, header)
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	return flagOverridesProbe{status: rr.Code, body: rr.Body.String(), overrides: observed}
}

func flagHeaderInstallation(headersEnabled bool) *auth.Installation {
	return &auth.Installation{
		PolicyHeaderOverridesEnabled: headersEnabled,
		FlagOverrides: flags.Overrides{
			Bools: map[flags.Key]bool{flags.KeyPlannerEnabled: true, flags.KeySiblingFailover: true},
		},
	}
}

func TestFlagOverridesHeader_AuthorizedHeaderLayersOverStoredOverrides(t *testing.T) {
	probe := runFlagOverridesHeader(t, flagHeaderInstallation(true), false,
		`{"planner_enabled":false,"escalation_active_classifier":"none","loop_escalation_holdout_pct":40}`)

	require.Equal(t, http.StatusOK, probe.status)
	assert.Equal(t, map[flags.Key]bool{flags.KeyPlannerEnabled: false, flags.KeySiblingFailover: true}, probe.overrides.Bools)
	assert.Equal(t, map[flags.Key]string{flags.KeyEscalationActiveClassifier: "none"}, probe.overrides.Strings)
	assert.Equal(t, map[flags.Key]int{flags.KeyLoopEscalationHoldoutPct: 40}, probe.overrides.Ints)
}

func TestFlagOverridesHeader_IgnoredWithoutAuthorization(t *testing.T) {
	for name, scenario := range map[string]struct {
		installation         *auth.Installation
		orgOverridesDisabled bool
		wantBools            map[flags.Key]bool
	}{
		"installation without policy headers": {
			installation: flagHeaderInstallation(false),
			wantBools:    map[flags.Key]bool{flags.KeyPlannerEnabled: true, flags.KeySiblingFailover: true},
		},
		"deployment disables org overrides": {
			installation:         flagHeaderInstallation(true),
			orgOverridesDisabled: true,
		},
		"no installation": {},
	} {
		t.Run(name, func(t *testing.T) {
			probe := runFlagOverridesHeader(t, scenario.installation, scenario.orgOverridesDisabled, `{"planner_enabled":false}`)

			require.Equal(t, http.StatusOK, probe.status)
			assert.Equal(t, scenario.wantBools, probe.overrides.Bools)
		})
	}
}

func TestFlagOverridesHeader_AuthorizedInvalidHeaderIsRejected(t *testing.T) {
	for name, header := range map[string]string{
		"not json":              `planner_enabled=false`,
		"unknown flag":          `{"no_such_flag":true}`,
		"wrong kind":            `{"planner_enabled":"off"}`,
		"out of range":          `{"loop_escalation_holdout_pct":101}`,
		"shadow without active": `{"escalation_shadow_classifier":"xgb"}`,
	} {
		t.Run(name, func(t *testing.T) {
			probe := runFlagOverridesHeader(t, flagHeaderInstallation(true), false, header)

			require.Equal(t, http.StatusBadRequest, probe.status)
			assert.Contains(t, probe.body, `"code":"flag_overrides_malformed"`)
		})
	}
}

func TestFlagOverridesHeader_AbsentHeaderKeepsStoredOverrides(t *testing.T) {
	probe := runFlagOverridesHeader(t, flagHeaderInstallation(true), false, "")

	require.Equal(t, http.StatusOK, probe.status)
	assert.Equal(t, map[flags.Key]bool{flags.KeyPlannerEnabled: true, flags.KeySiblingFailover: true}, probe.overrides.Bools)
}
