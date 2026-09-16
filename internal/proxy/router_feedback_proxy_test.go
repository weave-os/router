package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePolicyFeedbackRouter struct {
	mu       sync.Mutex
	decision router.Decision
	requests []router.Request
	payloads []map[string]interface{}
	err      error
	// applied records which feedback_ids the receiver has durably recorded,
	// modelling the deduplication contract a retry-safe receiver owes.
	applied map[string]int
}

func (f *fakePolicyFeedbackRouter) Route(ctx context.Context, req router.Request) (router.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return f.decision, nil
}

func (f *fakePolicyFeedbackRouter) ReportFeedback(ctx context.Context, payload map[string]interface{}) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.payloads = append(f.payloads, payload)
	if f.err != nil {
		return f.err
	}
	if f.applied == nil {
		f.applied = make(map[string]int)
	}
	id, _ := payload["feedback_id"].(string)
	if _, seen := f.applied[id]; !seen {
		f.applied[id] = 0
	}
	f.applied[id]++
	return nil
}

func (f *fakePolicyFeedbackRouter) Payloads() []map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]interface{}(nil), f.payloads...)
}

func (f *fakePolicyFeedbackRouter) Requests() []router.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]router.Request(nil), f.requests...)
}

// Deliveries returns how many times each feedback_id was received, which a
// deduplicating receiver collapses to one effect regardless of the count.
func (f *fakePolicyFeedbackRouter) Deliveries() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.applied))
	for id, n := range f.applied {
		out[id] = n
	}
	return out
}

// DistinctEffects is the number of distinct feedback events a deduplicating
// receiver would apply.
func (f *fakePolicyFeedbackRouter) DistinctEffects() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.applied)
}

func (f *fakePolicyFeedbackRouter) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// terminalAnthropicBody is a complete, successful Anthropic response. A turn
// that actually dispatches needs one: the completion gate withholds terminal
// success until history is committed, and a fixture with no terminal event is
// indistinguishable from a truncated upstream.
const terminalAnthropicBody = `{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`

// newDispatchingPinSvc builds a pin-backed service whose upstream answers with
// a real terminal response, for feedback tests whose turns reach dispatch.
func newDispatchingPinSvc(fr *fakeRouter, store *fakePinStore) *proxy.Service {
	upstream := &fakeProvider{proxyResponse: func(w http.ResponseWriter) {
		_, _ = w.Write([]byte(terminalAnthropicBody))
	}}
	return proxy.NewService(
		fr,
		map[string]providers.Client{providers.ProviderAnthropic: upstream},
		nil,
		false,
		nil,
		store,
		false,
		providers.ProviderAnthropic,
		feedbackAlternateModel,
		nil,
	)
}

// feedbackScopeFor derives the session key a /rf command in this body resolves
// against, so a test can seed the matching completion history.
func feedbackScopeFor(t *testing.T, body string, apiKeyID string) []byte {
	t.Helper()
	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	key := proxy.DeriveSessionKey(env, apiKeyID)
	return key[:]
}

const feedbackSessionPreamble = `{"role":"user","content":"refactor the router"},{"role":"assistant","content":"done"},`

func feedbackCommandBody(command string) string {
	return `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"messages":[` + feedbackSessionPreamble + `
			{"role":"user","content":"` + command + `"}
		]
	}`
}

// seedFeedbackHistory records completed responses in the scope the command in
// body will resolve against, oldest first.
func seedFeedbackHistory(t *testing.T, store *fakeFeedbackStore, installationID, body string, requests ...proxy.FeedbackRequest) []byte {
	t.Helper()
	key := feedbackScopeFor(t, body, "key-1")
	for _, req := range requests {
		store.complete(installationID, key, "default_mid", req)
	}
	return key
}

