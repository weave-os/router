package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
	"weave-os/router/internal/websearch"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNativeSearchCapturePreservesStatusAndSafeHeaders(t *testing.T) {
	b := &nativeSearchBuffer{header: make(http.Header)}
	b.Header().Set("Retry-After", "17")
	b.Header().Set(HeaderRouterModel, "served-model")
	b.Header().Set("Set-Cookie", "synthetic-cookie")
	b.WriteHeader(http.StatusTooManyRequests)
	_, err := b.Write([]byte(`{"error":{"type":"rate_limit_error"}}`))
	require.NoError(t, err)
	b.WriteHeader(http.StatusOK)
	assert.Equal(t, http.StatusTooManyRequests, b.status)
	rec := httptest.NewRecorder()
	copyNativeSearchHeaders(rec, b.header)
	assert.Equal(t, "17", rec.Header().Get("Retry-After"))
	assert.Equal(t, "served-model", rec.Header().Get(HeaderRouterModel))
	assert.Empty(t, rec.Header().Get("Set-Cookie"))
}

func TestNativeSearchCaptureBoundsUntrustedResponse(t *testing.T) {
	b := &nativeSearchBuffer{header: make(http.Header)}
	_, err := b.Write(bytes.Repeat([]byte("x"), nativeSearchResponseLimit))
	require.NoError(t, err)
	_, err = b.Write([]byte("x"))
	require.Error(t, err)
	assert.Equal(t, nativeSearchResponseLimit, b.body.Len())
}

func TestNativeSearchCaptureCancelsExcessToolOperations(t *testing.T) {
	cancelled := false
	b := &nativeSearchBuffer{header: make(http.Header), maxCalls: 1, calls: make(map[string]struct{}), cancel: func() { cancelled = true }}
	first := "data: " + `{"type":"response.output_item.added","output_index":1,"item":{"id":"search-first","type":"web_search_call"}}` + "\n\n"
	duplicate := "data: " + `{"type":"response.web_search_call.in_progress","output_index":0,"item_id":"search-first"}` + "\n\n"
	second := "data: " + `{"type":"response.web_search_call.in_progress","output_index":2}` + "\n\n"
	for _, event := range []string{first, duplicate, second} {
		require.True(t, json.Valid([]byte(strings.TrimSpace(strings.TrimPrefix(event, "data: ")))))
	}
	for _, chunk := range []string{first[:11], first[11:], duplicate} {
		_, err := b.Write([]byte(chunk))
		require.NoError(t, err)
	}
	assert.Len(t, b.calls, 1, "start notifications for the same item must be deduplicated")
	assert.False(t, cancelled)
	_, err := b.Write([]byte(second))
	require.ErrorIs(t, err, websearch.ErrCallLimitExceeded)
	assert.True(t, cancelled)
}

func TestNativeSearchErrorsUseAnthropicEnvelope(t *testing.T) {
	for status, kind := range map[int]string{400: "invalid_request_error", 401: "authentication_error", 403: "permission_error", 404: "not_found_error", 429: "rate_limit_error", 500: "api_error", 503: "api_error", 413: "request_too_large", 529: "overloaded_error"} {
		cause := errors.New("synthetic dispatch failure")
		response := nativeSearchErrorResponse(status, []byte(`{"error":{"message":"synthetic provider detail","type":"invalid_request","code":"provider-specific"}}`), cause)
		assert.Equal(t, status, response.Status)
		assert.ErrorIs(t, response, cause)
		assert.Equal(t, "error", gjson.GetBytes(response.Body, "type").String())
		assert.Equal(t, kind, gjson.GetBytes(response.Body, "error.type").String())
		assert.Equal(t, "synthetic provider detail", gjson.GetBytes(response.Body, "error.message").String())
		assert.False(t, gjson.GetBytes(response.Body, "error.code").Exists())
	}
	assert.Equal(t, "overloaded_error", gjson.GetBytes(nativeSearchErrorResponse(503, []byte(`{"error":{"type":"overloaded_error","message":"busy"}}`), nil).Body, "error.type").String())
	require.Equal(t, "Upstream web search failed.", gjson.GetBytes(nativeSearchErrorResponse(http.StatusBadGateway, []byte("unstructured"), nil).Body, "error.message").String())
}

func TestNativeSearchPreservesRoutingPassthroughWithoutPins(t *testing.T) {
	spy := &blindExperimentRouterSpy{err: errors.New("scorer must not run")}
	pins := newStubPinStore()
	service := NewService(spy, nil, nil, false, nil, pins, false, providers.ProviderOpenAI, catalog.ModelIDGPT56Luna.String(), nil)
	authService := auth.NewService(nil, nil, nil, nil, nil, nil, time.Now).WithRoutingPolicies(routingPolicyStub{mode: auth.RoutingPolicyPassthrough}, nil)
	ctx, err := authService.WithRoutingPolicy(context.Background(), "installation")
	require.NoError(t, err)
	ctx = context.WithValue(ctx, codexWebSearchContextKey{}, true)
	body := []byte(`{"model":"gpt-5.6-luna","messages":[{"role":"user","content":"isolated search"}]}`)
	envelope, err := translate.ParseOpenAI(body)
	require.NoError(t, err)
	request := router.Request{RequestedModel: catalog.ModelIDGPT56Luna.String(), EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}}}
	turn, err := service.runTurnLoop(ctx, envelope, envelope.RoutingFeatures(false), "api-key", uuid.New(), "", http.Header{}, request)
	require.NoError(t, err)
	assert.True(t, turn.CallerModelPassthrough)
	assert.True(t, turn.HardPinned)
	assert.Equal(t, catalog.ModelIDGPT56Luna.String(), turn.Decision.Model)
	assert.Equal(t, [sessionpin.SessionKeyLen]byte{}, turn.SessionKey)
	assert.Zero(t, spy.routeCalls)
	assert.Empty(t, pins.getRoles)
	assert.Empty(t, pins.upserts)
}
