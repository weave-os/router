package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

type completedEmptyTurnClient struct{ calls int }

func (c *completedEmptyTurnClient) Proxy(_ context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	c.calls++
	w.Header().Set("Content-Type", "text/event-stream")
	_, err := io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic\",\"status\":\"completed\",\"error\":null,\"incomplete_details\":null,\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[]}],\"usage\":{\"input_tokens\":100,\"output_tokens\":4}}}\n\n")
	return err
}
func (*completedEmptyTurnClient) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func TestProxyMessages_CompletedEmptyTurnReachesClientWithoutRetry(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			c := &completedEmptyTurnClient{}
			svc := NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.6-luna", Reason: "synthetic"}}, map[string]providers.Client{providers.ProviderOpenAI: c}, nil, false, nil, nil, false, providers.ProviderOpenAI, "gpt-5.6-luna", nil)
			svc.retrySleep = noopSleep
			body := fmt.Sprintf(`{"model":"auto","max_tokens":1024,"stream":%t,"messages":[{"role":"user","content":"Synthetic notification"}]}`, streaming)
			rec := httptest.NewRecorder()
			require.NoError(t, svc.ProxyMessages(context.Background(), []byte(body), rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))))
			require.Equal(t, 1, c.calls)
			require.Equal(t, 200, rec.Code)
			if streaming {
				require.Contains(t, rec.Body.String(), "event: message_stop")
				require.Contains(t, rec.Body.String(), `"stop_reason":"end_turn"`)
				require.NotContains(t, rec.Body.String(), "event: error")
			} else {
				require.Equal(t, "[]", gjson.GetBytes(rec.Body.Bytes(), "content").Raw)
				require.Equal(t, "end_turn", gjson.GetBytes(rec.Body.Bytes(), "stop_reason").String())
			}
		})
	}
}
