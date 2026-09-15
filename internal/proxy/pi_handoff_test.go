package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/inference"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"
)

const handoffTestBody = `{"model":"claude-sonnet-4-6","max_tokens":1024,"stream":false,"metadata":{"user_id":"pi:handoff-test"},"messages":[{"role":"user","content":"Implement the parser and verify its behavior."}]}`

type handoffResponseProvider struct{ betaCaptureProvider }

func (p *handoffResponseProvider) Proxy(ctx context.Context, decision router.Decision, req providers.PreparedRequest, w http.ResponseWriter, request *http.Request) error {
	if err := p.betaCaptureProvider.Proxy(ctx, decision, req, w, request); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	_, err := io.WriteString(w, `{"type":"message","role":"assistant","content":[{"type":"text","text":"Review complete."}],"stop_reason":"end_turn","usage":{"input_tokens":2000,"output_tokens":10}}`)
	return err
}

func handoffTestService() (*Service, *betaTestRouter, *betaCaptureProvider, context.Context) {
	classifier := &betaTestRouter{decision: router.Decision{
		Model: "claude-opus-4-7", Provider: providers.ProviderAnthropic,
		Metadata: &router.RoutingMetadata{PolicyGroup: "high", AuthoritativePerTurnSelection: true},
	}}
	upstream := &betaCaptureProvider{}
	svc := NewService(nil, map[string]providers.Client{providers.ProviderAnthropic: upstream}, nil, false, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithPiHandoffSecret(strings.Repeat("test-secret-", 4)).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMM, Router: classifier, Capabilities: policy.Capabilities{AuthoritativePerTurnSelection: true}})
	ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "handoff-test-key")
	ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
	return svc, classifier, upstream, router.WithStrategy(ctx, router.StrategyHMM)
}

func prepareTestHandoff(t *testing.T, svc *Service, ctx context.Context) preparedHandoff {
	t.Helper()
	recorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(WithHandoffPreparation(ctx), []byte(handoffTestBody), recorder, httptest.NewRequest("POST", "/v1/route/handoff", nil)))
	var prepared preparedHandoff
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &prepared), recorder.Body.String())
	require.NotEmpty(t, prepared.Token)
	require.NotEmpty(t, prepared.SessionToken)
	return prepared
}

func TestPiHandoffPreparationDoesNotDispatchAndContinuationDoesNotReclassify(t *testing.T) {
	svc, classifier, upstream, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	require.Empty(t, upstream.body)
	require.Equal(t, 1, classifier.calls)
	require.Equal(t, "high", prepared.Complexity)
	classifier.decision.Model = "claude-haiku-4-5"
	body, err := sjson.Set(handoffTestBody, "messages.0.content", "Compacted parser context; implement the remaining checks.")
	require.NoError(t, err)
	body, err = sjson.Set(body, piHandoffField, prepared.Token)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), recorder, httptest.NewRequest("POST", "/v1/messages", nil)))
	require.Equal(t, 1, classifier.calls)
	require.Equal(t, prepared.Model, gjson.GetBytes(upstream.body, "model").String())
	require.False(t, gjson.GetBytes(upstream.body, piHandoffField).Exists())
	require.Contains(t, string(upstream.body), "Compacted parser context")
}

