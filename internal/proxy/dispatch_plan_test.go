package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/translate"
)

const plannedTestModel = "deepseek/deepseek-v4-pro"

func plannedInputs(rec *httptest.ResponseRecorder, buf *preludeBuffer, bindings []catalog.ProviderBinding, body []byte) failoverInputs {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	return failoverInputs{
		w:               rec,
		buf:             buf,
		initialDecision: router.Decision{Model: plannedTestModel, Provider: bindings[0].Provider},
		bindings:        bindings,
		attempt: func(ctx context.Context, d router.Decision, p providers.Client) error {
			buf.Seal()
			return p.Proxy(ctx, d, providers.PreparedRequest{Body: body}, buf, r)
		},
		flushErr: flushBufferedIfPresent,
		purpose:  inference.PurposeAnthropicMessages,
	}
}

func TestDispatchPlanned_PrimarySucceeds(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: primary, providers.ProviderOpenRouter: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 0, winnerIdx)
	assert.Equal(t, 1, primary.calls)
	assert.Equal(t, 0, fallback.calls)
	assert.Equal(t, "ok", rec.Body.String())
	assert.Equal(t, providers.ProviderFireworks, rec.Header().Get(HeaderRouterProvider))
	assert.Equal(t, plannedTestModel, rec.Header().Get(HeaderRouterModel))
}

func TestDispatchPlanned_FailsOverBeforeCommit(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`down`)}}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("rescued")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: primary, providers.ProviderOpenRouter: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 1, winnerIdx)
	assert.Equal(t, 1, primary.calls)
	assert.Equal(t, 1, fallback.calls)
	assert.Equal(t, "rescued", rec.Body.String())
	assert.Equal(t, providers.ProviderFireworks, rec.Header().Get(HeaderRouterFallbackFrom))
	assert.Equal(t, providers.ProviderOpenRouter, rec.Header().Get(HeaderRouterProvider))
}

func TestDispatchPlanned_NoFailoverAfterCommit(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{
		writeBytes: []byte("event: message_start\n\npartial"),
		err:        &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`mid-stream`)},
	}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: primary, providers.ProviderOpenRouter: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}}, nil))

	require.Error(t, err)
	assert.Equal(t, 0, fallback.calls, "bytes reached the client; no retry")
	assert.True(t, buf.Committed())
}

func TestDispatchPlanned_WireModelMismatchNeverReachesProvider(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: primary, providers.ProviderOpenRouter: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}},
		[]byte(`{"model":"some-other-model"}`)))

	require.ErrorIs(t, err, dispatch.ErrTargetMismatch)
	assert.Equal(t, 0, primary.calls, "mismatch fails closed before I/O")
	assert.Equal(t, 0, fallback.calls, "mismatch is not a failover trigger")
	assert.Empty(t, rec.Body.String())
}

func TestDispatchPlanned_WireModelMayNameUpstreamID(t *testing.T) {
	entry, ok := catalog.ByID(plannedTestModel)
	require.True(t, ok)
	binding := entry.Providers[0]
	require.NotEmpty(t, binding.UpstreamID)
	client := &fakeClient{name: binding.Provider, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{binding.Provider: client})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: binding.Provider}}, []byte(`{"model":"`+binding.UpstreamID+`"}`)))

	require.NoError(t, err, "a primary binding without an explicit upstream id inherits the catalog's")
	assert.Equal(t, 1, client.calls)
}

func TestDispatchPlanned_SameBindingRetryUsesInjectedSleep(t *testing.T) {
	only := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{
		{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`blip`)}},
		{writeBytes: []byte("ok")},
	}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: only})
	slept := 0
	s.retrySleep = func(context.Context, time.Duration) error { slept++; return nil }

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderFireworks}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 0, winnerIdx)
	assert.Equal(t, 2, only.calls)
	assert.Equal(t, 1, slept)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestDispatchPlanned_ExhaustionFlushesUpstreamEnvelope(t *testing.T) {
	only := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 404, Body: []byte(`nope`)}}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: only})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderFireworks}}, nil))

	require.Error(t, err)
	assert.Equal(t, 1, only.calls, "404 must not same-binding-retry")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestDispatchPlanned_DeferredFlushLeavesWriterUntouched(t *testing.T) {
	only := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 500, Body: []byte(`boom`)}}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: only})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderFireworks}}, nil)
	in.deferFlushOnExhaustion = true
	_, err := s.dispatchWithFallback(context.Background(), in)

	require.Error(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Body.String(), "caller owns the deferred error")
}

