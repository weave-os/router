package ingress

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteRetiredBetaRequestKeepsProtocolReplyAvailableBeforeAdmission(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1,"messages":[{"role":"user","content":"/beta"}]}`)
	for _, path := range []string{"/v1/messages", "/v1/route/handoff"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)
			response := httptest.NewRecorder()
			retired, err := WriteRetiredBetaRequest(response, request, body)
			require.NoError(t, err)
			require.True(t, retired)
			require.Equal(t, http.StatusOK, response.Code)
			require.Contains(t, response.Body.String(), "Beta has been retired")
		})
	}
}
