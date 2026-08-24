package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"workweave/router/internal/providers"
	"workweave/router/internal/router"
	"workweave/router/internal/router/policy"
	"workweave/router/internal/router/sessionpin"
	"workweave/router/internal/router/sessionstrategy"
	"workweave/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type betaTestRouter struct {
	calls    int
	requests []router.Request
	decision router.Decision
}

func (r *betaTestRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.calls++
	r.requests = append(r.requests, req)
	return r.decision, nil
}

type betaTestPreferenceStore struct {
	preference sessionstrategy.Preference
	found      bool
	sets       int
	clears     int
	getErr     error
	setErr     error
	clearErr   error
}

func (s *betaTestPreferenceStore) Get(
	_ context.Context,
	installationID uuid.UUID,
	sessionKey [sessionstrategy.SessionKeyLen]byte,
) (sessionstrategy.Preference, bool, error) {
	if s.getErr != nil {
		return sessionstrategy.Preference{}, false, s.getErr
	}
	if !s.found || s.preference.InstallationID != installationID || s.preference.SessionKey != sessionKey {
		return sessionstrategy.Preference{}, false, nil
	}
	return s.preference, true, nil
}

func (s *betaTestPreferenceStore) Set(_ context.Context, preference sessionstrategy.Preference) error {
	if s.setErr != nil {
		return s.setErr
	}
	s.preference = preference
	s.found = true
	s.sets++
	return nil
}

func (s *betaTestPreferenceStore) Clear(
	_ context.Context,
	installationID uuid.UUID,
	sessionKey [sessionstrategy.SessionKeyLen]byte,
) error {
	if s.clearErr != nil {
		return s.clearErr
	}
	if s.found && s.preference.InstallationID == installationID && s.preference.SessionKey == sessionKey {
		s.preference = sessionstrategy.Preference{}
		s.found = false
	}
	s.clears++
	return nil
}

type betaCleanupPinStore struct {
	consumeCalls     int
	consumedStrategy []router.Strategy
	upsertCalls      int
}

func (s *betaCleanupPinStore) Get(context.Context, [sessionpin.SessionKeyLen]byte, string) (sessionpin.Pin, bool, error) {
	return sessionpin.Pin{}, false, nil
}

func (s *betaCleanupPinStore) Consume(_ context.Context, _ [sessionpin.SessionKeyLen]byte, _ string, strategy router.Strategy) (sessionpin.Pin, bool, error) {
	s.consumeCalls++
	s.consumedStrategy = append(s.consumedStrategy, strategy)
	return sessionpin.Pin{}, false, nil
}

func (s *betaCleanupPinStore) Upsert(context.Context, sessionpin.Pin) error {
	s.upsertCalls++
	return nil
}
func (*betaCleanupPinStore) UpdateUsage(context.Context, [sessionpin.SessionKeyLen]byte, string, sessionpin.Usage) error {
	return nil
}
func (*betaCleanupPinStore) IncrementUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (*betaCleanupPinStore) ResetUpstreamErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (*betaCleanupPinStore) IncrementOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) (int, error) {
	return 0, nil
}
func (*betaCleanupPinStore) ResetOverloadErrors(context.Context, [sessionpin.SessionKeyLen]byte, string, router.Strategy) error {
	return nil
}
func (*betaCleanupPinStore) DisableProvider(context.Context, [sessionpin.SessionKeyLen]byte, string, string, router.Strategy) error {
	return nil
}
func (*betaCleanupPinStore) SweepExpired(context.Context) error { return nil }

func betaTestEnvelope(t *testing.T, text string, withSession bool) *translate.RequestEnvelope {
	t.Helper()
	metadata := ""
	if withSession {
		metadata = `,"metadata":{"user_id":"user_account__session_4dbee464-ebf7-437f-9f20-db5a6f7fe3b4"}`
	}
	env, err := translate.ParseAnthropic([]byte(
		`{"model":"claude-sonnet-5","messages":[{"role":"user","content":` +
			mustJSONQuote(t, text) + `}],"max_tokens":128` + metadata + `}`,
	))
	require.NoError(t, err)
	return env
}

func mustJSONQuote(t *testing.T, value string) string {
	t.Helper()
	quoted, err := json.Marshal(value)
	require.NoError(t, err)
	return string(quoted)
}