func TestService_RouterFeedbackCommand_SavesAgainstLastCompletedResponse(t *testing.T) {
	body := feedbackCommandBody("/router-feedback got stuck on Haiku for too long")
	store := newFakePinStore()
	store.hasPin = true
	// A stale pin must not supply the attribution any more: the saved history
	// row is the only thing the rating may be attached to.
	store.pin = sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: feedbackAlternateModel, LastServedModel: feedbackAlternateModel}
	feedback := newFakeFeedbackStore()
	telem := newCaptureTelemetry()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvcWithTelemetry(fr, store, telem).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	installationID := uuid.NewString()
	key := seedFeedbackHistory(t, feedback, installationID, body,
		proxy.FeedbackRequest{RequestID: "req-older", ServedModel: feedbackAlternateModel, ServedProvider: providers.ProviderAnthropic, Strategy: "cluster"},
		proxy.FeedbackRequest{RequestID: "req-latest", ServedModel: feedbackServedModel, ServedProvider: providers.ProviderAnthropic, Strategy: "rl", RouteID: "rl:xyz"},
	)

	ctx := authedCtx(installationID)
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "router-feedback command must short-circuit routing")
	assert.Empty(t, telem.seqCalls, "attribution comes from committed history, never from telemetry")
	events := feedback.acceptedEvents()
	require.Len(t, events, 1)
	ev := events[0]
	assert.Equal(t, key, ev.SessionKey, "the command must resolve in the request's own session scope")
	assert.Equal(t, installationID, ev.InstallationID)
	assert.Equal(t, "got stuck on Haiku for too long", ev.Feedback)
	assert.Equal(t, -1, ev.Sequence, "an omitted selector normalizes to the last response")
	assert.Equal(t, int64(2), ev.TargetSequence)
	assert.Equal(t, "req-latest", ev.RequestID)
	assert.Equal(t, feedbackServedModel, ev.ServedModel, "served model comes from the resolved history row, not the pin")
	assert.Equal(t, "rl", ev.Strategy)
	assert.Equal(t, "rl:xyz", ev.RouteID)
	assert.Equal(t, feedbackRequestedModel, ev.RequestedModel)
	assert.Equal(t, proxy.RouterFeedbackPending, ev.DeliveryStatus)
	assert.NotEmpty(t, ev.ID, "the command carries a durable delivery identity")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	blocks, ok := resp["content"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, blocks)
	first, _ := blocks[0].(map[string]any)
	text, _ := first["text"].(string)
	assert.Contains(t, text, "Feedback saved")
	assert.NotContains(t, text, "unattached")
}

func TestService_RouterFeedbackCommand_SelectorCountsFromBothEnds(t *testing.T) {
	for _, tc := range []struct {
		name            string
		command         string
		wantSequence    int
		wantTarget      int64
		wantRequestID   string
		wantServedModel string
	}{
		{"negative selector counts back from the newest", "/rf -2 - wrong tier", -2, 2, "req-second", feedbackAlternateModel},
		{"positive selector counts from the first response", "/rf 1 - wrong tier", 1, 1, "req-first", feedbackRequestedModel},
		{"bare command rates the newest response", "/rf- wrong tier", -1, 3, "req-third", feedbackServedModel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := feedbackCommandBody(tc.command)
			store := newFakePinStore()
			feedback := newFakeFeedbackStore()
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
			svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

			installationID := uuid.NewString()
			seedFeedbackHistory(t, feedback, installationID, body,
				proxy.FeedbackRequest{RequestID: "req-first", ServedModel: feedbackRequestedModel},
				proxy.FeedbackRequest{RequestID: "req-second", ServedModel: feedbackAlternateModel},
				proxy.FeedbackRequest{RequestID: "req-third", ServedModel: feedbackServedModel},
			)

			httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
			require.NoError(t, svc.ProxyMessages(authedCtx(installationID), []byte(body), httptest.NewRecorder(), httpReq))

			events := feedback.acceptedEvents()
			require.Len(t, events, 1)
			assert.Equal(t, tc.wantSequence, events[0].Sequence)
			assert.Equal(t, tc.wantTarget, events[0].TargetSequence)
			assert.Equal(t, tc.wantRequestID, events[0].RequestID)
			assert.Equal(t, tc.wantServedModel, events[0].ServedModel)
		})
	}
}

