package httputil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewClientPropagatesTraceContext(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	otel.SetTextMapPropagator(propagation.TraceContext{})

	traceID, err := oteltrace.TraceIDFromHex("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	spanID, err := oteltrace.SpanIDFromHex("0123456789abcdef")
	require.NoError(t, err)
	ctx := oteltrace.ContextWithRemoteSpanContext(context.Background(), oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	}))
	member, err := baggage.NewMember("tenant", "internal")
	require.NoError(t, err)
	bag, err := baggage.New(member)
	require.NoError(t, err)
	ctx = baggage.ContextWithBaggage(ctx, bag)

	var got, gotBaggage string
	client := NewClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = req.Header.Get("traceparent")
		gotBaggage = req.Header.Get("baggage")
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody, Header: make(http.Header)}, nil
	}))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01", got)
	assert.Empty(t, gotBaggage)
}

func TestNewClientFailsTheCallOnARedirect(t *testing.T) {
	// atomic: written on the httptest handler goroutine, read here.
	var redirectTargetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	client := NewClient(NewTransport(time.Second, time.Second))
	req, err := http.NewRequest(http.MethodGet, redirector.URL, nil)
	require.NoError(t, err)

	resp, err := client.Do(req)

	require.Error(t, err, "a refused redirect fails the call rather than yielding a relayable response")
	// http.Client wraps CheckRedirect's error in *url.Error; the sentinel has to
	// survive that for dispatch classification to recognize it.
	assert.ErrorIs(t, err, ErrRefusedRedirect)
	assert.False(t, redirectTargetHit.Load(), "the redirect target must never be contacted")

	// Do returns the pre-redirect response alongside the error with its body already
	// closed; pin that it is unusable even if an adapter ever skipped the err check.
	require.NotNil(t, resp)
	n, readErr := resp.Body.Read(make([]byte, 1))
	assert.Zero(t, n)
	assert.Error(t, readErr, "the returned response body is closed")
}
