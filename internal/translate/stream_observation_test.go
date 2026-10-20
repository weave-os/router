package translate_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderStreamObservationGeminiRequiresEveryCandidateTerminal(t *testing.T) {
	o := translate.NewProviderStreamObservation(translate.FormatGemini)
	o.ObserveEvent([]byte(`data: {"candidates":[{"index":0,"content":{"parts":[{"text":"first"}]}},{"index":1,"content":{"parts":[{"text":"second"}]}}]}`))
	o.ObserveEvent([]byte(`data: {"candidates":[{"index":0,"finishReason":"STOP"}]}`))
	require.ErrorIs(t, o.OutcomeError(), translate.ErrStreamIncomplete)
	assert.True(t, o.Facts().UsableOutput)
	o.ObserveEvent([]byte(`data: {"candidates":[{"index":1,"finishReason":"MAX_TOKENS"}]}`))
	require.NoError(t, o.OutcomeError())
	assert.Equal(t, translate.StreamTerminal, o.Facts().State)
}

func TestProviderStreamObservationKeepsDeclaredFailureDistinctFromCompletion(t *testing.T) {
	for _, fixture := range []struct {
		format translate.Format
		frame  string
		status int
	}{
		{translate.FormatAnthropic, `event: error` + "\n" + `data: {"type":"error","error":{"type":"rate_limit_error","message":"limited"}}`, http.StatusTooManyRequests},
		{translate.FormatOpenAI, `data: {"error":{"code":"context_length_exceeded","message":"too long"}}`, http.StatusBadRequest},
		{translate.FormatGemini, `data: {"error":{"code":503,"status":"UNAVAILABLE","message":"unavailable"}}`, http.StatusServiceUnavailable},
		{translate.FormatOpenAIResponses, `data: {"type":"response.failed","response":{"status":"failed","error":{"code":"context_length_exceeded","message":"too long"}}}`, http.StatusBadRequest},
	} {
		o := translate.NewProviderStreamObservation(fixture.format)
		o.ObserveEvent([]byte(fixture.frame))
		require.NoError(t, o.EOF(), "the provider stated a terminal outcome")
		var failure *providers.UpstreamErrorResponse
		require.ErrorAs(t, o.OutcomeError(), &failure)
		assert.Equal(t, fixture.status, failure.Status)
		assert.Same(t, failure, o.DeclaredError())
		assert.Equal(t, translate.StreamFailed, o.Facts().State)
	}
}

func TestProviderStreamObservationRejectsTruncatedTerminalJSON(t *testing.T) {
	for _, fixture := range []struct {
		format translate.Format
		start  string
		tail   string
	}{
		{translate.FormatAnthropic, "event: message_start\ndata: {}", "event: message_stop\ndata: {\"type\":\"message_stop\""},
		{translate.FormatOpenAI, `data: {"choices":[{"index":0,"delta":{"content":"partial"}}]}`, `data: {"choices":[{"index":0,"finish_reason":"stop"`},
		{translate.FormatGemini, `data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`, `data: {"candidates":[{"finishReason":"STOP"`},
		{translate.FormatOpenAIResponses, `data: {"type":"response.created"}`, `data: {"type":"response.completed","response":{"status":"completed"`},
	} {
		o := translate.NewProviderStreamObservation(fixture.format)
		o.ObserveEvent([]byte(fixture.start))
		o.ObserveEvent([]byte(fixture.tail))
		require.ErrorIs(t, o.OutcomeError(), translate.ErrStreamIncomplete)
	}
}

func TestProviderStreamObservationIgnoresKeepalivesAndPreservesCancellation(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatGemini, translate.FormatOpenAIResponses} {
		o := translate.NewProviderStreamObservation(format)
		o.ObserveEvent([]byte(": keepalive"))
		require.ErrorIs(t, o.OutcomeError(), translate.ErrStreamEmpty)
		assert.False(t, o.Facts().UsableOutput)
		o.Cancel(context.Canceled)
		require.ErrorIs(t, o.OutcomeError(), context.Canceled)
		require.NoError(t, o.EOF())
		assert.Nil(t, o.DeclaredError())
		assert.Equal(t, translate.StreamCanceled, o.Facts().State)
	}
}

func TestProviderStreamObservationReasoningIsProviderProgress(t *testing.T) {
	o := translate.NewProviderStreamObservation(translate.FormatGemini)
	o.ObserveEvent([]byte(`data: {"candidates":[{"content":{"parts":[{"thought":true,"text":"reasoning"}]}}]}`))
	assert.True(t, o.Facts().ReasoningProgress)
	assert.False(t, o.Facts().UsableOutput)
	require.ErrorIs(t, o.OutcomeError(), translate.ErrStreamIncomplete)
}