func TestService_RouterFeedbackCommand_OutOfRangeSelectorSavesUnattached(t *testing.T) {
	body := feedbackCommandBody("/rf -9 too slow")
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	installationID := uuid.NewString()
	seedFeedbackHistory(t, feedback, installationID, body, proxy.FeedbackRequest{RequestID: "req-only", ServedModel: feedbackAlternateModel})

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(authedCtx(installationID), []byte(body), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "an unresolvable selector must still short-circuit routing")
	events := feedback.acceptedEvents()
	require.Len(t, events, 1, "out-of-range feedback is saved, not discarded")
	assert.Empty(t, events[0].RequestID, "it must never fall back to a different request")
	assert.Zero(t, events[0].TargetSequence)
	assert.Equal(t, "too slow", events[0].Feedback)
	assert.Empty(t, feedback.ratingFor("req-only"), "an unattached command must not rate an unrelated request")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	blocks, _ := resp["content"].([]any)
	require.NotEmpty(t, blocks)
	first, _ := blocks[0].(map[string]any)
	text, _ := first["text"].(string)
	assert.Contains(t, text, "Feedback saved")
	assert.Contains(t, text, "unattached", "the ack must say the feedback could not be attached")
}

func TestService_RouterFeedbackCommand_NoteOnlyPreservesExistingThumb(t *testing.T) {
	body := feedbackCommandBody("/rf the diff was incomplete")
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	installationID := uuid.NewString()
	seedFeedbackHistory(t, feedback, installationID, body, proxy.FeedbackRequest{RequestID: "req-note-only", ServedModel: feedbackServedModel})
	feedback.setRating("req-note-only", "up")

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(authedCtx(installationID), []byte(body), httptest.NewRecorder(), httpReq))

	events := feedback.acceptedEvents()
	require.Len(t, events, 1)
	assert.Empty(t, events[0].Rating, "a note-only submission carries no verdict")
	assert.Equal(t, "the diff was incomplete", events[0].Feedback)
	assert.Equal(t, "req-note-only", events[0].RequestID)
	assert.Equal(t, "up", feedback.ratingFor("req-note-only"), "a note alone must not overwrite an existing thumb")
}

func TestService_RouterFeedbackCommand_RatedSubmissionAppliesLocalRating(t *testing.T) {
	body := feedbackCommandBody("/rf- too slow")
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	installationID := uuid.NewString()
	seedFeedbackHistory(t, feedback, installationID, body, proxy.FeedbackRequest{RequestID: "req-rated", ServedModel: feedbackServedModel})
	feedback.setRating("req-rated", "up")

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(authedCtx(installationID), []byte(body), httptest.NewRecorder(), httpReq))

	assert.Equal(t, "down", feedback.ratingFor("req-rated"), "the command and its local rating are saved together")
	events := feedback.acceptedEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "down", events[0].Rating)
}

func TestService_RouterFeedbackCommand_AcceptanceFailureIsNotAcknowledged(t *testing.T) {
	body := feedbackCommandBody("/rf- too slow")
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	feedback.acceptErr = assert.AnError
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	err := svc.ProxyMessages(authedCtx(uuid.NewString()), []byte(body), rec, httpReq)

	require.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, feedback.acceptedEvents(), "a rolled-back acceptance saves nothing")
	assert.NotContains(t, rec.Body.String(), "Feedback saved", "a failed save must not be acknowledged as saved")
}

func TestService_RouterFeedbackCommand_WithoutDurableStoreIsExplicitlyUnavailable(t *testing.T) {
	body := feedbackCommandBody("/rf- too slow")
	store := newFakePinStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t))

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	err := svc.ProxyMessages(authedCtx(uuid.NewString()), []byte(body), rec, httpReq)

	require.ErrorIs(t, err, proxy.ErrFeedbackUnavailable, "a deployment without durable storage reports /rf unavailable")
	assert.Equal(t, 0, fr.routeCalls, "an unavailable command must not dispatch upstream instead")
	assert.NotContains(t, rec.Body.String(), "Feedback saved")
}

