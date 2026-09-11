package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/sessionstrategy"
	"weave-os/router/internal/translate"

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

type betaCaptureProvider struct {
	body []byte
}

func (p *betaCaptureProvider) Proxy(
	_ context.Context,
	_ router.Decision,
	req providers.PreparedRequest,
	_ http.ResponseWriter,
	_ *http.Request,
) error {
	p.body = append([]byte(nil), req.Body...)
	return nil
}

func (p *betaCaptureProvider) Passthrough(
	_ context.Context,
	req providers.PreparedRequest,
	_ http.ResponseWriter,
	_ *http.Request,
) error {
	p.body = append([]byte(nil), req.Body...)
	return nil
}

func (r *betaTestRouter) Route(_ context.Context, req router.Request) (router.Decision, error) {
	r.calls++
	r.requests = append(r.requests, req)
	return r.decision, nil
}

type betaTestPreferenceStore struct {
	mu          sync.Mutex
	preference  sessionstrategy.Preference
	found       bool
	toggles     int
	disables    int
	getErr      error
	toggleErr   error
	beforeWrite func()
}

func (s *betaTestPreferenceStore) Get(
	_ context.Context,
	installationID uuid.UUID,
	sessionKey [sessionstrategy.SessionKeyLen]byte,
) (sessionstrategy.Preference, bool, error) {
	s.mu.Lock()
	preference, found, err := s.preference, s.found, s.getErr
	s.mu.Unlock()
	if err != nil {
		return sessionstrategy.Preference{}, false, err
	}
	if !found || preference.InstallationID != installationID || preference.SessionKey != sessionKey {
		return sessionstrategy.Preference{}, false, nil
	}
	return preference, true, nil
}

func (s *betaTestPreferenceStore) Toggle(_ context.Context, preference sessionstrategy.Preference) (bool, error) {
	if err := preference.Validate(); err != nil {
		return false, err
	}
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toggleErr != nil {
		return false, s.toggleErr
	}
	s.toggles++
	if s.found {
		s.preference = sessionstrategy.Preference{}
		s.found = false
		return false, nil
	}
	s.preference = preference
	s.found = true
	return true, nil
}

func (s *betaTestPreferenceStore) Disable(
	_ context.Context,
	installationID uuid.UUID,
	sessionKey [sessionstrategy.SessionKeyLen]byte,
) (bool, error) {
	if s.beforeWrite != nil {
		s.beforeWrite()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toggleErr != nil {
		return false, s.toggleErr
	}
	s.disables++
	if !s.found || s.preference.InstallationID != installationID || s.preference.SessionKey != sessionKey {
		return false, nil
	}
	s.preference = sessionstrategy.Preference{}
	s.found = false
	return true, nil
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
			context.Background(), response, env, cmd, installationID, sessionKey, sessionKey, 1,
		))
		assert.Equal(t, "✦ **Weave Router** → "+want+"\n\n", gjson.Get(response.Body.String(), "content.0.text").String())
	}

	assert.Equal(t, 2, store.toggles)
	assert.False(t, store.found)
	require.NotEmpty(t, pins.consumedStrategy)
	assert.Equal(t, router.StrategyCluster, pins.consumedStrategy[0])
	assert.Equal(t, router.StrategyHMMBeta, pins.consumedStrategy[len(pins.consumedStrategy)-1])
}

func TestHandleBetaCommandAcceptsHeaderOnlyClientSession(t *testing.T) {
	store := &betaTestPreferenceStore{}
	svc := (&Service{pinStore: &betaCleanupPinStore{}}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: &betaTestRouter{}}).
		WithSessionStrategyStore(store)
	env := betaTestEnvelope(t, "/beta", false)
	cmd, found := env.ExtractBetaCommand()
	require.True(t, found)
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "codex-session"})

	response := httptest.NewRecorder()
	require.NoError(t, svc.handleBetaCommand(ctx, response, env, cmd, uuid.New(), sessionKey, sessionKey, 1))
	assert.Equal(t, "✦ **Weave Router** → "+betaEnabledMessage+"\n\n", gjson.Get(response.Body.String(), "content.0.text").String())
	assert.Equal(t, 1, store.toggles)
}

