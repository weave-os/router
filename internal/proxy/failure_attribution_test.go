package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/translate"
)

func attributionService(t *testing.T, sink *recordingAttemptSink, clients map[string]providers.Client) *Service {
	t.Helper()
	executor, err := dispatch.NewExecutor(dispatch.NewClients(clients), dispatch.WithAttemptSink(sink))
	require.NoError(t, err)
	return (&Service{}).WithInferenceExecutor(executor)
}

func TestDispatchPlanned_TranslatedRejectionIsRouterOwned(t *testing.T) {
	inbound := []byte(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`)
	translated := []byte(`{"model":"` + plannedTestModel + `","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	primary := &fakeClient{name: providers.ProviderGoogle, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`bad schema`)}}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	sink := &recordingAttemptSink{}
	s := attributionService(t, sink, map[string]providers.Client{providers.ProviderGoogle: primary, providers.ProviderOpenRouter: fallback})

	ctx := withAttemptTrace(context.Background(), inbound, http.Header{})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderGoogle}, {Provider: providers.ProviderOpenRouter}}, translated)
	in.deferFlushOnExhaustion = true
	_, err := s.dispatchWithFallback(ctx, in)
	require.Error(t, err)

	require.Len(t, sink.events, 1)
	assert.Equal(t, providers.ProviderGoogle, sink.events[0].Target.Provider)
	assert.Equal(t, plannedTestModel, sink.events[0].Target.CatalogID)
	assert.Equal(t, inference.AttemptOutcomeFailed, sink.events[0].Outcome)
	assert.Equal(t, string(FailureOwnerRouter), sink.events[0].Diagnosis.FailureOwner)
	assert.Equal(t, string(TurnErrorInvalidRequest), sink.events[0].Diagnosis.ErrorClass)
	assert.True(t, sink.events[0].Diagnosis.BodyTranslated)
	assert.Equal(t, "Router error: Gemini rejected our translated request (400).", TerminalErrorMessage(err))

	assert.Equal(t, "↳ switched: Gemini rejected our translated request (router issue)\n\n", lastFailureDetail(attemptTraceFromContext(ctx)))
}

func TestDispatchPlanned_UntranslatedRejectionIsProviderOwned(t *testing.T) {
	body := []byte(`{"model":"` + plannedTestModel + `","messages":[]}`)
	primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 400, Body: []byte(`bad`)}}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	sink := &recordingAttemptSink{}
	s := attributionService(t, sink, map[string]providers.Client{providers.ProviderFireworks: primary, providers.ProviderOpenRouter: fallback})

	ctx := withAttemptTrace(context.Background(), body, http.Header{})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}}, body)
	in.deferFlushOnExhaustion = true
	_, err := s.dispatchWithFallback(ctx, in)
	require.Error(t, err)

	require.Len(t, sink.events, 1)
	assert.Equal(t, string(FailureOwnerProvider), sink.events[0].Diagnosis.FailureOwner)
	assert.False(t, sink.events[0].Diagnosis.BodyTranslated)
	assert.Equal(t, "↳ switched: Fireworks rejected the request (provider issue)\n\n", lastFailureDetail(attemptTraceFromContext(ctx)))
}

