package classifier_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/api/classifier"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/proxy"
)

func TestStartThreadHandlerLogsRejectedBodiesWithoutEchoingFieldNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(observability.WithLogger(c.Request.Context(), logger))
	})
	engine.POST("/v1/router/classifier/threads", classifier.StartThreadHandler(&proxy.Service{}))

	response := httptest.NewRecorder()
	body := strings.NewReader(`{"synthetic_private_field_name":"x"}`)
	engine.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/router/classifier/threads", body))

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Contains(t, logs.String(), "Invalid classifier handshake UUID or JSON")
	require.NotContains(t, logs.String(), "synthetic_private_field_name")
}
