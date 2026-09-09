package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const geminiPassthroughBody = `{
	"contents":[{"role":"user","parts":[{"text":"hello"}]}]
}`

// Post-injection body shape: handler adds "model" and "stream" before calling.
const geminiInjectedBody = `{
	"model":"gemini-1.5-pro",
	"stream":false,
	"contents":[{"role":"user","parts":[{"text":"hello"}]}]
}`

func geminiExperimentContext(installationID string, arm auth.BlindExperimentArm) context.Context {
	ctx := authedCtx(installationID)
	ctx = context.WithValue(ctx, proxy.PolicyTrainingAllowedContextKey{}, true)
	ctx = context.WithValue(ctx, auth.UserIDContextKey{}, "router-user-1")
	return context.WithValue(ctx, auth.BlindExperimentContextKey{}, auth.BlindExperimentState{
		Active:              true,
		Arm:                 arm,
		AssignmentSource:    auth.BlindExperimentAssignmentAutomatic,
		CanonicalSubjectKey: "account-1",
	})
}

func TestProxyGeminiGenerateContent_RoutesToGoogleProvider(t *testing.T) {
	store := newFakePinStore()
	googleProv := &fakeProvider{}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: "gemini-2.5-pro", Reason: "cluster"}}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{providers.ProviderGoogle: googleProv},
		nil, false, nil,
		store,
		false, providers.ProviderGoogle, "gemini-2.5-flash",
		nil,
	)

	ctx := authedCtx("00000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-1.5-pro:generateContent", strings.NewReader(""))
	require.NoError(t, svc.ProxyGeminiGenerateContent(ctx, []byte(geminiInjectedBody), rec, httpReq))

	assert.Equal(t, "gemini-2.5-pro", rec.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, providers.ProviderGoogle, rec.Header().Get(proxy.HeaderRouterProvider))
	require.Len(t, googleProv.proxyBodies, 1, "the upstream Google client must be invoked once")
	body := string(googleProv.proxyBodies[0])
	assert.NotContains(t, body, `"model"`,
		"model is encoded in the upstream URL, not the body")
	assert.NotContains(t, body, `"stream"`,
		"streaming is signalled via GeminiStreamHintHeader")
}

func TestProxyGeminiGenerateContent_RestrictsRoutingToGeminiFamily(t *testing.T) {
	store := newFakePinStore()
	googleProv := &fakeProvider{}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: "gemini-2.5-pro", Reason: "cluster"}}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeProvider{},
			providers.ProviderGoogle:    googleProv,
		},
		nil, false, nil,
		store,
		false, providers.ProviderGoogle, "gemini-2.5-flash",
		nil,
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-1.5-pro:generateContent", strings.NewReader(""))
	require.NoError(t, svc.ProxyGeminiGenerateContent(authedCtx("00000000-0000-0000-0000-000000000001"), []byte(geminiInjectedBody), rec, req))

	require.NotNil(t, fr.capturedReq)
	assert.Equal(t, map[string]struct{}{providers.ProviderGoogle: {}}, fr.capturedReq.EnabledProviders)
}

func TestProxyGeminiGenerateContent_CrossFormatReturnsSentinel(t *testing.T) {
	store := newFakePinStore()
	// Cross-format from a Gemini envelope is deferred; handler maps to HTTP 501.
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-haiku-4-5", Reason: "cluster"}}
	svc := proxy.NewService(
		fr,
		map[string]providers.Client{
			providers.ProviderAnthropic: &fakeProvider{},
			providers.ProviderGoogle:    &fakeProvider{},
		},
		nil, false, nil,
		store,
		false, providers.ProviderGoogle, "gemini-2.5-flash",
		nil,
	)

	ctx := authedCtx("00000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-1.5-pro:generateContent", strings.NewReader(""))
	err := svc.ProxyGeminiGenerateContent(ctx, []byte(geminiInjectedBody), rec, httpReq)

	require.Error(t, err)
	assert.True(t, errors.Is(err, proxy.ErrGeminiCrossFormatUnsupported))
}

func TestProxyGeminiGenerateContent_DelaysMarkerUntilFirstUpstreamEvent(t *testing.T) {
	store := newFakePinStore()
	googleProv := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"upstream\"}]}}]}\n\n"))
	}}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: "gemini-2.5-pro", Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderGoogle: googleProv},
		nil, false, nil, store, false, providers.ProviderGoogle, "gemini-2.5-flash", nil,
	)
	rec := httptest.NewRecorder()
	body := strings.Replace(geminiInjectedBody, `"stream":false`, `"stream":true`, 1)
	require.NoError(t, svc.ProxyGeminiGenerateContent(authedCtx("00000000-0000-0000-0000-000000000001"), []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-1.5-pro:streamGenerateContent", nil)))

	markerAt := strings.Index(rec.Body.String(), "Weave Router")
	upstreamAt := strings.Index(rec.Body.String(), "upstream")
	assert.GreaterOrEqual(t, markerAt, 0)
	assert.GreaterOrEqual(t, upstreamAt, 0)
	assert.Less(t, markerAt, upstreamAt, "the first committed upstream event releases the marker")
}

func TestProxyGeminiGenerateContent_RetriesBuffered429WithoutMarkerLeak(t *testing.T) {
	store := newFakePinStore()
	googleProv := &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{
		Status: http.StatusTooManyRequests,
		Body:   []byte(`{"error":{"message":"retry later"}}`),
	}}
	svc := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: "gemini-2.5-pro", Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderGoogle: googleProv},
		nil, false, nil, store, false, providers.ProviderGoogle, "gemini-2.5-flash", nil,
	)
	rec := httptest.NewRecorder()
	body := strings.Replace(geminiInjectedBody, `"stream":false`, `"stream":true`, 1)
	err := svc.ProxyGeminiGenerateContent(authedCtx("00000000-0000-0000-0000-000000000001"), []byte(body), rec,
		httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-1.5-pro:streamGenerateContent", nil))
	require.Error(t, err)
	assert.Len(t, googleProv.proxyBodies, 3, "single-provider 429 retries are bounded")
	assert.NotContains(t, rec.Body.String(), "Weave Router", "a retryable upstream failure must not commit the marker")
	assert.Contains(t, rec.Body.String(), "retry later")
}