func TestDispatchPlanned_FailoverRecordsOwnerPerAttemptAndBadgeDetail(t *testing.T) {
	body := []byte(`{"model":"` + plannedTestModel + `","messages":[]}`)
	primary := &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`down`)}}}}
	fallback := &fakeClient{name: providers.ProviderOpenRouter, outcomes: []fakeOutcome{{writeBytes: []byte("ok")}}}
	sink := &recordingAttemptSink{}
	s := attributionService(t, sink, map[string]providers.Client{providers.ProviderFireworks: primary, providers.ProviderOpenRouter: fallback})
	s.retrySleep = func(context.Context, time.Duration) error { return nil }

	ctx := withAttemptTrace(context.Background(), []byte(`{"model":"claude","messages":[]}`), http.Header{})
	rec := httptest.NewRecorder()
	buf := newPreludeBuffer(rec)
	_, err := s.dispatchWithFallback(ctx, plannedInputs(rec, buf,
		[]catalog.ProviderBinding{{Provider: providers.ProviderFireworks}, {Provider: providers.ProviderOpenRouter}}, body))
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(sink.events), 2)
	first, last := sink.events[0], sink.events[len(sink.events)-1]
	assert.Equal(t, providers.ProviderFireworks, first.Target.Provider)
	assert.Equal(t, inference.AttemptOutcomeFailed, first.Outcome)
	assert.Equal(t, string(TurnErrorUpstream5xx), first.Diagnosis.ErrorClass)
	assert.Equal(t, string(FailureOwnerProvider), first.Diagnosis.FailureOwner)
	assert.False(t, first.Diagnosis.BodyTranslated, "a model-only substitution is not a rewrite")
	assert.Equal(t, providers.ProviderOpenRouter, last.Target.Provider)
	assert.Equal(t, inference.AttemptOutcomeServed, last.Outcome)
	assert.Empty(t, last.Diagnosis.FailureOwner)

	marker := withRescueDetail("✦ **Weave Router** → m · reason\n\n", lastFailureDetail(attemptTraceFromContext(ctx)))
	assert.Equal(t, "✦ **Weave Router** → m · reason\n\n↳ switched: Fireworks returned a 503 (provider issue)\n\n", marker)
}

func TestDispatchPlanned_ExhaustionNamesOwnerInTerminalMessage(t *testing.T) {
	body := []byte(`{"model":"` + plannedTestModel + `","messages":[]}`)
	newClient := func() *fakeClient {
		return &fakeClient{name: providers.ProviderFireworks, outcomes: []fakeOutcome{
			{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`down`)}},
			{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`down`)}},
			{err: &providers.UpstreamErrorResponse{Status: 503, Body: []byte(`down`)}},
		}}
	}
	for _, tc := range []struct {
		name    string
		header  http.Header
		message string
	}{
		{"names provider", http.Header{}, "Provider Fireworks failed: server error 503."},
		{"badge opt-out stays generic", http.Header{routingMarkerHeader: []string{"off"}}, upstreamCallFailedMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := attributionService(t, &recordingAttemptSink{}, map[string]providers.Client{providers.ProviderFireworks: newClient()})
			s.retrySleep = func(context.Context, time.Duration) error { return nil }
			ctx := withAttemptTrace(context.Background(), body, tc.header)
			rec := httptest.NewRecorder()
			buf := newPreludeBuffer(rec)
			in := plannedInputs(rec, buf, []catalog.ProviderBinding{{Provider: providers.ProviderFireworks}}, body)
			in.deferFlushOnExhaustion = true
			_, err := s.dispatchWithFallback(ctx, in)
			require.Error(t, err)

			assert.Equal(t, tc.message, TerminalErrorMessage(err))
			cls, ok := ClassifyDispatchError(err)
			require.True(t, ok)
			assert.Equal(t, DispatchErrorUpstreamStatus, cls.Kind)
			assert.Equal(t, 503, cls.Status)
			assert.Equal(t, tc.message, cls.Message)
		})
	}
}

func TestTerminalErrorMessage_RouterAndClientOwners(t *testing.T) {
	err := &providers.UpstreamErrorResponse{Status: 422, Body: []byte(`bad`)}
	router := tracedAttempt{Target: inference.Target{Provider: providers.ProviderGoogle}, Err: err,
		Diagnosis: inference.AttemptDiagnosis{ErrorClass: string(TurnErrorInvalidRequest), FailureOwner: string(FailureOwnerRouter), BodyTranslated: true}}
	assert.Equal(t, "Router error: Gemini rejected our translated request (422).", terminalMessageFor(router))

	client := tracedAttempt{Target: inference.Target{Provider: providers.ProviderAnthropic}, Err: &providers.UpstreamErrorResponse{Status: 401},
		Diagnosis: inference.AttemptDiagnosis{ErrorClass: string(TurnErrorAuthRejected), FailureOwner: string(FailureOwnerClient)}}
	assert.Equal(t, "Request rejected by Anthropic: rejected the request (401).", terminalMessageFor(client))

	unknown := tracedAttempt{Target: inference.Target{Provider: providers.ProviderAnthropic}, Diagnosis: inference.AttemptDiagnosis{FailureOwner: string(FailureOwnerUnknown)}}
	assert.Empty(t, terminalMessageFor(unknown))
	assert.Empty(t, rescueDetailLine(unknown))
	assert.Equal(t, upstreamCallFailedMessage, TerminalErrorMessage(err))
}

