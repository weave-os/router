package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	ActualCostUSD          float64 `json:"actual_cost_usd"`
	RequestedCostUSDMicros int64   `json:"requested_cost_usd_micros"`
	RequestedCostUSD       float64 `json:"requested_cost_usd"`
	SavingsUSDMicros       int64   `json:"savings_usd_micros"`
	SavingsUSD             float64 `json:"savings_usd"`
	InputTokens            int64   `json:"input_tokens"`
	OutputTokens           int64   `json:"output_tokens"`
	CacheCreationTokens    int64   `json:"cache_creation_tokens"`
	CacheReadTokens        int64   `json:"cache_read_tokens"`
	LastRecordedAt         string  `json:"last_recorded_at"`
}

type publicErrorBody struct {
	Message     string  `json:"message"`
	Description *string `json:"description"`
}

func decodePublicError(t *testing.T, rec *httptest.ResponseRecorder) publicErrorBody {
	t.Helper()
	var body publicErrorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.Message)
	return body
}

func TestSessionCostHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("reports the public cost fields plus savings", func(t *testing.T) {
		repo := &sessionCostRepo{cost: proxy.SessionCost{
			SessionID:              "session-1",
			RequestCount:           3,
			ActualCostUSDMicros:    250_000,
			RequestedCostUSDMicros: 570_000,
			InputTokens:            1200,
			OutputTokens:           340,
			CacheCreationTokens:    56,
			CacheReadTokens:        7800,
			LastRecordedAt:         time.Date(2026, 9, 28, 12, 30, 45, 123456789, time.FixedZone("PDT", -7*60*60)),
		}}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		var body sessionCostBody
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, sessionCostBody{
			SessionID:              "session-1",
			RequestCount:           3,
			ActualCostUSDMicros:    250_000,
			ActualCostUSD:          0.25,
			RequestedCostUSDMicros: 570_000,
			RequestedCostUSD:       0.57,
			SavingsUSDMicros:       320_000,
			SavingsUSD:             0.32,
			InputTokens:            1200,
			OutputTokens:           340,
			CacheCreationTokens:    56,
			CacheReadTokens:        7800,
			LastRecordedAt:         "2026-09-28T19:30:45.123456789Z",
		}, body)
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
		decodePublicError(t, rec)
	})

	t.Run("400s a session id over the identifier limit", func(t *testing.T) {
		repo := &sessionCostRepo{cost: proxy.SessionCost{SessionID: "session-1"}}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+strings.Repeat("s", proxy.MaxClientIdentifierLen+1)+"/cost", nil))

		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := decodePublicError(t, rec)
		require.NotNil(t, body.Description)
		require.Empty(t, repo.seenSessionID, "an invalid id must not reach the repository")
	})

	t.Run("accepts a session id at the identifier limit", func(t *testing.T) {
		repo := &sessionCostRepo{cost: proxy.SessionCost{SessionID: "session-1"}}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})
		sessionID := strings.Repeat("s", proxy.MaxClientIdentifierLen)

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/"+sessionID+"/cost", nil))

		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, sessionID, repo.seenSessionID)
	})

	t.Run("503s when telemetry storage is not configured", func(t *testing.T) {
		engine := sessionCostEngine(t, nil, &auth.Installation{ID: uuid.NewString()})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
		decodePublicError(t, rec)
	})

	t.Run("500s a repository failure without leaking it", func(t *testing.T) {
		repo := &sessionCostRepo{err: errors.New("postgres is down")}
		engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-1/cost", nil))

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		decodePublicError(t, rec)
		require.NotContains(t, rec.Body.String(), "postgres")
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

func TestSessionCostContextExpiry(t *testing.T) {
	for _, age := range []time.Duration{time.Minute, 6 * time.Minute} {
		t.Run(age.String(), func(t *testing.T) {
			now := time.Now().Add(-age)
			repo := &sessionCostRepo{cost: proxy.SessionCost{SessionID: "session-context", ContextSnapshot: &proxy.ContextSnapshot{
				Version: 1, EstimateKind: proxy.ContextEstimateApproximate, EstimateTokens: 72000, ContextWindow: 128000, OutputReserveTokens: 8000,
				ServedModel: "gpt-5.6-sol", RequestID: "test-request", RequestedAt: now.Add(-time.Second), RecordedAt: now,
			}}}
			engine := sessionCostEngine(t, repo, &auth.Installation{ID: uuid.NewString()})
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/sessions/session-context/cost", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			if age < proxy.ContextSnapshotTTL {
				require.Contains(t, body, "context_snapshot")
			} else {
				require.NotContains(t, body, "context_snapshot")
			}
		})
	}
}