func TestService_RouterFeedbackCommand_ScopeIsolation(t *testing.T) {
	body := feedbackCommandBody("/rf- wrong tier")
	otherSessionBody := `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"messages":[
			{"role":"user","content":"a different conversation"},
			{"role":"user","content":"/rf- wrong tier"}
		]
	}`
	installationID := uuid.NewString()
	otherInstallation := uuid.NewString()
	key := feedbackScopeFor(t, body, "key-1")

	for _, tc := range []struct {
		name  string
		write func(store *fakeFeedbackStore)
	}{
		{"another installation's history is not visible", func(store *fakeFeedbackStore) {
			store.complete(otherInstallation, key, "default_mid", proxy.FeedbackRequest{RequestID: "req-other-install"})
		}},
		{"another session's history is not visible", func(store *fakeFeedbackStore) {
			store.complete(installationID, feedbackScopeFor(t, otherSessionBody, "key-1"), "default_mid", proxy.FeedbackRequest{RequestID: "req-other-session"})
		}},
		{"another API key's history is not visible", func(store *fakeFeedbackStore) {
			store.complete(installationID, feedbackScopeFor(t, body, "key-2"), "default_mid", proxy.FeedbackRequest{RequestID: "req-other-key"})
		}},
		{"another tier role's history is not visible", func(store *fakeFeedbackStore) {
			store.complete(installationID, key, "default_high", proxy.FeedbackRequest{RequestID: "req-other-role"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakePinStore()
			feedback := newFakeFeedbackStore()
			tc.write(feedback)
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
			svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

			httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
			require.NoError(t, svc.ProxyMessages(authedCtx(installationID), []byte(body), httptest.NewRecorder(), httpReq))

			events := feedback.acceptedEvents()
			require.Len(t, events, 1)
			assert.Empty(t, events[0].RequestID, "history from another scope must never be attached")
		})
	}
}

func TestService_RouterFeedbackCommand_DoesNotReportToPolicyBeforeAcknowledging(t *testing.T) {
	body := feedbackCommandBody("/rf- wrong tier")
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	policyFeedback := &fakePolicyFeedbackRouter{}
	workers := testObservationWorkers(t)
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvc(fr, store).WithObservationWorkers(workers).
		WithRouterFeedbackStore(feedback).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyRL, Router: policyFeedback, FeedbackRetrySafe: true})

	installationID := uuid.NewString()
	seedFeedbackHistory(t, feedback, installationID, body, proxy.FeedbackRequest{RequestID: "req-rated", ServedModel: feedbackServedModel, Strategy: string(router.StrategyRL)})

	ctx := router.WithStrategy(authedCtx(installationID), router.StrategyRL)
	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, httpReq))

	assert.Contains(t, rec.Body.String(), "Feedback saved")
	drainObservationWorkers(t, workers)
	assert.Empty(t, policyFeedback.Payloads(), "reporting is the durable processor's job, not the request path's")
	events := feedback.acceptedEvents()
	require.Len(t, events, 1)
	assert.Equal(t, proxy.RouterFeedbackPending, events[0].DeliveryStatus, "the saved command is the pending-work record")
}