func TestPiHandoffStruggleEscalationPreservesClassAndPreviousProvider(t *testing.T) {
	svc, classifier, _, ctx := handoffTestService()
	upstream := &handoffResponseProvider{}
	svc.clients = dispatch.NewClients(map[string]providers.Client{
		providers.ProviderAnthropic: upstream,
		providers.ProviderFireworks: upstream,
	})
	env, err := translate.ParseAnthropic([]byte(handoffTestBody))
	require.NoError(t, err)
	sessionKey := DeriveSessionKey(env, "handoff-test-key")
	pins := newForceModelMapStore()
	pins.pins = map[string]sessionpin.Pin{
		forceModelMapKey(sessionKey, "default_mid"): {
			Model: "claude-fable-5-1", Provider: providers.ProviderAnthropic,
			Reason: translate.ReasonStruggleEscalation, PolicyGroup: "maximum",
			Strategy:    router.StrategyHMM,
			PinnedUntil: time.Now().Add(time.Hour), LastServedModel: "qwen/qwen3.8-max",
		},
		forceModelMapKey(sessionKey, hmmHistoryRole("default_mid")): {
			Model: "qwen/qwen3.8-max", Provider: providers.ProviderFireworks,
			Reason: hmmHistoryReason, PolicyGroup: "high", LastServedModel: "qwen/qwen3.8-max",
		},
	}
	svc.pinStore = pins
	prepared := prepareTestHandoff(t, svc, ctx)
	require.Equal(t, "claude-fable-5-1", prepared.Model)
	require.Equal(t, "maximum", prepared.Complexity)
	require.NotEmpty(t, prepared.SummaryToken)
	require.Zero(t, classifier.calls)
	require.Empty(t, upstream.body)

	summaryBody, err := sjson.Set(handoffTestBody, piHandoffField, prepared.SummaryToken)
	require.NoError(t, err)
	parsed, _, err := svc.parseHandoff(ctx, []byte(summaryBody))
	require.NoError(t, err)
	summary := handoffFromContext(parsed).Route.Decision
	require.Equal(t, "qwen/qwen3.8-max", summary.Model)
	require.Equal(t, providers.ProviderFireworks, summary.Provider)

	continuation, err := sjson.Set(handoffTestBody, "messages.0.content", "Compacted context; finish the review.")
	require.NoError(t, err)
	continuation, err = sjson.Set(continuation, piHandoffField, prepared.Token)
	require.NoError(t, err)
	continuation, err = sjson.Set(continuation, piSessionField, prepared.SessionToken)
	require.NoError(t, err)
	require.NoError(t, svc.ProxyMessages(ctx, []byte(continuation), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil)))
	require.Equal(t, "claude-fable-5-1", gjson.GetBytes(upstream.body, "model").String())
	require.False(t, gjson.GetBytes(upstream.body, piSessionField).Exists(), "session tickets must not reach a provider")
	require.Zero(t, classifier.calls)

	nextTurn, err := sjson.Delete(continuation, piHandoffField)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(WithHandoffPreparation(ctx), []byte(nextTurn), recorder, httptest.NewRequest("POST", "/v1/route/handoff", nil)))
	var next preparedHandoff
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &next))
	require.Equal(t, "claude-fable-5-1", next.Model, "compacted follow-up must find the original session pin")
	require.Equal(t, "maximum", next.Complexity)
	require.Empty(t, next.SummaryToken, "a same-model follow-up must not compact again")
	require.Zero(t, classifier.calls)
}

func TestPiHandoffRejectsTamperingIdentityAndExpiration(t *testing.T) {
	svc, _, upstream, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.Token)
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		ctx   context.Context
		token string
	}{
		{"other-key", context.WithValue(ctx, APIKeyIDContextKey{}, "another-key"), prepared.Token},
		{"other-installation", context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString()), prepared.Token},
		{"tampered", ctx, prepared.Token + "invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			altered, err := sjson.Set(body, piHandoffField, test.token)
			require.NoError(t, err)
			_, _, err = svc.parseHandoff(test.ctx, []byte(altered))
			require.ErrorIs(t, err, ErrHandoffInvalid)
		})
	}
	parsed, _, err := svc.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	claims := handoffFromContext(parsed)
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(svc.piHandoffSecret)
	require.NoError(t, err)
	body, err = sjson.Set(body, piHandoffField, expired)
	require.NoError(t, err)
	_, _, err = svc.parseHandoff(ctx, []byte(body))
	require.ErrorIs(t, err, ErrHandoffInvalid)
	require.Empty(t, upstream.body)
}

func TestPiHandoffRevalidatesSessionConfigurationAndEligibility(t *testing.T) {
	svc, _, _, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.Token)
	require.NoError(t, err)
	parsed, clean, err := svc.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(clean)
	require.NoError(t, err)
	_, req, err := svc.anthropicRoutingRequest(parsed, clean, nil, "handoff-test")
	require.NoError(t, err)
	_, err = svc.resumeHandoff(parsed, env, req)
	require.NoError(t, err)
	for _, mutation := range []struct {
		name  string
		apply func(*router.Request)
	}{
		{"tools", func(r *router.Request) { r.ToolConfigurationSHA256 = "changed" }},
		{"reasoning", func(r *router.Request) { r.ReasoningConfigurationSHA256 = "changed" }},
		{"model", func(r *router.Request) { r.RequestedModel = "claude-haiku-4-5" }},
		{"excluded", func(r *router.Request) { r.ExcludedModels = map[string]struct{}{prepared.Model: {}} }},
		{"provider", func(r *router.Request) { r.EnabledProviders = map[string]struct{}{} }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := req
			mutation.apply(&changed)
			_, err := svc.resumeHandoff(parsed, env, changed)
			require.ErrorIs(t, err, ErrHandoffInvalid)
		})
	}
	otherSession, err := translate.ParseAnthropic([]byte(strings.ReplaceAll(string(clean), "handoff-test", "other-session")))
	require.NoError(t, err)
	_, err = svc.resumeHandoff(parsed, otherSession, req)
	require.ErrorIs(t, err, ErrHandoffInvalid)
}

