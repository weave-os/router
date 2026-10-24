package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
)

const cortexGatewayURL = "https://acme-xy12345.snowflakecomputing.com/api/v2/cortex/anthropic"

func cortexAttempt(model string, err error, class TurnErrorClass) tracedAttempt {
	return tracedAttempt{
		Target:    inference.Target{Provider: providers.ProviderAnthropicGateway, CatalogID: model},
		BaseURL:   cortexGatewayURL,
		Err:       err,
		Diagnosis: inference.AttemptDiagnosis{ErrorClass: string(class), FailureOwner: string(FailureOwnerProvider)},
	}
}

func traceOf(attempts ...tracedAttempt) *attemptTrace {
	return &attemptTrace{attempts: attempts}
}

func TestProviderDisplayNameFor_NamesCustomerGateway(t *testing.T) {
	assert.Equal(t, "Snowflake Cortex", providerDisplayNameFor(providers.ProviderAnthropicGateway, cortexGatewayURL))
	assert.Equal(t, "Snowflake Cortex", providerDisplayNameFor(providers.ProviderOpenAIGateway, "https://acme.snowflakecomputing.com/api/v2/cortex/v1"))
	assert.Equal(t, "your gateway (llm.acme.internal:8443)", providerDisplayNameFor(providers.ProviderOpenAIGateway, "https://llm.acme.internal:8443/v1"))
	assert.Equal(t, "Snowflake Cortex", providerDisplayNameFor(providers.ProviderOpenAIGateway, "https://acme-xy12345.snowflakecomputing.com/api/v2/aigateways/snowflake"))
	assert.Equal(t, "your gateway", providerDisplayNameFor(providers.ProviderAnthropicGateway, ""))
	assert.Equal(t, "Anthropic", providerDisplayNameFor(providers.ProviderAnthropic, ""))
}

func cortexStatusError(status int, body string, headers http.Header) error {
	return &providers.UpstreamErrorResponse{Status: status, Headers: headers, Body: []byte(body)}
}

func TestFlushUpstreamErrorAsAnthropic_PrefixesAttributionKeepingRetrySignals(t *testing.T) {
	for _, tc := range []struct {
		name, body, wantMessage, wantType string
		status                            int
		class                             TurnErrorClass
	}{
		{"429", `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"}}`, "Snowflake Cortex returned 429 (rate limited) after 2 router retries: Rate limit exceeded", "rate_limit_error", 429, TurnErrorRateLimited},
		{"529", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "Snowflake Cortex returned 529 (overloaded) after 2 router retries: Overloaded", "overloaded_error", 529, TurnErrorRateLimited},
		{"5xx with Cortex body", `{"code":"390503","message":"Service temporarily unavailable. Request ID: 01b9","request_id":"01b9"}`, "Snowflake Cortex returned 503 (server error) after 2 router retries: Service temporarily unavailable. Request ID: 01b9", "", 503, TurnErrorUpstream5xx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{"Retry-After": {"7"}, "X-Should-Retry": {"true"}}
			upstreamErr := cortexStatusError(tc.status, tc.body, headers)
			a := cortexAttempt("claude-opus-5-5", upstreamErr, tc.class)
			err := attributeTerminalError(traceOf(a, a, a), upstreamErr)

			rec := httptest.NewRecorder()
			flushUpstreamErrorAsAnthropic(rec, err)

			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, "7", rec.Header().Get("Retry-After"))
			assert.Equal(t, "true", rec.Header().Get("X-Should-Retry"))
			msgPath := "error.message"
			if tc.wantType == "" {
				msgPath = "message"
				assert.Equal(t, "390503", gjson.Get(rec.Body.String(), "code").String(), "the rest of the body is kept")
			} else {
				assert.Equal(t, tc.wantType, gjson.Get(rec.Body.String(), "error.type").String())
			}
			assert.Equal(t, tc.wantMessage, gjson.Get(rec.Body.String(), msgPath).String())
		})
	}
}