func TestHandleBetaCommandTogglesAndAcknowledges(t *testing.T) {
	store := &betaTestPreferenceStore{}
	pins := &betaCleanupPinStore{}
	svc := (&Service{pinStore: pins}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: &betaTestRouter{}}).
		WithSessionStrategyStore(store)
	installationID := uuid.New()
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1

	for _, want := range []string{betaEnabledMessage, betaDisabledMessage} {
		env := betaTestEnvelope(t, "/beta", true)
		cmd, found := env.ExtractBetaCommand()
		require.True(t, found)
		response := httptest.NewRecorder()
		require.NoError(t, svc.handleBetaCommand(
			context.Background(), response, env, cmd, installationID, sessionKey, 1,
		))
		assert.Equal(t, "✦ **Weave Router** → "+want+"\n\n", gjson.Get(response.Body.String(), "content.0.text").String())
	}

	assert.Equal(t, 1, store.sets)
	assert.Equal(t, 1, store.clears)
	assert.False(t, store.found)
	require.NotEmpty(t, pins.consumedStrategy)
	assert.Equal(t, router.StrategyCluster, pins.consumedStrategy[0])
	assert.Equal(t, router.StrategyHMMBeta, pins.consumedStrategy[len(pins.consumedStrategy)-1])
}

func TestHandleBetaCommandRejectsArgumentsWithoutStateChange(t *testing.T) {
	store := &betaTestPreferenceStore{}
	svc := (&Service{}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: &betaTestRouter{}}).
		WithSessionStrategyStore(store)
	env := betaTestEnvelope(t, "/beta status", true)
	cmd, found := env.ExtractBetaCommand()
	require.True(t, found)
	response := httptest.NewRecorder()
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1

	require.NoError(t, svc.handleBetaCommand(
		context.Background(), response, env, cmd, uuid.New(), sessionKey, 1,
	))
	assert.Contains(t, gjson.Get(response.Body.String(), "content.0.text").String(), betaUsageMessage)
	assert.Zero(t, store.sets)
	assert.Zero(t, store.clears)
}

func TestHandleBetaCommandRequiresClientSessionAndAvailablePolicy(t *testing.T) {
	for _, tt := range []struct {
		name        string
		withSession bool
		withPolicy  bool
	}{
		{name: "missing client session", withPolicy: true},
		{name: "missing beta policy", withSession: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &betaTestPreferenceStore{}
			svc := (&Service{}).WithSessionStrategyStore(store)
			if tt.withPolicy {
				svc.WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: &betaTestRouter{}})
			}
			env := betaTestEnvelope(t, "/beta", tt.withSession)
			cmd, found := env.ExtractBetaCommand()
			require.True(t, found)
			response := httptest.NewRecorder()
			var sessionKey [sessionstrategy.SessionKeyLen]byte
			sessionKey[0] = 1

			require.NoError(t, svc.handleBetaCommand(
				context.Background(), response, env, cmd, uuid.New(), sessionKey, 1,
			))
			assert.Contains(t, gjson.Get(response.Body.String(), "content.0.text").String(), betaUnavailable)
			assert.Zero(t, store.sets)
		})
	}
}

func TestHandleBetaCommandWriteFailureLeavesStablePinsUntouched(t *testing.T) {
	store := &betaTestPreferenceStore{setErr: errors.New("write failed")}
	pins := &betaCleanupPinStore{}
	svc := (&Service{pinStore: pins}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: &betaTestRouter{}}).
		WithSessionStrategyStore(store)
	env := betaTestEnvelope(t, "/beta", true)
	cmd, found := env.ExtractBetaCommand()
	require.True(t, found)
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1

	err := svc.handleBetaCommand(
		context.Background(), httptest.NewRecorder(), env, cmd, uuid.New(), sessionKey, 1,
	)

	require.Error(t, err)
	assert.False(t, store.found)
	assert.Zero(t, pins.consumeCalls, "failed enable must not disturb stable routing state")
}

func TestHandleBetaCommandCanDisablePersistedBetaWhilePolicyUnavailable(t *testing.T) {
	installationID := uuid.New()
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1
	store := &betaTestPreferenceStore{
		preference: sessionstrategy.Preference{
			InstallationID: installationID,
			SessionKey:     sessionKey,
			Strategy:       router.StrategyHMMBeta,
		},
		found: true,
	}
	svc := (&Service{}).WithSessionStrategyStore(store)
	env := betaTestEnvelope(t, "/beta", true)
	cmd, found := env.ExtractBetaCommand()
	require.True(t, found)
	response := httptest.NewRecorder()

	require.NoError(t, svc.handleBetaCommand(
		context.Background(), response, env, cmd, installationID, sessionKey, 1,
	))
	assert.False(t, store.found)
	assert.Equal(t, 1, store.clears)
	assert.Contains(t, gjson.Get(response.Body.String(), "content.0.text").String(), betaDisabledMessage)
}