func TestPiHandoffDisabledAndUtilityTurnsDoNotDispatch(t *testing.T) {
	svc, classifier, upstream, ctx := handoffTestService()
	for _, prompt := range []string{"/beta", "/force-model claude-opus-4-7", "/unforce-model"} {
		body, err := sjson.Set(handoffTestBody, "messages.0.content", prompt)
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		require.NoError(t, svc.ProxyMessages(WithHandoffPreparation(ctx), []byte(body), recorder, httptest.NewRequest("POST", "/v1/route/handoff", nil)))
		require.True(t, gjson.GetBytes(recorder.Body.Bytes(), "bypass").Bool())
	}
	svc.piHandoffSecret = nil
	err := svc.ProxyMessages(WithHandoffPreparation(ctx), []byte(handoffTestBody), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/route/handoff", nil))
	require.ErrorIs(t, err, ErrHandoffUnavailable)
	require.Zero(t, classifier.calls)
	require.Empty(t, upstream.body)
}

func TestPiHandoffSummaryUsesPreviousModelWithoutReclassifyingOrWritingUsageToItsPin(t *testing.T) {
	svc, classifier, upstream, ctx := handoffTestService()
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{
		Model: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic,
		Reason: "hmm_policy", PolicyGroup: "low", PinnedUntil: time.Now().Add(time.Hour),
		LastServedModel: "claude-sonnet-4-6:high", LastTurnEndedAt: time.Now().Add(-time.Minute),
	}
	svc.pinStore = store
	prepared := prepareTestHandoff(t, svc, ctx)
	require.NotEmpty(t, prepared.SummaryToken)
	pinWrites := len(store.upserts)
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.SummaryToken)
	require.NoError(t, err)
	parsed, _, err := svc.parseHandoff(ctx, []byte(body))
	require.NoError(t, err)
	require.Equal(t, inference.PurposeClientCompaction, handoffFromContext(parsed).Route.Purpose)
	require.Equal(t, policy.OverrideSourceSession, handoffFromContext(parsed).Route.Origin)
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil)))
	require.Equal(t, "claude-sonnet-4-6", gjson.GetBytes(upstream.body, "model").String())
	require.Equal(t, 1, classifier.calls)
	require.Equal(t, pinWrites, len(store.upserts))
	require.Zero(t, store.usageHits)
}

func TestPiHandoffRecordsEscalationOutcomeOnlyAfterContinuation(t *testing.T) {
	store := newEscalationTestStore()
	observer := &escalationTestObserver{}
	upstream := &betaCaptureProvider{}
	svc := newEscalationCompletionService(store, observer, map[string]providers.Client{providers.ProviderAnthropic: upstream}).
		WithPiHandoffSecret(strings.Repeat("test-secret-", 4))
	ctx := escalationCompletionContext()
	prepared := prepareTestHandoff(t, svc, ctx)
	require.Len(t, observer.requests, 1)
	require.Empty(t, upstream.body)
	require.Len(t, store.sessions, 1)
	for _, session := range store.sessions {
		require.Nil(t, session.PreviousOutcome, "preparation has no inference outcome")
	}
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.Token)
	require.NoError(t, err)
	require.NoError(t, svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil)))
	require.Len(t, observer.requests, 1)
	for _, session := range store.sessions {
		require.NotNil(t, session.PreviousOutcome)
		require.False(t, session.PreviousOutcome.IsError)
	}
}

func TestPiHandoffRejectsConcurrentForceAndCrossSessionCommandsBeforeMutation(t *testing.T) {
	svc, _, upstream, ctx := handoffTestService()
	prepared := prepareTestHandoff(t, svc, ctx)
	body, err := sjson.Set(handoffTestBody, piHandoffField, prepared.Token)
	require.NoError(t, err)
	store := newStubPinStore()
	store.getFound = true
	store.getPin = sessionpin.Pin{Model: "claude-haiku-4-5", Provider: providers.ProviderAnthropic, Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour)}
	svc.pinStore = store
	err = svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))
	require.ErrorIs(t, err, ErrHandoffInvalid)
	for _, prompt := range []string{"/beta", "/force-model claude-opus-4-7", "/unforce-model"} {
		changed, err := sjson.Set(body, "messages.0.content", prompt)
		require.NoError(t, err)
		err = svc.ProxyMessages(ctx, []byte(changed), httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/messages", nil))
		require.ErrorIs(t, err, ErrHandoffInvalid)
	}
	require.Empty(t, upstream.body)
	require.Empty(t, store.upserts)
}
