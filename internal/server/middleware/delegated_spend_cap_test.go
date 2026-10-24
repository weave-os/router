package middleware_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubDelegatedSpend struct {
	sessionSpent, taskSpent int64
	err                     error
	reads                   int
}

func (s *stubDelegatedSpend) GetDelegatedTokenSpend(_ context.Context, _, _, _ string) (int64, int64, error) {
	s.reads++
	return s.sessionSpent, s.taskSpent, s.err
}

func runDelegatedSpendCap(t *testing.T, reader *stubDelegatedSpend, session *auth.DelegatedSession) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := billing.NewService(&stubBillingRepo{}).WithDelegatedSpend(reader)
	reached := false
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithDelegatedSession(c.Request.Context(), session))
		c.Next()
	})
	engine.Use(middleware.WithDelegatedSpendCap(svc))
	engine.POST("/v1/messages", func(c *gin.Context) {
		reached = true
		c.Status(http.StatusOK)
	})
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	return rr, reached
}

func TestWithDelegatedSpendCap(t *testing.T) {
	session := func(sessionCap, taskCap *int64) *auth.DelegatedSession {
		return &auth.DelegatedSession{IssuerKeyID: "issuer", SessionID: "s1", TaskID: "t1", SessionCapMicros: sessionCap, TaskCapMicros: taskCap}
	}

	t.Run("no session or no caps passes without a read", func(t *testing.T) {
		reader := &stubDelegatedSpend{}
		_, reached := runDelegatedSpendCap(t, reader, nil)
		assert.True(t, reached)
		_, reached = runDelegatedSpendCap(t, reader, session(nil, nil))
		assert.True(t, reached)
		assert.Zero(t, reader.reads)
	})

	t.Run("under both caps passes", func(t *testing.T) {
		_, reached := runDelegatedSpendCap(t, &stubDelegatedSpend{sessionSpent: 10, taskSpent: 20}, session(capPtr(100), capPtr(100)))
		assert.True(t, reached)
	})

	for name, tc := range map[string]struct {
		reader *stubDelegatedSpend
		sess   *auth.DelegatedSession
		scope  string
	}{
		"session cap reached":                       {&stubDelegatedSpend{sessionSpent: 100}, session(capPtr(100), nil), "session"},
		"task cap reached by sibling sessions":      {&stubDelegatedSpend{sessionSpent: 5, taskSpent: 500}, session(capPtr(100), capPtr(500)), "task"},
		"task reported first when both are reached": {&stubDelegatedSpend{sessionSpent: 100, taskSpent: 500}, session(capPtr(100), capPtr(500)), "task"},
	} {
		t.Run(name, func(t *testing.T) {
			rr, reached := runDelegatedSpendCap(t, tc.reader, tc.sess)
			assert.False(t, reached)
			require.Equal(t, http.StatusPaymentRequired, rr.Code)
			var body map[string]any
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
			assert.Equal(t, "key_spend_cap_reached", body["error"])
			assert.Equal(t, tc.scope, body["scope"])
		})
	}

	t.Run("read failure fails closed", func(t *testing.T) {
		rr, reached := runDelegatedSpendCap(t, &stubDelegatedSpend{err: errors.New("db down")}, session(capPtr(100), nil))
		assert.False(t, reached)
		assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
	})
}
