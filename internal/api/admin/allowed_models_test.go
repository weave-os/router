package admin_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/api/admin"
	"workweave/router/internal/auth"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubRoutableModels struct {
	models map[string]struct{}
}

func (s stubRoutableModels) RoutableModels() map[string]struct{} { return s.models }

// putAllowedModels drives the handler behind a middleware that injects an
// already-authed installation, so the request reaches the validation guards
// without a real auth flow. authSvc is nil: every case here must be rejected
// before the handler touches it.
func putAllowedModels(t *testing.T, routable admin.RoutableModelsSource, allowed []string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.PUT("/admin/v1/allowed-models", func(c *gin.Context) {
		c.Set("router_installation", &auth.Installation{ID: "inst-1"})
	}, admin.UpdateAllowedModelsHandler(nil, nil, routable))

	body, err := json.Marshal(map[string][]string{"allowed": allowed})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPut, "/admin/v1/allowed-models", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// claude-opus-4-8 is a real catalog row with no tier, so it passes catalog
// membership yet can never be routed. Saving it alone would desugar into
// "exclude every routable model" and 400 every routed request.
func TestUpdateAllowedModelsHandler_RejectsAllowlistWithNoRoutableMember(t *testing.T) {
	rec := putAllowedModels(t,
		stubRoutableModels{models: map[string]struct{}{"claude-opus-4-7": {}}},
		[]string{"claude-opus-4-8"},
	)

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body["error"], "no model this deployment can route")
}

// Membership validation still runs and still reports unknown IDs distinctly,
// so a typo does not get reported as a routability problem.
func TestUpdateAllowedModelsHandler_RejectsUnknownModelBeforeRoutabilityCheck(t *testing.T) {
	rec := putAllowedModels(t,
		stubRoutableModels{models: map[string]struct{}{"claude-opus-4-7": {}}},
		[]string{"not-a-real-model"},
	)

	require.Equal(t, http.StatusBadRequest, rec.Code)

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotContains(t, body["error"], "no model this deployment can route")
}
