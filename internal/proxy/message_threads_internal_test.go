package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/router"
)

func TestProxyMessages_ThreadContinueBouncedBeforeClassifierHistory(t *testing.T) {
	svc := &Service{now: time.Now}
	ctx := router.WithStrategy(context.Background(), router.StrategyLLMClassifier)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}],"thread":{"type":"continue","previous_message_id":"msg_1"}}`)

	require.NoError(t, svc.ProxyMessages(ctx, body, rec, req))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "thread_unsupported_request", gjson.Get(rec.Body.String(), "error.details.error_code").String(), "a delta-only continue must not reach classifier history validation")
}