func TestService_RouterFeedbackCommand_AgentToolResultContinuesRouting(t *testing.T) {
	const body = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_skill","name":"exec","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_skill","content":" /router-feedback too slow"}]}
		]
	}`
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newDispatchingPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(authedCtx(uuid.NewString()), []byte(body), rec, httpReq))

	assert.Equal(t, 1, fr.routeCalls, "agent-issued feedback must continue into an agent turn")
	events := feedback.acceptedEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "too slow", events[0].Feedback)
	assert.NotContains(t, rec.Body.String(), "Feedback saved", "agent-issued feedback must not terminate with a synthetic ack")
}

func TestService_RouterFeedbackCommand_EmptyFeedbackAsksForText(t *testing.T) {
	body := feedbackCommandBody("/router-feedback")
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(authedCtx(uuid.NewString()), []byte(body), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "bare command must still short-circuit, not reach an upstream")
	assert.Empty(t, feedback.acceptedEvents(), "empty feedback must not be persisted")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	blocks, _ := resp["content"].([]any)
	require.NotEmpty(t, blocks)
	first, _ := blocks[0].(map[string]any)
	text, _ := first["text"].(string)
	assert.Contains(t, text, "needs a verdict or a note")
}

func TestService_RouterFeedbackCommand_ThumbsUpShortcutPersists(t *testing.T) {
	body := feedbackCommandBody("/rf+")
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	installationID := uuid.NewString()
	seedFeedbackHistory(t, feedback, installationID, body, proxy.FeedbackRequest{RequestID: "req-rated", ServedModel: feedbackAlternateModel})

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
	require.NoError(t, svc.ProxyMessages(authedCtx(installationID), []byte(body), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls, "rating shortcut must short-circuit routing")
	events := feedback.acceptedEvents()
	require.Len(t, events, 1, "a verdict-only rating must still persist")
	assert.Equal(t, "up", events[0].Rating)
	assert.Equal(t, "👍", events[0].Feedback, "verdict-only submission stores a compact label")
	assert.Equal(t, "up", feedback.ratingFor("req-rated"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	blocks, _ := resp["content"].([]any)
	require.NotEmpty(t, blocks)
	first, _ := blocks[0].(map[string]any)
	text, _ := first["text"].(string)
	assert.Contains(t, text, "👍", "the ack echoes the recorded verdict")
}

func TestService_RouterFeedbackCommand_OpenAIIngress(t *testing.T) {
	const body = `{
		"model":"gpt-4o",
		"messages":[
			{"role":"user","content":"/router-feedback wrong model for this refactor"}
		]
	}`
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenAIPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.NewString()), []byte(body), rec, httpReq))

	assert.Equal(t, 0, fr.routeCalls)
	events := feedback.acceptedEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "wrong model for this refactor", events[0].Feedback)
	assert.Empty(t, events[0].RequestID, "no completed history in this scope, so the save is unattached")

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "chat.completion", resp["object"])
	choices, _ := resp["choices"].([]any)
	require.NotEmpty(t, choices)
	first, _ := choices[0].(map[string]any)
	msg, _ := first["message"].(map[string]any)
	content, _ := msg["content"].(string)
	assert.Contains(t, content, "Feedback saved")
}

func TestService_RouterFeedbackCommand_ThumbsDownShortcutWithNote(t *testing.T) {
	const body = `{
		"model":"gpt-4o",
		"messages":[
			{"role":"user","content":"/rf- wrong model for this refactor"}
		]
	}`
	store := newFakePinStore()
	feedback := newFakeFeedbackStore()
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-4o", Reason: "cluster"}}
	svc := newOpenAIPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).WithRouterFeedbackStore(feedback)

	rec := httptest.NewRecorder()
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(""))
	require.NoError(t, svc.ProxyOpenAIChatCompletion(authedCtx(uuid.NewString()), []byte(body), rec, httpReq))

	events := feedback.acceptedEvents()
	require.Len(t, events, 1)
	assert.Equal(t, "down", events[0].Rating)
	assert.Equal(t, "wrong model for this refactor", events[0].Feedback, "the note is stored verbatim alongside the verdict")
}

func TestService_RouterFeedbackCommand_PreservesAutomaticPinForOneFollowup(t *testing.T) {
	const feedbackBody = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"metadata":{"user_id":"pi:post-command-continuation"},
		"messages":[
			{"role":"user","content":"inspect the router state"},
			{"role":"assistant","content":"I found the route."},
			{"role":"user","content":"/rf+"}
		]
	}`
	const followupBody = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"metadata":{"user_id":"pi:post-command-continuation"},
		"messages":[
			{"role":"user","content":"inspect the router state"},
			{"role":"assistant","content":"I found the route."},
			{"role":"user","content":"/rf+"},
			{"role":"assistant","content":"✦ **Weave Router** → Feedback saved 👍.\n\n"},
			{"role":"user","content":"continue"}
		]
	}`
	const laterBody = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"metadata":{"user_id":"pi:post-command-continuation"},
		"messages":[
			{"role":"user","content":"inspect the router state"},
			{"role":"assistant","content":"I found the route."},
			{"role":"user","content":"/rf+"},
			{"role":"assistant","content":"✦ **Weave Router** → Feedback saved 👍.\n\n"},
			{"role":"user","content":"continue"},
			{"role":"assistant","content":"Continuing on the existing route."},
			{"role":"user","content":"now assess another task"}
		]
	}`

	sourceExpiry := time.Now().Add(time.Minute)
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           feedbackAlternateModel,
		Reason:          "hmm_policy(label=balanced)",
		LastServedModel: feedbackAlternateModel,
		PinnedUntil:     sourceExpiry,
	}
	policyRouter := &fakePolicyFeedbackRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    feedbackRequestedModel,
		Reason:   "hmm_policy(label=high)",
		Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMMEmbedding)},
	}}
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: feedbackRequestedModel, Reason: "cluster"}}
	svc := newDispatchingPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).
		WithRouterFeedbackStore(newFakeFeedbackStore()).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: router.StrategyHMMEmbedding,
			Router:   policyRouter,
			Capabilities: policy.Capabilities{
				SchemaVersion:                 policy.SchemaVersionV1,
				AuthoritativePerTurnSelection: true,
			},
		})
	ctx := router.WithStrategy(authedCtx(uuid.NewString()), router.StrategyHMMEmbedding)
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	require.NoError(t, svc.ProxyMessages(ctx, []byte(feedbackBody), httptest.NewRecorder(), httpReq))
	assert.Empty(t, policyRouter.Requests(), "the synthetic feedback response must not route")
	store.mu.Lock()
	continuations := make([]sessionpin.Pin, 0, len(store.commandContinuations))
	for _, continuation := range store.commandContinuations {
		continuations = append(continuations, continuation)
	}
	store.mu.Unlock()
	require.Len(t, continuations, 1)
	assert.True(t, continuations[0].PinnedUntil.After(sourceExpiry),
		"the continuation must renew a near-expiry automatic pin")

	followupRecorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(followupBody), followupRecorder, httpReq))
	assert.Empty(t, policyRouter.Requests(), "the first normal turn after a slash command must reuse the automatic pin")
	assert.Equal(t, feedbackAlternateModel, followupRecorder.Header().Get(proxy.HeaderRouterModel))

	laterRecorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(laterBody), laterRecorder, httpReq))
	require.Len(t, policyRouter.Requests(), 1, "the one-shot continuation must be consumed after one normal turn")
	assert.Equal(t, feedbackRequestedModel, laterRecorder.Header().Get(proxy.HeaderRouterModel))
}