func TestProxyGeminiGenerateContent_PersistsPassthroughExperimentTelemetry(t *testing.T) {
	const installationID = "22222222-2222-2222-2222-222222222222"
	routerSpy := &fakeRouter{err: errors.New("scorer must not run for passthrough arm")}
	googleProvider := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":4,"totalTokenCount":1204,"cachedContentTokenCount":1024}}`))
	}}
	telemetry := newCaptureTelemetry()
	service := proxy.NewService(
		routerSpy,
		map[string]providers.Client{providers.ProviderGoogle: googleProvider},
		nil, false, nil, newFakePinStore(), false,
		providers.ProviderGoogle, "gemini-2.5-flash", telemetry,
	)
	body := strings.Replace(geminiInjectedBody, "gemini-1.5-pro", "gemini-2.5-pro", 1)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-pro:generateContent", nil)

	require.NoError(t, service.ProxyGeminiGenerateContent(
		geminiExperimentContext(installationID, auth.BlindExperimentArmPassthrough),
		[]byte(body), recorder, request,
	))

	row := telemetry.firstRow(t)
	assert.Zero(t, routerSpy.routeCalls)
	assert.Equal(t, installationID, row.InstallationID)
	assert.Equal(t, "key-1", row.APIKeyID)
	assert.Equal(t, "gemini-2.5-pro", row.RequestedModel)
	assert.Equal(t, "gemini-2.5-pro", row.DecisionModel)
	assert.Equal(t, providers.ProviderGoogle, row.DecisionProvider)
	assert.Equal(t, "cluster_argmax", row.DecisionReason)
	assert.Equal(t, auth.BlindExperimentArmPassthrough, row.BlindExperimentArm)
	assert.Equal(t, auth.BlindExperimentAssignmentAutomatic, row.BlindExperimentAssignmentSource)
	assert.Equal(t, "account-1", row.BlindExperimentSubjectKey)
	assert.False(t, row.TrainingAllowed)
	assert.Equal(t, int32(1200), row.InputTokens)
	assert.Equal(t, int32(4), row.OutputTokens)
	require.NotNil(t, row.CacheReadTokens)
	assert.Equal(t, int32(1024), *row.CacheReadTokens)
	assert.Zero(t, row.UpstreamStatusCode, "successful rows follow the existing telemetry convention of zero status")
	assert.Positive(t, row.ActualInputCostUSD)
	assert.Positive(t, row.ActualOutputCostUSD)
}

func TestProxyGeminiGenerateContent_PersistsExperimentUpstreamError(t *testing.T) {
	const installationID = "33333333-3333-3333-3333-333333333333"
	googleProvider := &fakeProvider{proxyErr: &providers.UpstreamErrorResponse{
		Status: http.StatusBadRequest,
		Body:   []byte(`{"error":{"message":"invalid request"}}`),
	}}
	telemetry := newCaptureTelemetry()
	service := proxy.NewService(
		&fakeRouter{decision: router.Decision{Provider: providers.ProviderGoogle, Model: "gemini-2.5-pro", Reason: "cluster"}},
		map[string]providers.Client{providers.ProviderGoogle: googleProvider},
		nil, false, nil, newFakePinStore(), false,
		providers.ProviderGoogle, "gemini-2.5-flash", telemetry,
	)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-1.5-pro:generateContent", nil)

	err := service.ProxyGeminiGenerateContent(
		geminiExperimentContext(installationID, auth.BlindExperimentArmRouterOn),
		[]byte(geminiInjectedBody), recorder, request,
	)

	require.Error(t, err)
	row := telemetry.firstRow(t)
	assert.Equal(t, auth.BlindExperimentArmRouterOn, row.BlindExperimentArm)
	assert.Equal(t, auth.BlindExperimentAssignmentAutomatic, row.BlindExperimentAssignmentSource)
	assert.Equal(t, "account-1", row.BlindExperimentSubjectKey)
	assert.True(t, row.TrainingAllowed)
	assert.Equal(t, "gemini-1.5-pro", row.RequestedModel)
	assert.Equal(t, "gemini-2.5-pro", row.DecisionModel)
	assert.Equal(t, providers.ProviderGoogle, row.DecisionProvider)
	assert.Equal(t, int32(http.StatusBadRequest), row.UpstreamStatusCode)
	assert.Zero(t, row.InputTokens)
	assert.Zero(t, row.OutputTokens)
	assert.Len(t, googleProvider.proxyBodies, 1, "a non-retryable upstream error must not be retried")
}
