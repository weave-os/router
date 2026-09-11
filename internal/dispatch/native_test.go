package dispatch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

// nativeUpstream records the exact prepared request a Native transport hands
// to Proxy and writes a canned response so preserved output can be asserted.
type nativeUpstream struct {
	calls    int
	prep     providers.PreparedRequest
	decision router.Decision
	response string
	err      error
}

func (u *nativeUpstream) Proxy(_ context.Context, decision router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	u.calls++
	u.prep = prep
	u.decision = decision
	if u.err != nil {
		return u.err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(u.response))
	return nil
}

func (u *nativeUpstream) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func nativePlan(target inference.Target) fakePlan {
	return fakePlan{selected: target, budget: inference.BudgetSpec{Source: inference.BudgetSourcePolicy, MaxAttempts: 1}}
}

func runNative(t *testing.T, upstream *nativeUpstream, target inference.Target, native dispatch.Native) (dispatch.Result, error) {
	t.Helper()
	rec := &recorder{}
	exec := newExecutor(t, map[string]providers.Client{target.Provider: upstream}, rec)
	return exec.Run(context.Background(), inference.InvocationRequest{Purpose: inference.PurposeOriginalModelFallback, RequestID: "req-native"}, nativePlan(target), native.Transport())
}

func TestNative_PreservesOriginalBodyAndWritesOutput(t *testing.T) {
	upstream := &nativeUpstream{response: `{"id":"msg_native","model":"kimi-k2.5"}`}
	body := `{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"u"},"custom_native":true}`
	headers := make(http.Header)
	headers.Set("anthropic-version", "2024-10-22")
	writer := httptest.NewRecorder()
	inbound := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(""))

	result, err := runNative(t, upstream, primary, dispatch.Native{
		Prepared: providers.PreparedRequest{Body: []byte(body), Headers: headers},
		Request:  inbound,
		Writer:   writer,
		Model:    "kimi-k2.5",
	})
	require.NoError(t, err)

	assert.Equal(t, 1, upstream.calls)
	assert.True(t, upstream.prep.PreserveNative, "native transport must ask the adapter to preserve the original request")
	assert.JSONEq(t, body, string(upstream.prep.Body), "body must reach the adapter byte-for-byte")
	assert.Equal(t, "2024-10-22", upstream.prep.Headers.Get("anthropic-version"))
	assert.Equal(t, router.Decision{Provider: primary.Provider, Model: primary.CatalogID, Reason: dispatch.NativeReason}, upstream.decision)
	assert.Equal(t, http.StatusOK, writer.Code)
	assert.JSONEq(t, `{"id":"msg_native","model":"kimi-k2.5"}`, writer.Body.String(), "provider output must reach the caller's writer unchanged")
	assert.Equal(t, primary, result.Outcome.ServedTarget)
	assert.Equal(t, 1, result.Outcome.AttemptCount)
	assert.False(t, result.Outcome.FallbackUsed)
}

func TestNative_AcceptsUpstreamIDSpelling(t *testing.T) {
	upstream := &nativeUpstream{response: `{}`}
	body := `{"model":"accounts/fireworks/models/kimi-k2p5"}`
	_, err := runNative(t, upstream, primary, dispatch.Native{
		Prepared: providers.PreparedRequest{Body: []byte(body), Headers: make(http.Header)},
		Request:  httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("")),
		Writer:   httptest.NewRecorder(),
		Model:    "accounts/fireworks/models/kimi-k2p5",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, upstream.calls)
	assert.Equal(t, body, string(upstream.prep.Body), "original upstream spelling must be preserved, not normalized")
}

func TestNative_RejectsModelMismatchBeforeProviderIO(t *testing.T) {
	cases := map[string]dispatch.Native{
		"declared model differs": {
			Prepared: providers.PreparedRequest{Body: []byte(`{"model":"kimi-k2.5"}`)},
			Request:  httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
			Model:    "gpt-5.6-luna",
		},
		"body model differs": {
			Prepared: providers.PreparedRequest{Body: []byte(`{"model":"gpt-5.6-luna"}`)},
			Request:  httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
			Model:    "kimi-k2.5",
		},
		"gemini path model differs": {
			Prepared: providers.PreparedRequest{Body: []byte(`{"contents":[]}`)},
			Request:  httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-x:streamGenerateContent?alt=sse", nil),
			Model:    "kimi-k2.5",
		},
		"empty declared model": {
			Prepared: providers.PreparedRequest{Body: []byte(`{"model":"kimi-k2.5"}`)},
			Request:  httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
			Model:    "",
		},
	}
	for name, native := range cases {
		t.Run(name, func(t *testing.T) {
			upstream := &nativeUpstream{response: `{}`}
			writer := httptest.NewRecorder()
			native.Writer = writer
			result, err := runNative(t, upstream, primary, native)
			require.True(t, errors.Is(err, dispatch.ErrTargetMismatch), "got %v", err)
			assert.Zero(t, upstream.calls, "provider must not be called")
			assert.Zero(t, writer.Body.Len(), "nothing may reach the caller")
			assert.Equal(t, dispatch.FailureReasonTargetMismatch, result.Summary.FallbackReason)
		})
	}
}

func TestNative_GeminiPathModelMatchesTarget(t *testing.T) {
	gemini := inference.Target{CatalogID: "gemini-3.1-pro", Provider: providers.ProviderGoogle, UpstreamID: "gemini-3.1-pro"}
	upstream := &nativeUpstream{response: `{"candidates":[]}`}
	writer := httptest.NewRecorder()
	_, err := runNative(t, upstream, gemini, dispatch.Native{
		Prepared: providers.PreparedRequest{Body: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`), Headers: make(http.Header)},
		Request:  httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.1-pro:streamGenerateContent?alt=sse&key=redacted", nil),
		Writer:   writer,
		Model:    "gemini-3.1-pro",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, upstream.calls)
	assert.Equal(t, `{"candidates":[]}`, writer.Body.String())
}

func TestNative_UpstreamFailureIsNotRetried(t *testing.T) {
	upstream := &nativeUpstream{err: &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable, Body: []byte(`{"error":"down"}`)}}
	writer := httptest.NewRecorder()
	result, err := runNative(t, upstream, primary, dispatch.Native{
		Prepared: providers.PreparedRequest{Body: []byte(`{"model":"kimi-k2.5"}`), Headers: make(http.Header)},
		Request:  httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
		Writer:   writer,
		Model:    "kimi-k2.5",
	})
	require.Error(t, err)
	assert.Equal(t, 1, upstream.calls, "a one-attempt plan must issue exactly one upstream request")
	assert.Equal(t, 1, result.Outcome.AttemptCount)
	assert.Equal(t, dispatch.FailureReasonUpstreamStatus, result.Summary.FallbackReason)
	assert.Zero(t, writer.Body.Len(), "a buffered upstream error must not be replayed to the caller by the transport")
}

func TestNative_RequiresRequestAndWriter(t *testing.T) {
	upstream := &nativeUpstream{response: `{}`}
	_, err := runNative(t, upstream, primary, dispatch.Native{
		Prepared: providers.PreparedRequest{Body: []byte(`{"model":"kimi-k2.5"}`)},
		Model:    "kimi-k2.5",
	})
	require.Error(t, err)
	assert.Zero(t, upstream.calls)
}