func TestDispatchPlanned_ProviderMissingAtRuntimeSkipsToNext(t *testing.T) {
	fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderOpenRouter: fallback})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	winnerIdx, err := s.dispatchWithFallback(context.Background(), plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}}, nil))

	require.NoError(t, err)
	assert.Equal(t, 1, winnerIdx)
	assert.Equal(t, 1, fallback.calls)
}

func TestDispatchPlanned_RecordsAttemptProvenance(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`down`)}}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	sink := &recordingAttemptSink{}
	executor, err := dispatch.NewExecutor(dispatch.NewClients(map[string]providers.Client{
		providers.ProviderFireworks: primary, providers.ProviderOpenRouter: fallback,
	}), dispatch.WithAttemptSink(sink))
	require.NoError(t, err)
	s := (&Service{}).WithInferenceExecutor(executor)

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}}, nil)
	in.initialDecision.Reason = translate.ReasonUserForceModel
	in.origin = policy.OverrideSourceRequest
	_, err = s.dispatchWithFallback(context.Background(), in)

	require.NoError(t, err)
	require.Len(t, sink.events, 2)
	for _, event := range sink.events {
		assert.Equal(t, inference.PurposeAnthropicMessages, event.Provenance.Purpose)
		assert.NotEmpty(t, event.Provenance.PolicyID)
		assert.Equal(t, plannedTestModel, event.Target.CatalogID)
	}
	assert.Equal(t, providers.ProviderFireworks, sink.events[0].Target.Provider)
	assert.Equal(t, inference.AttemptOutcomeFailed, sink.events[0].Outcome)
	assert.Equal(t, providers.ProviderOpenRouter, sink.events[1].Target.Provider)
	assert.Equal(t, inference.AttemptOutcomeServed, sink.events[1].Outcome)
}

func TestDispatchPlanned_UnregisteredPurposeFailsClosed(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{providers.ProviderFireworks: primary})

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderFireworks}}, nil)
	in.purpose = inference.Purpose("not_a_purpose")
	_, err := s.dispatchWithFallback(context.Background(), in)

	var resolution *policy.ResolutionError
	require.True(t, errors.As(err, &resolution))
	assert.Equal(t, policy.ResolutionErrorUnknownPurpose, resolution.Code)
	assert.Equal(t, 0, primary.calls)
}

type recordingAttemptSink struct{ events []inference.AttemptEvent }

func (r *recordingAttemptSink) RecordAttempt(_ context.Context, event inference.AttemptEvent) {
	r.events = append(r.events, event)
}

func TestDispatchPlanned_LeaseFailureIsTerminal(t *testing.T) {
	primary := &fakeClient{name: providers.ProviderAnthropic, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("paid")}}}
	s := newServiceWithProviders(t, map[string]providers.Client{
		providers.ProviderAnthropic: primary, providers.ProviderOpenRouter: fallback,
	}).WithManagedSubscriptions(&scriptedSubscriptionLeaser{})
	slept := 0
	s.retrySleep = func(context.Context, time.Duration) error { slept++; return nil }
	ctx := context.WithValue(context.Background(), ManagedSubscriptionEnrollmentUnavailableContextKey{}, true)

	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderAnthropic}, {Provider: providers.ProviderOpenRouter}}, nil)
	in.initialDecision.Model = "claude-opus-4-8"
	_, err := s.dispatchWithFallback(ctx, in)

	require.ErrorIs(t, err, ErrSubscriptionPoolUnavailable)
	assert.Equal(t, 0, primary.calls, "no upstream call without a lease")
	assert.Equal(t, 0, fallback.calls, "a pool failure must not fail over to a paid binding")
	assert.Equal(t, 0, slept, "a pool failure must not same-binding retry")
	assert.Empty(t, rec.Body.String(), "the pool error is returned as-is, not rendered as an upstream envelope")
}