func TestService_RouterFeedbackCommand_DoesNotContinueMaxedPin(t *testing.T) {
	const feedbackBody = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"metadata":{"user_id":"pi:maxed-post-command"},
		"messages":[
			{"role":"user","content":"inspect the router state"},
			{"role":"assistant","content":"I found the route."},
			{"role":"user","content":"/rf+"}
		]
	}`
	const followupBody = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"metadata":{"user_id":"pi:maxed-post-command"},
		"messages":[
			{"role":"user","content":"inspect the router state"},
			{"role":"assistant","content":"I found the route."},
			{"role":"user","content":"/rf+"},
			{"role":"assistant","content":"✦ **Weave Router** → Feedback saved 👍.\n\n"},
			{"role":"user","content":"continue"}
		]
	}`

	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           feedbackAlternateModel,
		Reason:          "hmm_policy(label=balanced)",
		LastServedModel: feedbackAlternateModel,
		// Keep this in sync with prevTurnMaxedOutThreshold. A saturated source
		// pin must not be copied into a one-shot post-command continuation.
		LastOutputTokens: 8000,
		PinnedUntil:      time.Now().Add(time.Minute),
	}
	policyRouter := &fakePolicyFeedbackRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    feedbackRequestedModel,
		Reason:   "hmm_policy(label=high)",
		Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMMEmbedding)},
	}}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    feedbackRequestedModel,
		Reason:   "cluster",
	}}
	svc := newDispatchingPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).
		WithRouterFeedbackStore(newFakeFeedbackStore()).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: router.StrategyHMMEmbedding,
			Router:   policyRouter,
			Capabilities: policy.Capabilities{
				SchemaVersion:                 policy.SchemaVersionV1,
				AuthoritativePerTurnSelection: true,
			},
		})
	ctx := router.WithStrategy(authedCtx(uuid.NewString()), router.StrategyHMMEmbedding)
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	require.NoError(t, svc.ProxyMessages(ctx, []byte(feedbackBody), httptest.NewRecorder(), httpReq))
	assert.Empty(t, policyRouter.Requests(), "the synthetic feedback response must not route")
	store.mu.Lock()
	continuationCount := len(store.commandContinuations)
	store.mu.Unlock()
	assert.Zero(t, continuationCount, "a maxed source pin must not create a continuation")

	followupRecorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(followupBody), followupRecorder, httpReq))
	require.Len(t, policyRouter.Requests(), 1, "the maxed source pin must be excluded before fresh routing")
	assert.Equal(t, feedbackRequestedModel, followupRecorder.Header().Get(proxy.HeaderRouterModel))
}

