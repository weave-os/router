package openai_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/api/openai"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func TestImagesHandler_StatusByForwardOutcome(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name       string
		forwardErr error
		wantStatus int
	}{
		{"served", nil, http.StatusOK},
		{"missing own credential", proxy.ErrCodexImagesRequireOwnCredential, http.StatusForbidden},
		{"gateway tenancy", proxy.ErrCodexImagesUnavailableWithGateway, http.StatusForbidden},
		{"transport failure", errors.New("dial upstream"), http.StatusBadGateway},
		{"included quota exhausted", &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Body: []byte(`{"error":{"type":"usage_limit_reached","message":"Codex included quota is exhausted."}}`)}, http.StatusTooManyRequests},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotOperation, gotBody string
			engine := gin.New()
			engine.POST("/v1/images/edits", openai.ImagesHandler(proxy.CodexImageEdits, func(_ context.Context, _ http.Header, operation string, body []byte, w http.ResponseWriter) error {
				gotOperation, gotBody = operation, string(body)
				if tc.forwardErr == nil {
					w.WriteHeader(http.StatusOK)
				}
				return tc.forwardErr
			}))
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/images/edits", strings.NewReader(`{"prompt":"p"}`)))

			assert.Equal(t, tc.wantStatus, rec.Code)
			assert.Equal(t, proxy.CodexImageEdits, gotOperation)
			assert.JSONEq(t, `{"prompt":"p"}`, gotBody)
			if tc.forwardErr != nil {
				assert.NotEmpty(t, gjson.Get(rec.Body.String(), "error.message").String())
				assert.NotContains(t, rec.Body.String(), "dial upstream")
			}
		})
	}
}