func TestApplySessionStrategyOnlyUsesPersistedPreference(t *testing.T) {
	installationID := uuid.New()
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1
	stableCtx := router.WithStrategy(context.Background(), router.StrategyHMM)
	store := &betaTestPreferenceStore{}
	svc := (&Service{}).WithSessionStrategyStore(store)

	ctx, err := svc.applySessionStrategy(stableCtx, installationID, sessionKey)
	require.NoError(t, err)
	assert.Equal(t, router.StrategyHMM, router.StrategyFromContext(ctx))

	store.preference = sessionstrategy.Preference{
		InstallationID: installationID,
		SessionKey:     sessionKey,
		Strategy:       router.StrategyHMMBeta,
	}
	store.found = true
	ctx, err = svc.applySessionStrategy(stableCtx, installationID, sessionKey)
	require.NoError(t, err)
	assert.Equal(t, router.StrategyHMMBeta, router.StrategyFromContext(ctx))
}

func TestApplySessionStrategyReturnsOriginalContextOnStoreError(t *testing.T) {
	marker := struct{}{}
	ctx := context.WithValue(context.Background(), marker, "kept")
	svc := (&Service{}).WithSessionStrategyStore(&betaTestPreferenceStore{getErr: errors.New("db down")})
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1

	got, err := svc.applySessionStrategy(ctx, uuid.New(), sessionKey)

	require.Error(t, err)
	assert.Same(t, ctx, got)
	assert.Equal(t, "kept", got.Value(marker))
}