func TestProviderStreamObservationChatDoneRequiresEveryObservedChoice(t *testing.T) {
	for _, fixture := range []struct {
		name       string
		choices    string
		incomplete bool
	}{
		{"single choice sentinel", `[{"index":0,"delta":{"content":"answer"}}]`, false},
		{"multiple open choices", `[{"index":0,"delta":{"content":"first"}},{"index":1,"delta":{"content":"second"}}]`, true},
		{"partly finished choices", `[{"index":0,"finish_reason":"stop"},{"index":1,"delta":{"content":"second"}}]`, true},
		{"every choice finished", `[{"index":0,"finish_reason":"stop"},{"index":1,"finish_reason":"length"}]`, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			o := translate.NewProviderStreamObservation(translate.FormatOpenAI)
			o.ObserveEvent([]byte(`data: {"choices":` + fixture.choices + `}`))
			o.ObserveEvent([]byte("data: [DONE]"))
			if fixture.incomplete {
				require.ErrorIs(t, o.OutcomeError(), translate.ErrStreamIncomplete)
			} else {
				require.NoError(t, o.OutcomeError())
			}
		})
	}
}

func TestProviderStreamObservationGeminiBodyRequiresTerminalFinish(t *testing.T) {
	for _, fixture := range []struct {
		name       string
		body       string
		incomplete bool
	}{
		{"content without finish", `{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`, true},
		{"one unfinished candidate", `{"candidates":[{"index":0,"finishReason":"STOP"},{"index":1,"content":{"parts":[{"text":"partial"}]}}]}`, true},
		{"complete candidate", `{"candidates":[{"content":{"parts":[{"text":"answer"}]},"finishReason":"STOP"}]}`, false},
		{"all candidates finished", `{"candidates":[{"index":0,"finishReason":"STOP"},{"index":1,"finishReason":"MAX_TOKENS"}]}`, false},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			o := translate.NewProviderStreamObservation(translate.FormatGemini)
			o.ObserveBody([]byte(fixture.body))
			if fixture.incomplete {
				require.ErrorIs(t, o.OutcomeError(), translate.ErrStreamIncomplete)
			} else {
				require.NoError(t, o.OutcomeError())
			}
		})
	}
}

func TestProviderStreamObservationRejectsMalformedTailAfterChoiceFinish(t *testing.T) {
	for _, fixture := range []struct {
		format   translate.Format
		terminal string
		tail     string
	}{
		{translate.FormatOpenAI, `data: {"choices":[{"index":0,"finish_reason":"stop"}]}`, `data: {"choices":[{"index":1`},
		{translate.FormatGemini, `data: {"candidates":[{"index":0,"finishReason":"STOP"}]}`, `data: {"candidates":[{"index":1`},
	} {
		t.Run(fmt.Sprint(fixture.format), func(t *testing.T) {
			o := translate.NewProviderStreamObservation(fixture.format)
			o.ObserveEvent([]byte(fixture.terminal))
			o.ObserveEvent([]byte(fixture.tail))
			require.ErrorIs(t, o.OutcomeError(), translate.ErrStreamIncomplete)
		})
	}
}

func TestProviderStreamObservationPreservesCompleteJSONFallback(t *testing.T) {
	for _, format := range []translate.Format{translate.FormatAnthropic, translate.FormatOpenAI, translate.FormatOpenAIResponses} {
		o := translate.NewProviderStreamObservation(format)
		o.ObserveBody([]byte(`{"id":"synthetic-response","content":[]}`))
		require.NoError(t, o.OutcomeError(), "native non-Gemini JSON fallback remains accepted")
	}
}

func TestProviderStreamObservationIgnoresRecordsAfterExplicitEnd(t *testing.T) {
	for _, fixture := range []struct {
		format   translate.Format
		terminal string
	}{
		{translate.FormatAnthropic, "event: message_stop\ndata: {\"type\":\"message_stop\"}"},
		{translate.FormatOpenAI, "data: [DONE]"},
		{translate.FormatOpenAIResponses, `data: {"type":"response.completed","response":{"status":"completed"}}`},
	} {
		t.Run(fmt.Sprint(fixture.format), func(t *testing.T) {
			o := translate.NewProviderStreamObservation(fixture.format)
			o.ObserveEvent([]byte(fixture.terminal))
			o.ObserveEvent([]byte(`data: {"truncated":`))
			o.ObserveEvent([]byte(`data: {"error":{"message":"late error"}}`))
			require.NoError(t, o.OutcomeError())
			assert.Equal(t, translate.StreamTerminal, o.Facts().State)
		})
	}
}
