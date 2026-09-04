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
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// sessionCostRepo serves one session's cost and records the scope it was asked
// for, so the handler's installation scoping is observable.
type sessionCostRepo struct {
	stubTelemetryRepo
	cost               proxy.SessionCost
	err                error
	seenInstallationID string
	seenSessionID      string
}

func (r *sessionCostRepo) GetSessionCost(_ context.Context, installationID, sessionID string) (proxy.SessionCost, error) {
	r.seenInstallationID = installationID
	r.seenSessionID = sessionID
	return r.cost, r.err
}

func sessionCostEngine(t *testing.T, repo proxy.TelemetryRepository, installation *auth.Installation) *gin.Engine {
	t.Helper()
	svc := proxy.NewService(nil, nil, nil, false, nil, nil, false, "", "", repo)
	engine := gin.New()
	engine.GET("/v1/sessions/:session_id/cost", func(c *gin.Context) {
		if installation != nil {
			c.Set("router_installation", installation)
		}
	}, admin.SessionCostHandler(svc))
	return engine
}

type sessionCostBody struct {
	SessionID              string  `json:"session_id"`
	RequestCount           int64   `json:"request_count"`
	ActualCostUSDMicros    int64   `json:"actual_cost_usd_micros"`
	RequestedCostUSDMicros int64   `json:"requested_cost_usd_micros"`
	SavingsUSDMicros       int64   `json:"savings_usd_micros"`
	SavingsUSD             float64 `json:"savings_usd"`
	InputTokens            int64   `json:"input_tokens"`
}

func TestSessionCostHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("reports savings as requested minus actual", func(t *testing.T) {
		repo := &sessionCostRepo{cost: proxy.SessionCost{
			SessionID:              "session-1",
			RequestCount:           3,
			ActualCostUSDMicros:    250_000,
			RequestedCostUSDMicros: 570_000,
			InputTokens:            1200,
			LastRecordedAt:         time.Unix(1700000000, 0).UTC(),
		}}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		var body sessionCostBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, "session-1", body.SessionID)
		require.Equal(t, int64(3), body.RequestCount)
		require.Equal(t, int64(320_000), body.SavingsUSDMicros)
		require.InDelta(t, 0.32, body.SavingsUSD, 1e-9)
		require.Equal(t, int64(1200), body.InputTokens)
		require.Equal(t, "session-1", repo.seenSessionID)
	})

	t.Run("scopes the lookup to the calling installation", func(t *testing.T) {
		installationID := uuid.NewString()
		repo := &sessionCostRepo{cost: proxy.SessionCost{SessionID: "session-1"}}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: installationID})

		engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, installationID, repo.seenInstallationID)
	})

	t.Run("reports a negative total when the router spent more", func(t *testing.T) {
		repo := &sessionCostRepo{cost: proxy.SessionCost{
			SessionID:              "session-1",
			ActualCostUSDMicros:    900_000,
			RequestedCostUSDMicros: 400_000,
		}}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		var body sessionCostBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, int64(-500_000), body.SavingsUSDMicros)
	})

	t.Run("404s an unknown or foreign session", func(t *testing.T) {
		repo := &sessionCostRepo{err: proxy.ErrSessionCostNotFound}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("500s a repository failure", func(t *testing.T) {
		repo := &sessionCostRepo{err: errors.New("postgres is down")}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("401s without an authenticated installation", func(t *testing.T) {
		repo := &sessionCostRepo{cost: proxy.SessionCost{SessionID: "session-1"}}
		engine := sessionCostEngine(t, repo, nil)

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, http.StatusUnauthorized, rec.Code)
		require.Empty(t, repo.seenSessionID, "an unauthenticated caller must not reach the repository")
	})
}