func TestPersistedBetaPreferenceFailsClosedWhenBetaRouterUnavailable(t *testing.T) {
	installationID := uuid.New()
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1
	store := &betaTestPreferenceStore{
		preference: sessionstrategy.Preference{
			InstallationID: installationID,
			SessionKey:     sessionKey,
			Strategy:       router.StrategyHMMBeta,
		},
		found: true,
	}
	svc := (&Service{}).
		WithPolicyStrategy(policy.StrategySpec{
			Strategy:    router.StrategyHMMBeta,
			Router:      nil,
			Unavailable: errors.New("beta unavailable"),
		}).
		WithSessionStrategyStore(store)

	ctx, err := svc.applySessionStrategy(context.Background(), installationID, sessionKey)
	require.NoError(t, err)
	assert.Equal(t, router.StrategyHMMBeta, router.StrategyFromContext(ctx))

	_, err = svc.routeFor(ctx, router.Request{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "beta unavailable")
}

func TestProxyEntrypointsInterceptBetaWithoutRoutingUpstream(t *testing.T) {
	for _, tt := range []struct {
		name   string
		openAI bool
	}{
		{name: "anthropic"},
		{name: "openai", openAI: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stableRouter := &betaTestRouter{}
			betaRouter := &betaTestRouter{}
			store := &betaTestPreferenceStore{}
			pins := &betaCleanupPinStore{}
			svc := NewService(stableRouter, nil, nil, false, nil, pins, false, "", "", nil).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: betaRouter}).
				WithSessionStrategyStore(store)
			ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "beta-test-key")
			ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
			body := []byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"/beta"}],"max_tokens":128,"metadata":{"user_id":"user_account__session_4dbee464-ebf7-437f-9f20-db5a6f7fe3b4"}}`)
			request := httptest.NewRequest("POST", "/v1/messages", nil)
			response := httptest.NewRecorder()

			var err error
			if tt.openAI {
				request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
				err = svc.ProxyOpenAIChatCompletion(ctx, body, response, request)
			} else {
				err = svc.ProxyMessages(ctx, body, response, request)
			}
			require.NoError(t, err)
			if tt.openAI {
				assert.Contains(t, gjson.Get(response.Body.String(), "choices.0.message.content").String(), betaEnabledMessage)
			} else {
				assert.Contains(t, gjson.Get(response.Body.String(), "content.0.text").String(), betaEnabledMessage)
			}
			assert.True(t, store.found)
			assert.Zero(t, stableRouter.calls)
			assert.Zero(t, betaRouter.calls)
			assert.Zero(t, pins.upsertCalls, "/beta must not grant a continuation or write a routing pin")
		})
	}
}

func TestBetaToggleSelectsBetaThenRestoresStableRouter(t *testing.T) {
	stableRouter := &betaTestRouter{}
	betaRouter := &betaTestRouter{}
	store := &betaTestPreferenceStore{}
	svc := NewService(stableRouter, nil, nil, false, nil, nil, false, "", "", nil).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: betaRouter}).
		WithSessionStrategyStore(store)
	installationID := uuid.New()
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1
	baseCtx := router.WithStrategy(context.Background(), router.StrategyCluster)

	enableEnv := betaTestEnvelope(t, "/beta", true)
	enableCmd, found := enableEnv.ExtractBetaCommand()
	require.True(t, found)
	require.NoError(t, svc.handleBetaCommand(
		baseCtx, httptest.NewRecorder(), enableEnv, enableCmd, installationID, sessionKey, 1,
	))
	betaCtx, err := svc.applySessionStrategy(baseCtx, installationID, sessionKey)
	require.NoError(t, err)
	_, err = svc.routeFor(betaCtx, router.Request{})
	require.NoError(t, err)
	assert.Equal(t, 1, betaRouter.calls)
	assert.Zero(t, stableRouter.calls)

	disableEnv := betaTestEnvelope(t, "/beta", true)
	disableCmd, found := disableEnv.ExtractBetaCommand()
	require.True(t, found)
	require.NoError(t, svc.handleBetaCommand(
		baseCtx, httptest.NewRecorder(), disableEnv, disableCmd, installationID, sessionKey, 1,
	))
	stableCtx, err := svc.applySessionStrategy(baseCtx, installationID, sessionKey)
	require.NoError(t, err)
	_, err = svc.routeFor(stableCtx, router.Request{})
	require.NoError(t, err)
	assert.Equal(t, 1, betaRouter.calls)
	assert.Equal(t, 1, stableRouter.calls)
}

func TestProxyEntrypointsStripHistoricalBetaArtifactsBeforeRouting(t *testing.T) {
	for _, tt := range []struct {
		name   string
		openAI bool
		tools  []any
	}{
		{
			name: "anthropic",
			tools: []any{
				map[string]any{"name": "Read", "input_schema": map[string]any{"type": "object"}},
			},
		},
		{
			name:   "openai",
			openAI: true,
			tools: []any{
				map[string]any{"type": "function", "function": map[string]any{"name": "Read", "parameters": map[string]any{"type": "object"}}},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decisionProvider := providers.ProviderAnthropic
			decisionModel := "claude-haiku-4-5"
			if tt.openAI {
				decisionProvider = providers.ProviderOpenAI
				decisionModel = "gpt-5.5"
			}
			routing := &betaTestRouter{decision: router.Decision{
				Provider: decisionProvider,
				Model:    decisionModel,
				Reason:   "test",
			}}
			svc := NewService(
				routing,
				map[string]providers.Client{
					providers.ProviderAnthropic: embedTestProvider{},
					providers.ProviderOpenAI:    embedTestProvider{},
				},
				nil, false, nil, nil, false,
				providers.ProviderAnthropic, "claude-haiku-4-5", nil,
			)
			body, err := json.Marshal(map[string]any{
				"model": "claude-opus-4-8",
				"messages": []any{
					map[string]any{"role": "user", "content": "inspect this repository"},
					map[string]any{"role": "assistant", "content": "I will inspect it."},
					map[string]any{"role": "user", "content": "/beta"},
					map[string]any{"role": "assistant", "content": "✦ **Weave Router** → Beta enabled. Type /beta again to turn it off.\n\n"},
					map[string]any{"role": "user", "content": "continue with the implementation"},
				},
				"tools":      tt.tools,
				"max_tokens": 4096,
			})
			require.NoError(t, err)
			request := httptest.NewRequest("POST", "/v1/messages", nil)
			response := httptest.NewRecorder()
			if tt.openAI {
				request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
				err = svc.ProxyOpenAIChatCompletion(context.Background(), body, response, request)
			} else {
				err = svc.ProxyMessages(context.Background(), body, response, request)
			}
			require.NoError(t, err)
			require.Len(t, routing.requests, 1)
			assert.NotContains(t, routing.requests[0].PromptText, "/beta")
			assert.NotContains(t, routing.requests[0].PromptText, "Beta enabled")
			assert.Contains(t, routing.requests[0].PromptText, "continue with the implementation")
			assert.Len(t, routing.requests[0].ConversationMessages, 3,
				"the command and acknowledgement must both be absent from routing features")
		})
	}
}