func TestFlushBufferedIfPresent_PrefixesOpenAIErrorMessage(t *testing.T) {
	body := `{"error":{"message":"Too many requests","type":"rate_limit_error","code":"rate_limit_exceeded"}}`
	upstreamErr := cortexStatusError(429, body, http.Header{"Retry-After": {"3"}})
	a := cortexAttempt("gpt-6-luna", upstreamErr, TurnErrorRateLimited)
	a.Target.Provider = providers.ProviderOpenAIGateway
	err := attributeTerminalError(traceOf(a), upstreamErr)

	rec := httptest.NewRecorder()
	flushBufferedIfPresent(rec, err)

	assert.Equal(t, 429, rec.Code)
	assert.Equal(t, "3", rec.Header().Get("Retry-After"))
	assert.Equal(t, "rate_limit_exceeded", gjson.Get(rec.Body.String(), "error.code").String())
	assert.Equal(t, "Snowflake Cortex returned 429 (rate limited): Too many requests", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestAttributeTerminalError_AllAttemptsTimedOutNamesProvider(t *testing.T) {
	timeoutErr := errors.New("upstream call: http2: timeout awaiting response headers")
	a := cortexAttempt("claude-opus-5-5", timeoutErr, TurnErrorTimeout)
	err := attributeTerminalError(traceOf(a, a, a), timeoutErr)

	assert.Equal(t, "Snowflake Cortex timed out after 2 router retries (provider issue).", TerminalErrorMessage(err))
}

func TestSSEErrorEvents_GenericFailureNamesProvider(t *testing.T) {
	stall := errors.New("upstream stream stalled")
	a := cortexAttempt("claude-opus-5-5", stall, TurnErrorStreamStalled)
	err := attributeAttempt(a, 0, stall)

	anthropic := httptest.NewRecorder()
	_ = emitAnthropicSSEErrorEvent(anthropic, err)
	assert.Contains(t, anthropic.Body.String(), `"type":"api_error","message":"Snowflake Cortex stalled mid-stream."`)

	openAI := httptest.NewRecorder()
	_ = emitOpenAISSEErrorEvent(openAI, err)
	assert.Contains(t, openAI.Body.String(), `"message":"Snowflake Cortex stalled mid-stream.","type":"server_error"`)

	plain := httptest.NewRecorder()
	_ = emitAnthropicSSEErrorEvent(plain, stall)
	assert.Contains(t, plain.Body.String(), `"message":"upstream stream failed"`, "an unattributed failure keeps the generic text")
}

func TestSiblingMarkerReason_NamesRealFailureClass(t *testing.T) {
	for _, tc := range []struct {
		name  string
		a     tracedAttempt
		cause string
	}{
		{"timeout", cortexAttempt("gpt-6-luna", errors.New("timeout awaiting response headers"), TurnErrorTimeout), "timed out"},
		{"429", cortexAttempt("gpt-6-luna", cortexStatusError(429, `{}`, nil), TurnErrorRateLimited), "was rate-limited"},
		{"529", cortexAttempt("gpt-6-luna", cortexStatusError(529, `{}`, nil), TurnErrorRateLimited), "was overloaded"},
		{"5xx", cortexAttempt("gpt-6-luna", cortexStatusError(502, `{}`, nil), TurnErrorUpstream5xx), "returned a 502"},
		{"stalled", cortexAttempt("gpt-6-luna", errors.New("stalled"), TurnErrorStreamStalled), "stalled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), attemptTraceContextKey{}, traceOf(tc.a))
			assert.Equal(t, "switched after gpt-6-luna "+tc.cause, siblingMarkerReason(ctx, "gpt-6-luna"))
			assert.Equal(t, "↳ switched: Snowflake Cortex "+tc.cause+" (provider issue)\n\n", lastFailureDetail(traceOf(tc.a)))
		})
	}
}

func slowStartTrace(attemptAt, firstAt time.Time, attempts ...tracedAttempt) *attemptTrace {
	return &attemptTrace{
		current:        inference.Target{Provider: providers.ProviderAnthropicGateway, CatalogID: "claude-opus-5-5"},
		baseURL:        cortexGatewayURL,
		attemptAt:      attemptAt,
		firstAttemptAt: firstAt,
		attempts:       attempts,
	}
}

func TestSlowStartNotice_ThresholdBaselineAndCooldown(t *testing.T) {
	tracker := newSlowStartTracker()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()

	_, shown := slowStartNotice(ctx, slowStartTrace(now.Add(-9*time.Second), now.Add(-9*time.Second)), tracker, now)
	assert.False(t, shown, "under the 10s floor")

	for i := range slowStartMinSamples {
		at := now.Add(time.Duration(i) * time.Second)
		_, _ = slowStartNotice(ctx, slowStartTrace(at.Add(-3*time.Second), at.Add(-3*time.Second)), tracker, at)
	}
	now = now.Add(time.Minute)
	notice, shown := slowStartNotice(ctx, slowStartTrace(now.Add(-41*time.Second), now.Add(-41*time.Second)), tracker, now)
	require.True(t, shown)
	assert.Equal(t, "↳ Snowflake Cortex took 41s to start (usually ~3s)\n\n", notice.Text)

	now = now.Add(time.Minute)
	_, shown = slowStartNotice(ctx, slowStartTrace(now.Add(-41*time.Second), now.Add(-41*time.Second)), tracker, now)
	assert.False(t, shown, "once per session cooldown")

	now = now.Add(slowStartCooldown)
	failed := cortexAttempt("gpt-6-luna", errors.New("timeout awaiting response headers"), TurnErrorTimeout)
	failed.Target.Provider = providers.ProviderOpenAIGateway
	notice, shown = slowStartNotice(ctx, slowStartTrace(now.Add(-40*time.Second), now.Add(-84*time.Second), failed), tracker, now)
	require.True(t, shown)
	assert.Equal(t, "↳ Snowflake Cortex took 84s to start (usually ~3s) after gpt-6-luna on Snowflake Cortex timed out\n\n", notice.Text)
}

func TestSlowStartNotice_BaselineRaisesThreshold(t *testing.T) {
	tracker := newSlowStartTracker()
	now := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	for i := range slowStartMinSamples {
		at := now.Add(time.Duration(i) * time.Second)
		_, _ = slowStartNotice(context.Background(), slowStartTrace(at.Add(-6*time.Second), at.Add(-6*time.Second)), tracker, at)
	}
	now = now.Add(time.Minute)
	_, shown := slowStartNotice(context.Background(), slowStartTrace(now.Add(-15*time.Second), now.Add(-15*time.Second)), tracker, now)
	assert.False(t, shown, "15s is under 3x a 6s median")
	assert.True(t, strings.HasPrefix(slowStartKey(tracedAttempt{Target: inference.Target{Provider: "p", CatalogID: "m"}, BaseURL: cortexGatewayURL}), "p|acme-xy12345.snowflakecomputing.com|m"))
}

func TestTerminalErrorMessage_KeepsUpstreamMessageForResponses(t *testing.T) {
	body := `{"error":{"message":"Rate limit exceeded for account. Please retry later.","type":"rate_limit_error","code":"rate_limit_exceeded"}}`
	upstreamErr := cortexStatusError(429, body, http.Header{"Retry-After": {"2"}})
	a := cortexAttempt("claude-opus-5", upstreamErr, TurnErrorRateLimited)
	a.Target.Provider = providers.ProviderOpenAIGateway
	err := attributeTerminalError(traceOf(a, a, a), upstreamErr)

	assert.Equal(t, "Snowflake Cortex returned 429 (rate limited) after 2 router retries: Rate limit exceeded for account. Please retry later.", TerminalErrorMessage(err))
}
