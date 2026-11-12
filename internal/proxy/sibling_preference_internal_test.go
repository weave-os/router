package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/escalation"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

type siblingPreferenceRecordingStore struct {
	stubPinStore
	preferences map[[sessionpin.SessionKeyLen]byte]map[escalation.Group]router.SiblingPreference
	readErr     error
	reads       int
}

func (s *siblingPreferenceRecordingStore) GetSiblingPreferences(_ context.Context, _ uuid.UUID, key [sessionpin.SessionKeyLen]byte, _ router.Strategy) (map[escalation.Group]router.SiblingPreference, error) {
	s.reads++
	return s.preferences[key], s.readErr
}

func (s *siblingPreferenceRecordingStore) RecordSiblingPreference(_ context.Context, _ uuid.UUID, key [sessionpin.SessionKeyLen]byte, _ router.Strategy, group escalation.Group, preferred string, displaced []string, startedAt time.Time) (bool, error) {
	if s.preferences == nil {
		s.preferences = make(map[[sessionpin.SessionKeyLen]byte]map[escalation.Group]router.SiblingPreference)
	}
	if s.preferences[key] == nil {
		s.preferences[key] = make(map[escalation.Group]router.SiblingPreference)
	}
	previous := s.preferences[key][group]
	for _, model := range displaced {
		if !slices.Contains(previous.DisplacedModels, model) {
			previous.DisplacedModels = append(previous.DisplacedModels, model)
		}
	}
	previous.DisplacedModels = slices.DeleteFunc(previous.DisplacedModels, func(model string) bool { return model == preferred })
	previous.Model = preferred
	previous.StartedAt = startedAt
	s.preferences[key][group] = previous
	return true, nil
}

func (*siblingPreferenceRecordingStore) ClearSiblingWinnerExclusions(context.Context, [sessionpin.SessionKeyLen]byte, router.Strategy, []string, string, time.Time) error {
	return nil
}

type siblingSequenceStore struct {
	siblingPreferenceRecordingStore
	pins map[string]sessionpin.Pin
}

func (s *siblingSequenceStore) Get(_ context.Context, _ [sessionpin.SessionKeyLen]byte, role string) (sessionpin.Pin, bool, error) {
	pin, ok := s.pins[role]
	return pin, ok, nil
}
func (s *siblingSequenceStore) Upsert(_ context.Context, pin sessionpin.Pin) error {
	s.pins[pin.Role] = pin
	return nil
}

type siblingSequenceDecider struct{}

func (siblingSequenceDecider) Decide(context.Context, policy.Query) (policy.Result, error) {
	return policy.Result{SchemaVersion: policy.SchemaVersionV4}, nil
}

func TestSiblingFallbackPersistsAcrossClusterChanges(t *testing.T) {
	primary := catalog.ModelIDClaudeOpus48.String()
	sibling := "claude-sonnet-5"
	medium := catalog.ModelIDClaudeHaiku45.String()
	models := map[string]struct{}{primary: {}, sibling: {}, medium: {}}
	resolver := policy.NewResolver(models, map[string]struct{}{providers.ProviderAnthropic: {}},
		func(model catalog.Model) string { return "anthropic/" + model.ID }, policy.ManagedProviderPolicy())
	group := escalation.High
	policyRouter := policy.NewSidecarRouter(policy.SidecarRouterConfig{Strategy: router.StrategyHMM}, siblingSequenceDecider{}, resolver).
		WithArmSelector(func(context.Context, policy.SelectionInput) (policy.SelectionPick, error) {
			high := policy.PreviewGroup{Group: string(escalation.High), RosterArms: []string{"anthropic/" + primary, "anthropic/" + sibling}, EligibleArms: []string{"anthropic/" + primary, "anthropic/" + sibling}}
			mid := policy.PreviewGroup{Group: string(escalation.Medium), RosterArms: []string{"anthropic/" + medium}, EligibleArms: []string{"anthropic/" + medium}}
			if group == escalation.Medium {
				return policy.SelectionPick{Group: string(group), Arm: mid.EligibleArms[0], RankedFallback: []policy.PreviewGroup{mid, high}}, nil
			}
			return policy.SelectionPick{Group: string(group), Arm: high.EligibleArms[0], RankedFallback: []policy.PreviewGroup{high, mid}}, nil
		})
	store := &siblingSequenceStore{pins: make(map[string]sessionpin.Pin)}
	client := &siblingChainClient{failing: map[string]bool{primary: true}}
	svc := NewService(policyRouter, map[string]providers.Client{providers.ProviderAnthropic: client}, nil, false, nil, store, false, providers.ProviderAnthropic, medium, nil).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}}).
		WithRetrySleep(noopSleep)
	ctx := requestcontext.WithClientIdentity(rescuedFailureCtx(), ClientIdentity{SessionID: "synthetic-session"})
	body := `{"model":"claude-opus-4-8","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"start"}]}`
	serve := func() *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, req))
		return rec
	}
	first := serve()
	assert.Equal(t, sibling, first.Header().Get(HeaderRouterModel))
	failedCalls := len(client.failed)
	require.Positive(t, failedCalls)
	second := serve()
	assert.Equal(t, sibling, second.Header().Get(HeaderRouterModel))
	assert.Len(t, client.failed, failedCalls, "a later prompt must not retry the displaced model")
	group = escalation.Medium
	third := serve()
	assert.Equal(t, medium, third.Header().Get(HeaderRouterModel))
	group = escalation.High
	compaction := `{"model":"claude-opus-4-8","system":"Your task is to create a detailed summary","messages":[{"role":"user","content":"summarize"}]}`
	compactionRec := httptest.NewRecorder()
	require.NoError(t, svc.ProxyMessages(ctx, []byte(compaction), compactionRec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(compaction))))
	fourth := serve()
	assert.Equal(t, sibling, fourth.Header().Get(HeaderRouterModel))
	assert.Len(t, client.failed, failedCalls, "returning to High keeps the successful sibling")

	client.failing[sibling] = true
	client.failing[primary] = false
	fifth := serve()
	assert.Equal(t, primary, fifth.Header().Get(HeaderRouterModel), "the displaced model remains the last resort")
	failedCalls = len(client.failed)
	sixth := serve()
	assert.Equal(t, primary, sixth.Header().Get(HeaderRouterModel), "a successful last resort becomes the new preference")
	assert.Len(t, client.failed, failedCalls, "the failing replacement is not retried on the next prompt")

	client.failing[primary] = true
	failedCalls = len(client.failed)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	err := svc.ProxyMessages(ctx, []byte(body), httptest.NewRecorder(), req)
	require.Error(t, err, "an exhausted two-model roster must return an error")
	assert.LessOrEqual(t, len(client.failed)-failedCalls, 10, "the rescue walk remains bounded")
}