func TestRouterOwnedFailureWording(t *testing.T) {
	pool := tracedAttempt{Target: inference.Target{Provider: providers.ProviderAnthropic}, Err: ErrSubscriptionPoolExhausted,
		Diagnosis: inference.AttemptDiagnosis{ErrorClass: string(TurnErrorOther), FailureOwner: string(FailureOwnerRouter)}}
	assert.Equal(t, "↳ switched: Anthropic had no subscription capacity (router issue)\n\n", rescueDetailLine(pool))
	assert.Equal(t, "Router error: Anthropic subscription capacity exhausted.", terminalMessageFor(pool))

	customTool := tracedAttempt{Target: inference.Target{Provider: providers.ProviderAnthropic}, Err: translate.ErrCustomToolArguments,
		Diagnosis: inference.AttemptDiagnosis{ErrorClass: string(TurnErrorOther), FailureOwner: string(FailureOwnerRouter), BodyTranslated: true}}
	assert.Equal(t, "↳ switched: Anthropic sent tool arguments we couldn't translate (router issue)\n\n", rescueDetailLine(customTool))
	assert.Equal(t, "Router error: Anthropic tool call translation failed.", terminalMessageFor(customTool))
}

func TestWithRescueDetail_SuppressedBadgeStaysEmpty(t *testing.T) {
	assert.Empty(t, withRescueDetail("", "↳ switched: Fireworks timed out (provider issue)\n\n"))
}

func TestCustomerOwnedCredential(t *testing.T) {
	for _, tc := range []struct {
		name  string
		creds *requestcontext.Credentials
		want  bool
	}{
		{"deployment key", nil, false},
		{"byok", &requestcontext.Credentials{APIKey: []byte("k"), Source: requestcontext.SourceBYOK}, true},
		{"client key", &requestcontext.Credentials{APIKey: []byte("k"), Source: requestcontext.SourceClient}, true},
		{"own claude subscription", &requestcontext.Credentials{APIKey: []byte("k"), Source: requestcontext.SourceSubscription, OAuth: true}, true},
		{"own codex subscription", &requestcontext.Credentials{APIKey: []byte("k"), Source: requestcontext.SourceCodexSubscription, OAuth: true}, true},
		{"weave-leased subscription", &requestcontext.Credentials{APIKey: []byte("k"), Source: requestcontext.SourceSubscription, SubscriptionAccountID: "acct"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.creds != nil {
				ctx = requestcontext.WithCredentials(ctx, tc.creds)
			}
			assert.Equal(t, tc.want, customerOwnedCredential(ctx))
		})
	}
}

func TestBodyRewrittenIgnoresGeminiRoutingFields(t *testing.T) {
	inbound := []byte(`{"model":"gemini-3-pro","stream":true,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	forwarded := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	if bodyRewritten(geminiForwardedBody(inbound), forwarded) {
		t.Fatal("stripping the synthetic model/stream fields must not count as a rewrite")
	}
	sanitized := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[]}`)
	if !bodyRewritten(geminiForwardedBody(inbound), sanitized) {
		t.Fatal("a body change beyond the routing fields must count as a rewrite")
	}
}

func TestRescueDetailIsStrippedWithTheBadgeOnReplay(t *testing.T) {
	badge := withRescueDetail(
		"\u2063\u2060\u2063\u2060✦ **Weave Router** → model-b · switched after model-a failed\n\n",
		translate.RescueDetailPrefix+"Fireworks returned a 503 (provider issue)\n\n",
	)
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(badge+"answer") + `}]}]}`)
	stripped, err := translate.StripRoutingMarkerFromMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(stripped, "messages.0.content.0.text").String(); got != "answer" {
		t.Fatalf("replayed text = %q, want only the answer", got)
	}
}

func TestCanceledCommittedStreamIsClientOwned(t *testing.T) {
	err := &providers.UpstreamStatusError{Status: http.StatusBadGateway, Cause: context.Canceled}
	if got := classifyFailureOwner(failureOwnerInput{Err: err}); got != FailureOwnerClient {
		t.Fatalf("owner = %q, want client", got)
	}
}
