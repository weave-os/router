package proxy

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/translate"
)

func TestRequestNoticeStream_FailedFallbackCandidatesShowNoMarker(t *testing.T) {
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	var stream requestNoticeStream
	models := []string{"claude-opus-5", "claude-sonnet-5", "gpt-6.1-terra", "xiaomi/mimo-v2.6-pro"}
	for i, model := range models {
		notice := translate.NewRouterNotice(translate.NoticeModelSelection, "✦ **Weave Router** → "+model+"\n\n", model, "")
		w, err := stream.prepare(context.Background(), buf, buf, translate.FormatOpenAIResponses, true, notice)
		require.NoError(t, err)
		if i < len(models)-1 {
			buf.Discard()
			continue
		}
		buf.Seal()
		_, err = w.Write([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n"))
		require.NoError(t, err)
	}
	body := rec.Body.String()
	markers := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "response.output_text.delta") && strings.Contains(line, "Weave Router") {
			markers++
		}
	}
	assert.Equal(t, 2, markers, "only the first pick and the model that served may show a marker:\n%s", body)
	assert.Contains(t, body, "claude-opus-5")
	assert.NotContains(t, body, "claude-sonnet-5")
	assert.NotContains(t, body, "gpt-6.1-terra")
	assert.Contains(t, body, "xiaomi/mimo-v2.6-pro")
	assert.True(t, buf.Committed())
}

func TestSiblingMarkerReason_CitesThePickedModelsOwnFailure(t *testing.T) {
	trace := traceOf(
		cortexAttempt("gpt-6.1-sol", ErrSubscriptionPoolThrottled, TurnErrorRateLimited),
		cortexAttempt("claude-opus-5", ErrSubscriptionPoolExhausted, TurnErrorRateLimited),
	)
	ctx := context.WithValue(context.Background(), attemptTraceContextKey{}, trace)
	assert.Equal(t, "switched after gpt-6.1-sol had every subscription seat rate-limited", siblingMarkerReason(ctx, "gpt-6.1-sol"))
	assert.Equal(t, "switched after gpt-6-luna was unavailable", siblingMarkerReason(ctx, "gpt-6-luna"),
		"a model with no traced failure must not be blamed for overload")
}
