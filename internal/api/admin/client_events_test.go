package admin_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/api/admin"
	"weave-os/router/internal/auth"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const clientEventTestToken = "rk_client_event"

// clientEventKeyRepo authenticates exactly one token; every other hash is a
// no-rows miss so the middleware returns a real 401.
type clientEventKeyRepo struct{ auth.APIKeyRepository }

func (clientEventKeyRepo) GetActiveByHashWithInstallation(_ context.Context, hash string) (*auth.APIKey, *auth.Installation, error) {
	if hash != auth.HashAPIKeySHA256(clientEventTestToken) {
		return nil, nil, sql.ErrNoRows
	}
	return &auth.APIKey{ID: "key", InstallationID: "installation", Scope: auth.ScopeRouting, CredentialSubjectID: "subject"},
		&auth.Installation{ID: "installation", ExternalID: "org-test"}, nil
}

func (clientEventKeyRepo) MarkUsed(context.Context, string) (bool, error) { return false, nil }

type clientEventInstallationRepo struct{ auth.InstallationRepository }

func (clientEventInstallationRepo) MarkFirstRequestServed(context.Context, string) error { return nil }

type lifecycleRecorder struct{ events []auth.HarnessLifecycleEvent }

func (*lifecycleRecorder) APIKeyFirstUsed(auth.APIKeyFirstUsedEvent)             {}
func (*lifecycleRecorder) SubscriptionConnected(auth.SubscriptionConnectedEvent) {}
func (r *lifecycleRecorder) HarnessLifecycle(event auth.HarnessLifecycleEvent) {
	r.events = append(r.events, event)
}

func clientEventEngine(t *testing.T, observer auth.OnboardingObserver, now time.Time) *gin.Engine {
	t.Helper()
	svc := auth.NewService(clientEventInstallationRepo{}, clientEventKeyRepo{}, nil, nil, auth.NoOpAPIKeyCache{}, nil, func() time.Time { return now })
	if observer != nil {
		svc.WithOnboardingObserver(observer)
	}
	engine := gin.New()
	engine.POST("/v1/client-events", middleware.WithAuth(svc, false), admin.ClientEventHandler(svc))
	return engine
}

func postClientEvent(engine *gin.Engine, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/client-events", strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	return response
}

func TestClientEventHandlerReportsLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	recorder := &lifecycleRecorder{}
	engine := clientEventEngine(t, recorder, now)

	response := postClientEvent(engine, clientEventTestToken, `{"action":"off","harness":"codex","extra":"ignored"}`)

	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	require.Empty(t, response.Body.String())
	require.Equal(t, []auth.HarnessLifecycleEvent{{
		InstallationExternalID: "org-test", CredentialSubjectID: "subject", APIKeyID: "key",
		Harness: auth.LifecycleHarnessCodex, Action: auth.HarnessLifecycleActionOff, OccurredAt: now,
	}}, recorder.events)
}

func TestClientEventHandlerRejectsInvalidBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, body := range map[string]string{
		"empty":           ``,
		"missing action":  `{"harness":"claude_code"}`,
		"missing harness": `{"action":"on"}`,
		"unknown action":  `{"action":"status","harness":"claude_code"}`,
		"unknown harness": `{"action":"on","harness":"cursor"}`,
		"short harness":   `{"action":"on","harness":"claude"}`,
		"not json":        `action=on`,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := &lifecycleRecorder{}
			engine := clientEventEngine(t, recorder, time.Now())
			response := postClientEvent(engine, clientEventTestToken, body)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.JSONEq(t, `{"error":"invalid_body"}`, response.Body.String())
			require.Empty(t, recorder.events)
		})
	}
}

func TestClientEventHandlerRequiresValidKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, token := range map[string]string{"missing": "", "unknown": "rk_unknown", "wrong prefix": "sk_nope"} {
		t.Run(name, func(t *testing.T) {
			recorder := &lifecycleRecorder{}
			engine := clientEventEngine(t, recorder, time.Now())
			response := postClientEvent(engine, token, `{"action":"uninstall","harness":"pi"}`)
			require.Equal(t, http.StatusUnauthorized, response.Code)
			require.Empty(t, recorder.events)
		})
	}
}

func TestClientEventHandlerWithoutObserver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := clientEventEngine(t, nil, time.Now())
	response := postClientEvent(engine, clientEventTestToken, `{"action":"on","harness":"opencode"}`)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
}
