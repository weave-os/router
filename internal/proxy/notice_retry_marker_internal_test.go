package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

const (
	markerOpus   = "claude-opus-5"
	markerSonnet = "claude-sonnet-5"
	markerTerra  = "gpt-6.1-terra"
	markerSol    = "gpt-6.1-sol"
	markerMimo   = "xiaomi/mimo-v2.6-pro"
)

func TestRequestNoticeStream_FailedFallbackCandidatesShowNoMarker(t *testing.T) {
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	var stream requestNoticeStream
	models := []string{markerOpus, markerSonnet, markerTerra, markerMimo}
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
	assert.Contains(t, body, markerOpus)
	assert.NotContains(t, body, markerSonnet)
	assert.NotContains(t, body, markerTerra)
	assert.Contains(t, body, markerMimo)
	assert.True(t, buf.Committed())
}

func TestSiblingMarkerReason_CitesThePickedModelsOwnFailure(t *testing.T) {
	trace := traceOf(
		cortexAttempt(markerSol, ErrSubscriptionPoolThrottled, TurnErrorRateLimited),
		cortexAttempt(markerOpus, ErrSubscriptionPoolExhausted, TurnErrorRateLimited),
	)
	ctx := context.WithValue(context.Background(), attemptTraceContextKey{}, trace)
	assert.Equal(t, "switched after "+markerSol+" had every subscription seat rate-limited", siblingMarkerReason(ctx, markerSol))
	assert.Equal(t, "switched after "+catalog.ModelGPT6Luna+" was unavailable", siblingMarkerReason(ctx, catalog.ModelGPT6Luna),
		"a model with no traced failure must not be blamed for overload")
}

const reconnectNudge = "reconnect your Codex seat\n\n"

func markerLines(body string) int {
	markers := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "Weave Router") && (strings.Contains(line, "response.output_text.delta") || strings.Contains(line, "chat.completion.chunk")) {
			markers++
		}
	}
	return markers
}

func modelNotice(model string) translate.RouterNotice {
	return translate.NewRouterNotice(translate.NoticeModelSelection, "✦ **Weave Router** → "+model+"\n\n", model, "")
}

const responsesOutputItem = "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"message\",\"id\":\"msg_1\",\"role\":\"assistant\",\"content\":[]}}\n\n"

func TestRequestNoticeStream_InterleavedNudgeKeepsLaterCandidatesDeferred(t *testing.T) {
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	var stream requestNoticeStream
	prepare := func(notice translate.RouterNotice) http.ResponseWriter {
		w, err := stream.prepare(context.Background(), buf, buf, translate.FormatOpenAIResponses, true, notice)
		require.NoError(t, err)
		return w
	}
	prepare(modelNotice(markerOpus))
	buf.Discard()
	prepare(modelNotice(markerSonnet))
	prepare(translate.NewRouterNotice(translate.NoticeSubscriptionReconnect, reconnectNudge, markerSonnet, ""))
	buf.Discard()
	prepare(modelNotice(markerTerra))
	buf.Discard()
	w := prepare(modelNotice(markerMimo))
	buf.Seal()
	_, err := w.Write([]byte(responsesOutputItem))
	require.NoError(t, err)

	body := rec.Body.String()
	assert.NotContains(t, body, markerTerra, "a notice between candidates must not re-arm eager markers")
	assert.Contains(t, body, markerMimo)
	assert.Equal(t, 2, markerLines(body), body)
}

func TestRequestNoticeStream_NudgeWithinAttemptKeepsServingMarker(t *testing.T) {
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	var stream requestNoticeStream
	_, err := stream.prepare(context.Background(), buf, buf, translate.FormatOpenAIResponses, true, modelNotice(markerOpus))
	require.NoError(t, err)
	buf.Discard()
	w, err := stream.prepare(context.Background(), buf, buf, translate.FormatOpenAIResponses, true, modelNotice(markerMimo))
	require.NoError(t, err)
	require.NoError(t, stream.deliverWithinAttempt(translate.NewRouterNotice(translate.NoticeSubscriptionReconnect, reconnectNudge, markerMimo, "")))
	buf.Seal()
	_, err = w.Write([]byte(responsesOutputItem))
	require.NoError(t, err)

	body := rec.Body.String()
	assert.Contains(t, body, "reconnect your Codex seat")
	assert.Contains(t, body, markerMimo, "the nudge must not drop the serving model's marker")
}

func TestRequestNoticeStream_CommittedChatFallbackNamesServingModel(t *testing.T) {
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	var stream requestNoticeStream
	_, err := stream.prepare(context.Background(), buf, buf, translate.FormatOpenAI, true, modelNotice(markerOpus))
	require.NoError(t, err)
	buf.Discard()
	w, err := stream.prepare(context.Background(), buf, buf, translate.FormatOpenAI, true, modelNotice(markerMimo))
	require.NoError(t, err)
	buf.Seal()
	_, err = w.Write([]byte("data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
	require.NoError(t, err)
	require.True(t, buf.Committed())

	assert.Contains(t, rec.Body.String(), markerMimo, "a committed fallback names the model that served even if it fails before content")
}