func TestSuccessfulSiblingIsRememberedOnBothIngresses(t *testing.T) {
	for _, openAI := range []bool{false, true} {
		for _, siblingFails := range []bool{false, true} {
			for _, crossGroup := range []bool{false, true} {
				store := &siblingPreferenceRecordingStore{}
				decision := rescuableDecision("cluster:v0.2", true)
				decision.Metadata.PolicyGroup = string(escalation.High)
				decision.Metadata.SiblingModels = []string{rescuedPrimaryModel}
				if !crossGroup {
					decision.Metadata.SiblingModels = append(decision.Metadata.SiblingModels, rescuerModel)
				}
				overload := &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}
				primary := &failingClient{err: overload}
				sibling := &servingClient{}
				if siblingFails {
					sibling.err = overload
				}
				svc := newRescuedFailureTurnService(store, decision, primary, sibling, false)
				body := anthropicMessagesBody()
				if openAI {
					body = openaiChatBody()
				}
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
				rec := httptest.NewRecorder()
				var err error
				if openAI {
					err = svc.ProxyOpenAIChatCompletion(rescuedFailureCtx(), body, rec, req)
				} else {
					err = svc.ProxyMessages(rescuedFailureCtx(), body, rec, req)
				}
				if siblingFails {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				if siblingFails || crossGroup {
					assert.Empty(t, store.preferences, "failed or cross-group rescues cannot replace a group's preference")
					continue
				}
				require.Len(t, store.preferences, 1)
				for _, preferences := range store.preferences {
					assert.Equal(t, rescuerModel, preferences[escalation.High].Model)
					assert.Equal(t, []string{rescuedPrimaryModel}, preferences[escalation.High].DisplacedModels)
				}
			}
		}
	}
}

func TestSiblingPreferenceReachesFreshRoutingAfterNewUserPrompt(t *testing.T) {
	preference := router.SiblingPreference{Model: freshTurnModel, DisplacedModels: []string{demotedPinModel}}
	pin := demotedPin(demotedPinModel)
	pin.PolicyGroup = string(escalation.High)
	store := &siblingSequenceStore{pins: map[string]sessionpin.Pin{sessionpin.DefaultRole: pin}}
	ctx := context.Background()
	env, _ := demotionTurnLoopEnv(t)
	threadKey := deriveSessionKeyForRequest(ctx, env, "api-key")
	key := deriveConversationSessionKeyForRequest(ctx, env, "api-key", threadKey, requestcontext.SiblingPreferenceConversationKey)
	store.preferences = map[[sessionpin.SessionKeyLen]byte]map[escalation.Group]router.SiblingPreference{key: {escalation.High: preference}}
	svc, scorer := cooldownTurnLoopService(store, false)
	runDemotionTurnLoop(t, svc, ctx)
	require.NotEmpty(t, scorer.requests, "the displaced automatic pin cannot bypass fresh routing")
	assert.Equal(t, preference, scorer.requests[0].SiblingPreferences[escalation.High])
	assert.False(t, automaticPinEligible(pin, scorer.requests[0]))
	assert.True(t, forcedPinEligible(pin, scorer.requests[0]), "explicit pins retain their existing eligibility")
}

