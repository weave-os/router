package middleware_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/router"
	"weave-os/router/internal/server/middleware"
)

var (
	pinArtifactSHA = strings.Repeat("1", 64)
	pinRosterSHA   = strings.Repeat("2", 64)
	validPolicyPin = pinArtifactSHA + "@" + pinRosterSHA
)

type policyPinProbe struct {
	status    int
	body      []byte
	ctx       context.Context
	request   router.PolicyPinRequest
	requested bool
}

func runPolicyPinOverride(t *testing.T, installation *auth.Installation, header string, enabled bool) policyPinProbe {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		if installation != nil {
			c.Set("router_installation", installation)
		}
		c.Next()
	})
	if enabled {
		engine.Use(middleware.WithPolicyPinOverride())
	}

	var probe policyPinProbe
	engine.POST("/v1/messages", func(c *gin.Context) {
		probe.ctx = c.Request.Context()
		probe.request, probe.requested = router.PolicyPinRequestFrom(probe.ctx)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if header != "" {
		req.Header.Set(middleware.PolicyPinOverrideHeader, header)
	}
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)
	probe.status = rr.Code
	probe.body = rr.Body.Bytes()
	return probe
}

func TestPolicyPinOverride_AuthorizedInstallationHonoursPin(t *testing.T) {
	probe := runPolicyPinOverride(t, overrideEnabledInstallation(), validPolicyPin, true)

	require.Equal(t, http.StatusOK, probe.status)
	require.True(t, probe.requested, "the pin must be recorded on the request context")
	assert.True(t, probe.request.Authorized)
	assert.Equal(t, pinArtifactSHA, probe.request.Pin.ArtifactSHA256)
	assert.Equal(t, pinRosterSHA, probe.request.Pin.RosterSHA256)
	pin, honoured := router.HonouredPolicyPin(probe.ctx)
	assert.True(t, honoured)
	assert.Equal(t, validPolicyPin, pin.String())
}

func TestPolicyPinOverride_UnauthorizedInstallationRecordsButIgnoresPin(t *testing.T) {
	for name, installation := range map[string]*auth.Installation{
		"overrides disabled": {ID: "inst-plain"},
		"no installation":    nil,
	} {
		t.Run(name, func(t *testing.T) {
			probe := runPolicyPinOverride(t, installation, validPolicyPin, true)

			require.Equal(t, http.StatusOK, probe.status)
			require.True(t, probe.requested, "an ignored pin must still be recorded as requested")
			assert.False(t, probe.request.Authorized, "an unauthorized pin must not be honoured")
			assert.Equal(t, router.PolicyPin{}, probe.request.Pin, "an unauthorized header value must never be parsed")
			_, honoured := router.HonouredPolicyPin(probe.ctx)
			assert.False(t, honoured)
		})
	}
}

func TestPolicyPinOverride_UnauthorizedInstallationIgnoresHeaderValueEntirely(t *testing.T) {
	installation := &auth.Installation{ID: "inst-plain"}
	baseline := runPolicyPinOverride(t, installation, "", true)
	require.Equal(t, http.StatusOK, baseline.status)

	for name, raw := range map[string]string{
		"valid":     validPolicyPin,
		"malformed": "not-a-pin",
		"huge":      strings.Repeat("z", 1<<16),
	} {
		t.Run(name, func(t *testing.T) {
			probe := runPolicyPinOverride(t, installation, raw, true)

			assert.Equal(t, baseline.status, probe.status, "gate-off must serve the same status as a request without the header")
			assert.Equal(t, baseline.body, probe.body)
			require.True(t, probe.requested)
			assert.Equal(t, router.PolicyPinRequest{}, probe.request)
		})
	}
}

func TestPolicyPinOverride_MalformedPinIs400WithTypedCode(t *testing.T) {
	for name, raw := range map[string]string{
		"no separator": pinArtifactSHA,
		"short roster": pinArtifactSHA + "@deadbeef",
		"non hex":      strings.Repeat("x", 64) + "@" + pinRosterSHA,
	} {
		t.Run(name, func(t *testing.T) {
			probe := runPolicyPinOverride(t, overrideEnabledInstallation(), raw, true)

			require.Equal(t, http.StatusBadRequest, probe.status)
			assert.False(t, probe.requested, "the handler must not run on a malformed pin")
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(probe.body, &body))
			assert.Equal(t, string(router.PolicyPinErrorMalformed), body.Error.Code)
		})
	}
}

func TestPolicyPinOverride_MalformedPinFromUnauthorizedInstallationIsNot400(t *testing.T) {
	probe := runPolicyPinOverride(t, &auth.Installation{ID: "inst-plain"}, "not-a-pin", true)

	assert.Equal(t, http.StatusOK, probe.status, "authorization is checked before the value is parsed")
}

func TestPolicyPinOverride_AbsentHeaderLeavesNoMark(t *testing.T) {
	probe := runPolicyPinOverride(t, overrideEnabledInstallation(), "", true)

	require.Equal(t, http.StatusOK, probe.status)
	assert.False(t, probe.requested, "no header means no pin mark")
}

func TestPolicyPinOverride_NotRegisteredMeansHeaderHasNoEffect(t *testing.T) {
	for name, raw := range map[string]string{"valid": validPolicyPin, "malformed": "garbage"} {
		t.Run(name, func(t *testing.T) {
			probe := runPolicyPinOverride(t, overrideEnabledInstallation(), raw, false)

			assert.Equal(t, http.StatusOK, probe.status)
			assert.False(t, probe.requested, "with the flag off the header must leave no telemetry mark")
		})
	}
}