func TestHandleBetaCommandOverlappingTogglesAcknowledgeDistinctStates(t *testing.T) {
	store := &betaTestPreferenceStore{}
	var readers sync.WaitGroup
	readers.Add(2)
	store.beforeWrite = func() {
		readers.Done()
		readers.Wait()
	}
	svc := (&Service{}).
		WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: &betaTestRouter{}}).
		WithSessionStrategyStore(store)
	installationID := uuid.New()
	var sessionKey [sessionstrategy.SessionKeyLen]byte
	sessionKey[0] = 1

	acknowledgements := make(chan string, 2)
	var toggles sync.WaitGroup
	for range 2 {
		toggles.Add(1)
		go func() {
			defer toggles.Done()
			env := betaTestEnvelope(t, "/beta", true)
			cmd, found := env.ExtractBetaCommand()
			assert.True(t, found)
			response := httptest.NewRecorder()
			assert.NoError(t, svc.handleBetaCommand(
				context.Background(), response, env, cmd, installationID, sessionKey, sessionKey, 1,
			))
			acknowledgements <- gjson.Get(response.Body.String(), "content.0.text").String()
		}()
	}
	toggles.Wait()
	close(acknowledgements)

	var acked []string
	for text := range acknowledgements {
		acked = append(acked, text)
	}
	assert.ElementsMatch(t, []string{
		"✦ **Weave Router** → " + betaEnabledMessage + "\n\n",
		"✦ **Weave Router** → " + betaDisabledMessage + "\n\n",
	}, acked)
	assert.Equal(t, 2, store.toggles)
	assert.False(t, store.found, "two overlapping toggles from stable must land back on stable")
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
		context.Background(), response, env, cmd, uuid.New(), sessionKey, sessionKey, 1,
	))
	assert.Contains(t, gjson.Get(response.Body.String(), "content.0.text").String(), betaUsageMessage)
	assert.Zero(t, store.toggles)
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
				context.Background(), response, env, cmd, uuid.New(), sessionKey, sessionKey, 1,
			))
			assert.Contains(t, gjson.Get(response.Body.String(), "content.0.text").String(), betaUnavailable)
			assert.Zero(t, store.toggles)
		})
	}
}

func TestHandleBetaCommandWriteFailureLeavesStablePinsUntouched(t *testing.T) {
	store := &betaTestPreferenceStore{toggleErr: errors.New("write failed")}
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
		context.Background(), httptest.NewRecorder(), env, cmd, uuid.New(), sessionKey, sessionKey, 1,
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
		context.Background(), response, env, cmd, installationID, sessionKey, sessionKey, 1,
	))
	assert.False(t, store.found)
	assert.Equal(t, 1, store.disables)
	assert.Zero(t, store.toggles, "an unavailable policy must never flip the preference")
	assert.Contains(t, gjson.Get(response.Body.String(), "content.0.text").String(), betaDisabledMessage)
}

func TestHandleBetaCommandOverlappingTogglesCannotReenableUnavailableBeta(t *testing.T) {
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
	var arrivals sync.WaitGroup
	arrivals.Add(2)
	store.beforeWrite = func() {
		arrivals.Done()
		arrivals.Wait()
	}
	svc := (&Service{}).WithSessionStrategyStore(store)

	acknowledgements := make(chan string, 2)
	var commands sync.WaitGroup
	for range 2 {
		commands.Add(1)
		go func() {
			defer commands.Done()
			env := betaTestEnvelope(t, "/beta", true)
			cmd, found := env.ExtractBetaCommand()
			assert.True(t, found)
			response := httptest.NewRecorder()
			assert.NoError(t, svc.handleBetaCommand(
				context.Background(), response, env, cmd, installationID, sessionKey, sessionKey, 1,
			))
			acknowledgements <- gjson.Get(response.Body.String(), "content.0.text").String()
		}()
	}
	commands.Wait()
	close(acknowledgements)

	var acked []string
	for text := range acknowledgements {
		acked = append(acked, text)
	}
	assert.ElementsMatch(t, []string{
		"✦ **Weave Router** → " + betaDisabledMessage + "\n\n",
		"✦ **Weave Router** → " + betaUnavailable + "\n\n",
	}, acked)
	assert.False(t, store.found, "beta must stay off while its policy is unavailable")
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
		baseCtx, httptest.NewRecorder(), enableEnv, enableCmd, installationID, sessionKey, sessionKey, 1,
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
		baseCtx, httptest.NewRecorder(), disableEnv, disableCmd, installationID, sessionKey, sessionKey, 1,
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

func TestHistoricalBetaArtifactsStripStaleThinkingSignatures(t *testing.T) {
	for _, tt := range []struct {
		name string
		ack  string
	}{
		{name: "after enabling beta", ack: "✦ **Weave Router** → Beta enabled. Type /beta again to turn it off.\n\n"},
		{name: "after restoring stable", ack: "✦ **Weave Router** → Beta disabled. Stable routing restored.\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			routing := &betaTestRouter{decision: router.Decision{
				Provider: providers.ProviderAnthropic,
				Model:    "claude-opus-4-7",
				Reason:   "test",
			}}
			provider := &betaCaptureProvider{}
			svc := NewService(
				routing,
				map[string]providers.Client{providers.ProviderAnthropic: provider},
				nil, false, nil, nil, false,
				providers.ProviderAnthropic, "claude-opus-4-7", nil,
			)
			body := []byte(`{
				"model":"claude-opus-4-7",
				"messages":[
					{"role":"user","content":"inspect this repository"},
					{"role":"assistant","content":[
						{"type":"thinking","thinking":"old thought","signature":"stale-signature"},
						{"type":"text","text":"I will inspect it."}
					]},
					{"role":"user","content":"/beta"},
					{"role":"assistant","content":` + mustJSONQuote(t, tt.ack) + `},
					{"role":"user","content":"continue with the implementation"}
				],
				"max_tokens":4096,
				"thinking":{"type":"adaptive"}
			}`)
			request := httptest.NewRequest("POST", "/v1/messages", nil)
			response := httptest.NewRecorder()

			require.NoError(t, svc.ProxyMessages(context.Background(), body, response, request))
			require.NotEmpty(t, provider.body)
			assert.NotContains(t, string(provider.body), "stale-signature")
			assert.NotContains(t, string(provider.body), `"type":"thinking"`)
			assert.NotContains(t, string(provider.body), "/beta")
			assert.Contains(t, string(provider.body), "continue with the implementation")
		})
	}
}