func TestSiblingPreferenceStoreFailureStopsAutomaticRouting(t *testing.T) {
	store := &siblingPreferenceRecordingStore{readErr: errors.New("synthetic database outage")}
	primary := &failingClient{err: &providers.UpstreamErrorResponse{Status: http.StatusServiceUnavailable}}
	sibling := &servingClient{}
	svc := newRescuedFailureTurnService(store, rescuableDecision("cluster:v0.2", true), primary, sibling, false)
	body := anthropicMessagesBody()
	err := svc.ProxyMessages(rescuedFailureCtx(), body, httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body))))
	require.ErrorContains(t, err, "load session sibling preferences")
	assert.Zero(t, primary.calls)
	assert.Zero(t, sibling.calls)
}

func TestSiblingPreferencesDoNotBlockExplicitModels(t *testing.T) {
	for _, saved := range []bool{false, true} {
		store := &siblingSequenceStore{pins: make(map[string]sessionpin.Pin)}
		store.readErr = errors.New("synthetic preference outage")
		svc := newRescuedFailureTurnService(store, rescuableDecision("cluster:v0.2", true), &failingClient{}, &servingClient{}, false)
		env, features := demotionTurnLoopEnv(t)
		req := router.Request{RequestedModel: features.Model, EstimatedInputTokens: features.Tokens}
		if saved {
			store.pins[forceModelSessionRole] = sessionpin.Pin{Provider: providers.ProviderOpenAI, Model: rescuerModel, Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour)}
		} else {
			req.ForceModel = rescuerModel
		}
		res, err := svc.runTurnLoop(context.Background(), env, features, "synthetic-key", uuid.New(), "", http.Header{}, req)
		require.NoError(t, err)
		assert.Equal(t, rescuerModel, res.Decision.Model)
		assert.Zero(t, store.reads, "explicit model selection cannot depend on automatic sibling state")
	}
}

func TestUtilityTurnsDoNotConsultSiblingPreferences(t *testing.T) {
	for _, test := range []struct {
		turn turntype.TurnType
		body string
	}{
		{turntype.Probe, `{"model":"claude-opus-4-8","max_tokens":1,"messages":[{"role":"user","content":"probe"}]}`},
		{turntype.Compaction, `{"model":"claude-opus-4-8","system":"Your task is to create a detailed summary","messages":[{"role":"user","content":"summarize"}]}`},
		{turntype.TitleGen, `{"model":"claude-opus-4-8","output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}}},"messages":[{"role":"user","content":"title"}]}`},
		{turntype.Classifier, `{"model":"claude-opus-4-8","max_tokens":64,"messages":[{"role":"user","content":"classify"}]}`},
		{turntype.Recap, `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"The user stepped away and is coming back. Recap in under 30 words."}]}`},
	} {
		t.Run(string(test.turn), func(t *testing.T) {
			store := &siblingPreferenceRecordingStore{readErr: errors.New("synthetic preference outage")}
			svc := newRescuedFailureTurnService(store, rescuableDecision("cluster:v0.2", true), &failingClient{}, &servingClient{}, false)
			env, err := translate.ParseAnthropic([]byte(test.body))
			require.NoError(t, err)
			features := env.RoutingFeatures(false)
			res, err := svc.runTurnLoop(context.Background(), env, features, "synthetic-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: features.Model, EstimatedInputTokens: features.Tokens})
			require.NoError(t, err)
			assert.Equal(t, test.turn, res.TurnType)
			assert.Zero(t, store.reads)
			assert.Zero(t, res.SiblingPreferenceKey)
		})
	}
}

func TestPolicyPinDoesNotConsultSiblingPreferences(t *testing.T) {
	store := &siblingPreferenceRecordingStore{readErr: errors.New("synthetic preference outage")}
	decision := rescuableDecision("hmm_policy", true)
	decision.Metadata.PolicyPinHonoured = true
	svc := newRescuedFailureTurnService(store, decision, &failingClient{}, &servingClient{}, false)
	env, features := demotionTurnLoopEnv(t)
	res, err := svc.runTurnLoop(pinnedContext(true), env, features, "synthetic-key", uuid.New(), "", http.Header{}, router.Request{RequestedModel: features.Model, EstimatedInputTokens: features.Tokens})
	require.NoError(t, err)
	assert.Equal(t, decision.Model, res.Decision.Model)
	assert.Zero(t, store.reads)
	assert.Zero(t, res.SiblingPreferenceKey)
}
