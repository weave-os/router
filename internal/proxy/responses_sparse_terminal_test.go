package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sparseResponsesClient struct {
	calls     int
	interrupt bool
}

func (c *sparseResponsesClient) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.calls++
	w.Header().Set("Content-Type", "text/event-stream")
	for _, payload := range []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_native","status":"in_progress","output":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":1,"output_index":0,"delta":"streamed answer"}`,
	} {
		if _, err := io.WriteString(w, "data: "+payload+"\n\n"); err != nil {
			return err
		}
	}
	if c.interrupt {
		return &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":{"message":"unavailable"}}`)}
	}
	_, err := io.WriteString(w, "data: "+`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_native","status":"completed","output":[]}}`+"\n\n")
	return err
}

func (c *sparseResponsesClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func TestProxyOpenAIResponses_NativeStreamCompletionDoesNotReplayOutput(t *testing.T) {
	for _, clientApp := range []string{"", ClientAppCodex} {
		for _, interrupt := range []bool{false, true} {
			t.Run(fmt.Sprintf("client=%s/interrupted=%t", clientApp, interrupt), func(t *testing.T) {
				client := &sparseResponsesClient{interrupt: interrupt}
				svc := NewService(
					staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "test"}},
					map[string]providers.Client{providers.ProviderOpenAI: client},
					nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-sol", nil,
				)
				svc.retrySleep = noopSleep
				ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: clientApp})
				body := `{"model":"gpt-5.6-luna","stream":true,"input":"hi"}`
				rec := httptest.NewRecorder()
				err := svc.ProxyOpenAIResponses(ctx, []byte(body), rec,
					httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))
				if interrupt {
					require.Error(t, err)
					assert.Contains(t, rec.Body.String(), `"type":"response.failed"`)
					assert.NotContains(t, rec.Body.String(), `"type":"response.completed"`)
				} else {
					require.NoError(t, err)
					assert.Contains(t, rec.Body.String(), `"type":"response.completed"`)
					assert.NotContains(t, rec.Body.String(), `"type":"response.failed"`)
				}
				assert.Equal(t, 1, client.calls, "never retry after provider output commits")
				assert.Equal(t, 1, strings.Count(rec.Body.String(), "streamed answer"))
			})
		}
	}
}