func TestBetaPreferenceSurvivesFirstMessageRewriteWithinClientSession(t *testing.T) {
	for _, tt := range []struct {
		name   string
		openAI bool
	}{
		{name: "anthropic"},
		{name: "openai", openAI: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			decisionProvider := providers.ProviderAnthropic
			decisionModel := "claude-haiku-4-5"
			if tt.openAI {
				decisionProvider = providers.ProviderOpenAI
				decisionModel = "gpt-5.5"
			}
			decision := router.Decision{Provider: decisionProvider, Model: decisionModel, Reason: "test"}
			stableRouter := &betaTestRouter{decision: decision}
			betaRouter := &betaTestRouter{decision: decision}
			svc := NewService(
				stableRouter,
				map[string]providers.Client{
					providers.ProviderAnthropic: embedTestProvider{},
					providers.ProviderOpenAI:    embedTestProvider{},
				},
				nil, false, nil, nil, false,
				providers.ProviderAnthropic, "claude-haiku-4-5", nil,
			).
				WithPolicyStrategy(policy.StrategySpec{Strategy: router.StrategyHMMBeta, Router: betaRouter}).
				WithSessionStrategyStore(&betaTestPreferenceStore{})
			ctx := context.WithValue(context.Background(), APIKeyIDContextKey{}, "beta-test-key")
			ctx = context.WithValue(ctx, InstallationIDContextKey{}, uuid.NewString())
			ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{SessionID: "conversation-1"})

			tools := []any{
				map[string]any{"name": "Read", "input_schema": map[string]any{"type": "object"}},
			}
			if tt.openAI {
				tools = []any{
					map[string]any{"type": "function", "function": map[string]any{"name": "Read", "parameters": map[string]any{"type": "object"}}},
				}
			}
			send := func(firstUserMessage string) {
				body, err := json.Marshal(map[string]any{
					"model":      "claude-sonnet-5",
					"messages":   []any{map[string]any{"role": "user", "content": firstUserMessage}},
					"tools":      tools,
					"max_tokens": 4096,
				})
				require.NoError(t, err)
				request := httptest.NewRequest("POST", "/v1/messages", nil)
				if tt.openAI {
					request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
					err = svc.ProxyOpenAIChatCompletion(ctx, body, httptest.NewRecorder(), request)
				} else {
					err = svc.ProxyMessages(ctx, body, httptest.NewRecorder(), request)
				}
				require.NoError(t, err)
			}

			send("/beta")
			send("This session is being continued from a previous conversation. Summary: ...")

			assert.Equal(t, 1, betaRouter.calls, "a compacted or sub-agent thread must keep the conversation's /beta preference")
			assert.Zero(t, stableRouter.calls)
		})
	}
}
