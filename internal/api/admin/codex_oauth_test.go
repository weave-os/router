package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"workweave/router/internal/providers"
)

type codexOAuthLoginFake struct {
	status   providers.CodexOAuthStatus
	started  providers.CodexOAuthStart
	startErr error
	canceled bool
}

func (f *codexOAuthLoginFake) Start(context.Context) (providers.CodexOAuthStart, error) {
	return f.started, f.startErr
}

func (f *codexOAuthLoginFake) Status(context.Context) providers.CodexOAuthStatus {
	return f.status
}

func (f *codexOAuthLoginFake) Cancel(context.Context) error {
	f.canceled = true
	return nil
}

func TestCodexOAuthHandlersExposeOnlyBrowserState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &codexOAuthLoginFake{
		status:  providers.CodexOAuthStatus{State: "authenticated"},
		started: providers.CodexOAuthStart{LoginID: "login-1", AuthURL: "https://auth.example.test/login"},
	}
	engine := gin.New()
	engine.GET("/status", CodexOAuthStatusHandler(fake))
	engine.POST("/start", CodexOAuthStartHandler(fake))
	engine.POST("/cancel", CodexOAuthCancelHandler(fake))

	status := httptest.NewRecorder()
	engine.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/status", nil))
	require.Equal(t, http.StatusOK, status.Code)
	require.JSONEq(t, `{"state":"authenticated"}`, status.Body.String())

	start := httptest.NewRecorder()
	engine.ServeHTTP(start, httptest.NewRequest(http.MethodPost, "/start", nil))
	require.Equal(t, http.StatusOK, start.Code)
	require.JSONEq(t, `{"state":"pending","login_id":"login-1","auth_url":"https://auth.example.test/login"}`, start.Body.String())

	cancel := httptest.NewRecorder()
	engine.ServeHTTP(cancel, httptest.NewRequest(http.MethodPost, "/cancel", nil))
	require.Equal(t, http.StatusNoContent, cancel.Code)
	require.True(t, fake.canceled)
}