func TestService_RouterFeedbackCommand_DoesNotResurrectClearedPin(t *testing.T) {
	const feedbackBody = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"metadata":{"user_id":"pi:cleared-post-command"},
		"messages":[
			{"role":"user","content":"inspect the router state"},
			{"role":"assistant","content":"I found the route."},
			{"role":"user","content":"/rf+"}
		]
	}`
	const followupBody = `{
		"model":"claude-sonnet-4-6",
		"max_tokens":1024,
		"metadata":{"user_id":"pi:cleared-post-command"},
		"messages":[
			{"role":"user","content":"inspect the router state"},
			{"role":"assistant","content":"I found the route."},
			{"role":"user","content":"/rf+"},
			{"role":"assistant","content":"✦ **Weave Router** → Feedback saved 👍.\n\n"},
			{"role":"user","content":"continue"}
		]
	}`

	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider:        providers.ProviderAnthropic,
		Model:           feedbackAlternateModel,
		Reason:          "hmm_policy(label=balanced)",
		LastServedModel: feedbackAlternateModel,
		PinnedUntil:     time.Now().Add(time.Minute),
	}
	policyRouter := &fakePolicyFeedbackRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    feedbackRequestedModel,
		Reason:   "hmm_policy(label=high)",
		Metadata: &router.RoutingMetadata{Strategy: string(router.StrategyHMMEmbedding)},
	}}
	fr := &fakeRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    feedbackRequestedModel,
		Reason:   "cluster",
	}}
	svc := newDispatchingPinSvc(fr, store).WithObservationWorkers(testObservationWorkers(t)).
		WithRouterFeedbackStore(newFakeFeedbackStore()).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy: router.StrategyHMMEmbedding,
			Router:   policyRouter,
			Capabilities: policy.Capabilities{
				SchemaVersion:                 policy.SchemaVersionV1,
				AuthoritativePerTurnSelection: true,
			},
		})
	ctx := router.WithStrategy(authedCtx(uuid.NewString()), router.StrategyHMMEmbedding)
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))

	require.NoError(t, svc.ProxyMessages(ctx, []byte(feedbackBody), httptest.NewRecorder(), httpReq))
	store.mu.Lock()
	continuationCount := len(store.commandContinuations)
	// Simulate a later intentional eviction. The continuation remains here to
	// verify the turn loop cannot revive the cleared source pin if cleanup was
	// delayed or ran on another process.
	store.pin = sessionpin.Pin{
		Reason:      "degenerate_response",
		PinnedUntil: time.Now().Add(-time.Second),
	}
	store.mu.Unlock()
	require.Equal(t, 1, continuationCount, "feedback must create the expected one-shot continuation")

	followupRecorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(followupBody), followupRecorder, httpReq))
	require.Len(t, policyRouter.Requests(), 1, "a stale continuation must not restore an intentionally cleared route")
	assert.Equal(t, feedbackRequestedModel, followupRecorder.Header().Get(proxy.HeaderRouterModel))
	store.mu.Lock()
	continuationCount = len(store.commandContinuations)
	store.mu.Unlock()
	assert.Zero(t, continuationCount, "the rejected continuation must be consumed")
}
