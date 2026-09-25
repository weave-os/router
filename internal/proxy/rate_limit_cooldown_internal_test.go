package proxy

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cooldownCall is one recorded ExpireAndCoolDownModel invocation.
type cooldownCall struct {
	role   string
	model  string
	until  time.Time
	reason sessionpin.DemotionReason
}

// cooldownStubPinStore is demotionStubPinStore plus the cooldown write.
type cooldownStubPinStore struct {
	demotionStubPinStore
	mu        sync.Mutex
	cooldowns []cooldownCall
}

func (s *cooldownStubPinStore) ExpireAndCoolDownModel(_ context.Context, expired sessionpin.Pin, model string, until time.Time, reason sessionpin.DemotionReason) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cooldowns = append(s.cooldowns, cooldownCall{role: expired.Role, model: model, until: until, reason: reason})
	return nil
}

var _ sessionpin.CooldownStore = (*cooldownStubPinStore)(nil)

var rateLimitTestNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func newRateLimitTestService(store sessionpin.Store, transient bool, cooldownSeconds int) *Service {
	svc := newRescuedDemotionTestService(store, true).WithTransientRateLimit(transient, cooldownSeconds)
	svc.now = func() time.Time { return rateLimitTestNow }
	return svc
}

func upstream429(retryAfter string) *providers.UpstreamErrorResponse {
	err := &providers.UpstreamErrorResponse{Status: http.StatusTooManyRequests, Headers: http.Header{}}
	if retryAfter != "" {
		err.Headers.Set("Retry-After", retryAfter)
	}
	return err
}

func strikeRescuedPrimary(svc *Service, ctx context.Context, err error) (string, sessionpin.DemotionReason) {
	return svc.maybeStrikeArmAfterRescuedFailure(
		ctx,
		true,
		false,
		err,
		rescuedPrimaryDecision("hmm:authoritative model=claude-opus-4-7"),
		uuid.New(),
		nonZeroSessionKey(),
		sessionpin.DefaultRole, sessionpin.DefaultRole,
	)
}

// A rescued 429 under the flag is a cooldown on both rows the next turn
// merges, expiring one cooldown after now; the session-lifetime write is not
// made.
func TestRescued429_CoolsDownInsteadOfDemoting(t *testing.T) {
	store := &cooldownStubPinStore{}
	svc := newRateLimitTestService(store, true, 45)
	ctx, turn := withRateLimitTurn(context.Background())

	model, reason := strikeRescuedPrimary(svc, ctx, upstream429(""))

	assert.Equal(t, demotedArm, model)
	assert.Equal(t, sessionpin.DemotionReasonRateLimited, reason)
	assert.Empty(t, store.demotions, "a 429 must not demote for the session")
	until := rateLimitTestNow.Add(45 * time.Second)
	assert.Equal(t, []cooldownCall{
		{role: sessionpin.DefaultRole, model: demotedArm, until: until, reason: sessionpin.DemotionReasonRateLimited},
		{role: hmmHistoryRole(sessionpin.DefaultRole), model: demotedArm, until: until, reason: sessionpin.DemotionReasonRateLimited},
	}, store.cooldowns)

	fields := turn.completionLogFields()
	assert.Contains(t, fields, "demotion_expires_at")
	assert.Contains(t, fields, until.Format(time.RFC3339))
	assert.Contains(t, fields, int64(45_000))
	assert.Equal(t, []any{"arm_demoted", demotedArm, "arm_demotion_reason", "rate_limited", "rescued_arm_demoted", demotedArm},
		armStrikeLogFields("", model, reason))
}

// The per-org override on rate_limit_cooldown_seconds decides the expiry.
func TestRescued429_CooldownHonoursOrgOverride(t *testing.T) {
	store := &cooldownStubPinStore{}
	svc := newRateLimitTestService(store, true, 45)
	ctx := flags.WithOverrides(context.Background(), flags.Overrides{Ints: map[flags.Key]int{flags.KeyRateLimitCooldownSeconds: 120}})

	_, reason := strikeRescuedPrimary(svc, ctx, upstream429(""))

	assert.Equal(t, sessionpin.DemotionReasonRateLimited, reason)
	require.Len(t, store.cooldowns, 2)
	assert.Equal(t, rateLimitTestNow.Add(120*time.Second), store.cooldowns[0].until)
}

// Every rescued failure that is not a buffered 429 keeps the session-lifetime
// demotion under the flag.
func TestRescuedNon429_StaysPermanentUnderFlag(t *testing.T) {
	for _, err := range []error{
		&providers.UpstreamErrorResponse{Status: http.StatusBadGateway},
		&providers.UpstreamErrorResponse{Status: http.StatusInternalServerError},
		providers.ErrUpstreamIdleTimeout,
	} {
		store := &cooldownStubPinStore{}
		svc := newRateLimitTestService(store, true, 45)

		model, reason := strikeRescuedPrimary(svc, context.Background(), err)

		assert.Equal(t, demotedArm, model)
		assert.Equal(t, sessionpin.DemotionReasonRescuedFailure, reason)
		assert.Empty(t, store.cooldowns)
		require.Len(t, store.demotions, 2)
		assert.Equal(t, sessionpin.DemotionReasonRescuedFailure, store.demotions[0].reason)
	}
}

// Flag off, a rescued 429 is what it was: a session-lifetime demotion with
// the rescued_failure reason, no cooldown write, no cooldown ctx account, no
// extra completion fields.
func TestRescued429_FlagOffKeepsPermanentDemotion(t *testing.T) {
	store := &cooldownStubPinStore{}
	svc := newRateLimitTestService(store, false, 45)
	ctx, turn := svc.withRateLimitTurn(context.Background())
	assert.Nil(t, turn)
	assert.Nil(t, turn.completionLogFields())
	assert.Nil(t, svc.throttleRetryDelay(ctx))

	model, reason := strikeRescuedPrimary(svc, ctx, upstream429(""))

	assert.Equal(t, demotedArm, model)
	assert.Equal(t, sessionpin.DemotionReasonRescuedFailure, reason)
	assert.Empty(t, store.cooldowns)
	assert.Equal(t, []demotionCall{
		{role: sessionpin.DefaultRole, model: demotedArm, reason: sessionpin.DemotionReasonRescuedFailure},
		{role: hmmHistoryRole(sessionpin.DefaultRole), model: demotedArm, reason: sessionpin.DemotionReasonRescuedFailure},
	}, store.demotions)
	assert.Equal(t, armDemotionLogFields("", model), armStrikeLogFields("", model, reason))
}

// A store that cannot persist an expiry falls back to the session-lifetime
// strike rather than leaving the arm unstruck.
func TestRescued429_StoreWithoutCooldownsDemotesForSession(t *testing.T) {
	store := &demotionStubPinStore{}
	svc := newRateLimitTestService(store, true, 45)

	model, reason := strikeRescuedPrimary(svc, context.Background(), upstream429(""))

	assert.Equal(t, demotedArm, model)
	assert.Equal(t, sessionpin.DemotionReasonRateLimited, reason)
	require.Len(t, store.demotions, 2)
}

// A cooldown keeps the arm out of the pool until it expires and not a
// moment longer.
func TestActiveDemotionCooldowns_ExpiryBoundary(t *testing.T) {
	until := rateLimitTestNow.Add(45 * time.Second)
	cooldowns := map[string]time.Time{"claude-opus-5": until, "glm-5": rateLimitTestNow.Add(-time.Second)}

	assert.Equal(t, map[string]time.Time{"claude-opus-5": until}, activeDemotionCooldowns(cooldowns, rateLimitTestNow))
	assert.Equal(t, map[string]time.Time{"claude-opus-5": until}, activeDemotionCooldowns(cooldowns, until.Add(-time.Millisecond)))
	assert.Nil(t, activeDemotionCooldowns(cooldowns, until), "eligible again the instant the cooldown expires")
	assert.Nil(t, activeDemotionCooldowns(nil, rateLimitTestNow))
}

func TestMergeDemotionCooldowns_KeepsLaterExpiry(t *testing.T) {
	early := rateLimitTestNow.Add(10 * time.Second)
	late := rateLimitTestNow.Add(50 * time.Second)
	merged := mergeDemotionCooldowns(
		map[string]time.Time{"a": early, "b": early},
		map[string]time.Time{"a": late, "c": late},
	)
	assert.Equal(t, map[string]time.Time{"a": late, "b": early, "c": late}, merged)
	assert.Equal(t, []string{"b", "a", "c"}, cooldownsByExpiry(merged))
}

// A leftover cooldown never outranks a session-lifetime strike on the same
// model, and an image-bearing turn cannot readmit a text-only arm.
func TestReadmittableCooldowns_DropsPermanentAndImageUnsafeArms(t *testing.T) {
	until := rateLimitTestNow.Add(30 * time.Second)
	cooling := map[string]time.Time{
		"claude-opus-5":             until,
		"glm-5":                     until,
		"qwen/qwen3-235b-a22b-2507": until,
	}

	assert.Equal(t, map[string]time.Time{"claude-opus-5": until, "qwen/qwen3-235b-a22b-2507": until},
		readmittableCooldowns(cooling, []string{"glm-5"}, false))
	assert.Equal(t, map[string]time.Time{"claude-opus-5": until},
		readmittableCooldowns(cooling, []string{"glm-5"}, true))
	assert.Nil(t, readmittableCooldowns(map[string]time.Time{"glm-5": until}, []string{"glm-5"}, false))
}

// Exhausted pool: every candidate but the failed arm is cooling down, so the
// rescue readmits the cooling arms, the one that has cooled longest first,
// and never the arm that just 429'd. A session-lifetime demotion stays out.
// Exhaustion is recorded only when the walk actually dispatches one.
func TestRescueDecisions_ExhaustedPoolReadmitsCoolingArms(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderFireworks)
	md := &router.RoutingMetadata{
		CandidateModels: []string{"claude-opus-5", "claude-sonnet-5", "deepseek/deepseek-v4-pro", "claude-haiku-4-5"},
		CandidateProviders: map[string]string{
			"claude-sonnet-5":          providers.ProviderAnthropic,
			"deepseek/deepseek-v4-pro": providers.ProviderFireworks,
			"claude-haiku-4-5":         providers.ProviderAnthropic,
		},
	}
	cooling := map[string]time.Time{
		"claude-sonnet-5":          rateLimitTestNow.Add(40 * time.Second),
		"deepseek/deepseek-v4-pro": rateLimitTestNow.Add(5 * time.Second),
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, []string{"claude-sonnet-5", "deepseek/deepseek-v4-pro", "claude-haiku-4-5"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, cooling)
	ctx, turn := withRateLimitTurn(ctx)

	got := s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)

	assert.Equal(t, []string{"deepseek/deepseek-v4-pro", "claude-sonnet-5"}, siblingModels(got),
		"soonest-to-recover first; the permanently demoted haiku and the failed opus stay out")
	assert.NotContains(t, turn.completionLogFields(), true, "nothing dispatched yet")

	s.noteRescueReadmission(ctx, overloadedDecision(md), got[0])
	fields := turn.completionLogFields()
	assert.Contains(t, fields, "rescue_pool_exhausted")
	assert.Contains(t, fields, true)
	assert.Contains(t, fields, []string{"deepseek/deepseek-v4-pro"})
}

// A cooling arm ranks behind every eligible candidate: the walk reaches it
// only after they all failed, and dispatching the eligible one records no
// exhaustion.
func TestRescueDecisions_CoolingArmRanksBehindEligibleCandidates(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderFireworks)
	md := &router.RoutingMetadata{
		CandidateModels: []string{"claude-opus-5", "deepseek/deepseek-v4-pro", "claude-sonnet-5"},
		CandidateProviders: map[string]string{
			"claude-sonnet-5":          providers.ProviderAnthropic,
			"deepseek/deepseek-v4-pro": providers.ProviderFireworks,
		},
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, []string{"deepseek/deepseek-v4-pro"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{"deepseek/deepseek-v4-pro": rateLimitTestNow.Add(time.Minute)})
	ctx, turn := withRateLimitTurn(ctx)

	got := s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)

	assert.Equal(t, []string{"claude-sonnet-5", "deepseek/deepseek-v4-pro"}, siblingModels(got))
	s.noteRescueReadmission(ctx, overloadedDecision(md), got[0])
	assert.Contains(t, turn.completionLogFields(), false)
	assert.NotContains(t, turn.completionLogFields(), true)
}

// Without cooldowns on ctx (flag off, or nothing cooling) an exhausted pool
// is what it was: no candidate.
func TestRescueDecisions_ExhaustedPoolWithoutCooldownsStaysEmpty(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic)
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"claude-opus-5", "claude-sonnet-5"},
		CandidateProviders: map[string]string{"claude-sonnet-5": providers.ProviderAnthropic},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"claude-sonnet-5"})

	assert.Empty(t, s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0))
}

// The scorer drops automatically excluded models from CandidateModels, so a
// cooling arm is usually absent from the scored list: it is still readmitted,
// resolved through its catalog binding.
func TestRescueDecisions_ReadmitsCoolingArmAbsentFromScoredCandidates(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic)
	md := &router.RoutingMetadata{
		CandidateModels:    []string{"claude-sonnet-5", "claude-haiku-4-5"},
		CandidateProviders: map[string]string{"claude-haiku-4-5": providers.ProviderAnthropic},
	}
	failed := overloadedDecision(md)
	failed.Model = "claude-sonnet-5"
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"claude-opus-5"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{"claude-opus-5": rateLimitTestNow.Add(30 * time.Second)})

	got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)

	assert.Equal(t, []string{"claude-haiku-4-5", "claude-opus-5"}, siblingModels(got))
	assert.Equal(t, providers.ProviderAnthropic, got[1].Provider)
}

func TestSiblingFailover_ScorerReadmitsOnlyEligibleCoolingPeersAtOrAboveSelectedTier(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI)
	failed := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-sonnet-5",
		Metadata: &router.RoutingMetadata{
			ClusterRouterVersion: "v-test",
			CandidateModels:      []string{"claude-sonnet-5", "gpt-5"},
			ScorerRescuePool:     []string{"claude-sonnet-5", "gpt-5", "claude-opus-5", "claude-haiku-4-5"},
			CandidateProviders:   map[string]string{"gpt-5": providers.ProviderOpenAI},
		},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"claude-opus-5", "claude-haiku-4-5", "gpt-4.1-mini"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{
		"claude-opus-5":    rateLimitTestNow.Add(30 * time.Second),
		"claude-haiku-4-5": rateLimitTestNow.Add(10 * time.Second),
		"gpt-4.1-mini":     rateLimitTestNow.Add(5 * time.Second),
	})

	got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)

	assert.Equal(t, []string{"gpt-5", "claude-opus-5"}, siblingModels(got))
	assert.Equal(t, providers.ProviderAnthropic, got[1].Provider)
}

func TestSiblingFailover_SidecarReadmitsCoolingRosterArmLast(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic, providers.ProviderOpenAI)
	failed := router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    "claude-haiku-4-5",
		Metadata: &router.RoutingMetadata{
			RosterFailover:     true,
			RescueModels:       []string{"claude-haiku-4-5", "gpt-4.1-mini"},
			SidecarRescuePool:  []string{"claude-opus-5"},
			CandidateProviders: map[string]string{"gpt-4.1-mini": providers.ProviderOpenAI},
		},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"claude-opus-5", "gpt-5"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{
		"claude-opus-5": rateLimitTestNow.Add(30 * time.Second),
		"gpt-5":         rateLimitTestNow.Add(10 * time.Second),
	})

	got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)

	assert.Equal(t, []string{"gpt-4.1-mini", "claude-opus-5"}, siblingModels(got))
	assert.Equal(t, providers.ProviderAnthropic, got[1].Provider)
}

// Lifting the cooldowns lifts only the cooldowns: a deployment-wide
// automatic exclusion still keeps its model out of the readmission walk.
func TestRescueDecisions_ReadmissionKeepsGlobalAutomaticExclusions(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic).
		WithGlobalAutomaticExclusions(&stubGlobalExclusionStore{byModel: map[string]string{"claude-haiku-4-5": "disabled"}})
	md := &router.RoutingMetadata{
		CandidateModels: []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"},
		CandidateProviders: map[string]string{
			"claude-sonnet-5":  providers.ProviderAnthropic,
			"claude-haiku-4-5": providers.ProviderAnthropic,
		},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"claude-sonnet-5"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{"claude-sonnet-5": rateLimitTestNow.Add(30 * time.Second)})

	got := s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)

	assert.Equal(t, []string{"claude-sonnet-5"}, siblingModels(got))
}

// A cooling arm the deployment has disabled stays out: readmission lifts the
// cooldown, never the deployment-wide exclusion on the same model.
func TestRescueDecisions_ReadmissionKeepsGlobalExclusionOnCoolingArm(t *testing.T) {
	s := siblingService(providers.ProviderAnthropic).
		WithGlobalAutomaticExclusions(&stubGlobalExclusionStore{byModel: map[string]string{"claude-sonnet-5": "disabled"}})
	md := &router.RoutingMetadata{
		CandidateModels: []string{"claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"},
		CandidateProviders: map[string]string{
			"claude-sonnet-5":  providers.ProviderAnthropic,
			"claude-haiku-4-5": providers.ProviderAnthropic,
		},
	}
	ctx := context.WithValue(context.Background(), SessionDemotedModelsContextKey{}, []string{"claude-sonnet-5", "claude-haiku-4-5"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{
		"claude-sonnet-5":  rateLimitTestNow.Add(10 * time.Second),
		"claude-haiku-4-5": rateLimitTestNow.Add(30 * time.Second),
	})

	got := s.siblingFailoverDecisions(ctx, overloadedDecision(md), 1_000, 0, 0)

	assert.Equal(t, []string{"claude-haiku-4-5"}, siblingModels(got))
}

// The gateway BYOK walk readmits cooling arms the same way.
func TestGatewayRescueDecisions_ExhaustedPoolReadmitsCoolingArms(t *testing.T) {
	s := &Service{}
	ctx := context.WithValue(context.Background(), ExternalAPIKeysContextKey{}, []*auth.ExternalAPIKey{
		{Provider: providers.ProviderOpenAIGateway, Plaintext: []byte("pat"), ModelAliases: map[string]string{"grok-4.6": "grok-4.6"}},
		{Provider: providers.ProviderAnthropicGateway, Plaintext: []byte("pat"), ModelAliases: map[string]string{"claude-opus-5": "claude-opus-5"}},
	})
	ctx = context.WithValue(ctx, SessionDemotedModelsContextKey{}, []string{"claude-opus-5"})
	ctx = context.WithValue(ctx, SessionCooldownModelsContextKey{}, map[string]time.Time{"claude-opus-5": rateLimitTestNow.Add(time.Minute)})
	failed := router.Decision{
		Provider: providers.ProviderOpenAIGateway,
		Model:    "grok-4.6",
		Metadata: &router.RoutingMetadata{CandidateModels: []string{"grok-4.6", "claude-opus-5"}},
	}

	got := s.siblingFailoverDecisions(ctx, failed, 1_000, 0, 0)

	assert.Equal(t, []string{"claude-opus-5"}, siblingModels(got))
}

// The turn's throttle policy is the dispatch default schedule with the
// turn's account as observer: an honoured Retry-After lands on the
// completion line.
func TestThrottleRetryDelay_RecordsHonouredRetryAfter(t *testing.T) {
	svc := newRateLimitTestService(&cooldownStubPinStore{}, true, 45)
	ctx, turn := svc.withRateLimitTurn(context.Background())
	require.NotNil(t, turn)
	retryDelay := svc.throttleRetryDelay(ctx)
	require.NotNil(t, retryDelay)

	delay, retry := retryDelay(dispatch.Attempt{}, upstream429("3"), 250*time.Millisecond)
	assert.True(t, retry)
	assert.Equal(t, 3*time.Second, delay)

	delay, retry = retryDelay(dispatch.Attempt{}, upstream429("30"), 250*time.Millisecond)
	assert.False(t, retry, "above the cap the turn goes straight to rescue")
	assert.Zero(t, delay)

	fields := turn.completionLogFields()
	assert.Contains(t, fields, "retry_after_honoured")
	assert.Contains(t, fields, int64(3_000))
}

func coolingPin(model string, cooldowns map[string]time.Time, demoted ...string) sessionpin.Pin {
	pin := demotedPin(model, demoted...)
	pin.DemotionCooldowns = cooldowns
	return pin
}

func cooldownTurnLoopService(store sessionpin.Store, transient bool) (*Service, *authoritativeTestRouter) {
	scorer := &authoritativeTestRouter{decision: router.Decision{
		Provider: providers.ProviderAnthropic,
		Model:    freshTurnModel,
		Reason:   "cluster:v0.2",
	}}
	svc := NewService(scorer, nil, nil, false, nil, store, false,
		providers.ProviderAnthropic, "claude-haiku-4-5", nil).
		WithTransientRateLimit(transient, 45)
	svc.now = func() time.Time { return rateLimitTestNow }
	return svc, scorer
}

// A cooldown still in force keeps the arm out of the next turn's automatic
// pool and off the pin, and is carried on the result for the in-turn rescue.
func TestTurnLoopExcludesCoolingArmBeforeExpiry(t *testing.T) {
	until := rateLimitTestNow.Add(30 * time.Second)
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: coolingPin(demotedPinModel, map[string]time.Time{demotedPinModel: until}),
	}}
	svc, scorer := cooldownTurnLoopService(store, true)

	res := runDemotionTurnLoop(t, svc, context.Background())

	assert.Equal(t, freshTurnModel, res.Decision.Model)
	assert.False(t, res.StickyHit, "a cooling pin must not be reused")
	require.Len(t, scorer.requests, 1)
	assert.Contains(t, scorer.requests[0].AutomaticExcludedModels, demotedPinModel)
	assert.Equal(t, []string{demotedPinModel}, res.SessionDemotedModels)
	assert.Equal(t, map[string]time.Time{demotedPinModel: until}, res.SessionCooldownModels)
}

// Once the cooldown has expired the arm is eligible again: the pin holds and
// nothing is excluded.
func TestTurnLoopReadmitsArmAfterCooldownExpiry(t *testing.T) {
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: coolingPin(demotedPinModel, map[string]time.Time{demotedPinModel: rateLimitTestNow.Add(-time.Second)}),
	}}
	svc, scorer := cooldownTurnLoopService(store, true)

	res := runDemotionTurnLoop(t, svc, context.Background())

	assert.Equal(t, demotedPinModel, res.Decision.Model, "the cooled arm is served again")
	assert.True(t, res.StickyHit)
	for _, req := range scorer.requests {
		assert.NotContains(t, req.AutomaticExcludedModels, demotedPinModel)
	}
	assert.Empty(t, res.SessionDemotedModels)
	assert.Empty(t, res.SessionCooldownModels)
}

// A cooldown recorded on the HMM history row counts too.
func TestTurnLoopHonoursCooldownRecordedOnHMMHistory(t *testing.T) {
	until := rateLimitTestNow.Add(30 * time.Second)
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole:                 demotedPin(demotedPinModel),
		hmmHistoryRole(sessionpin.DefaultRole): coolingPin(demotedPinModel, map[string]time.Time{demotedPinModel: until}),
	}}
	svc, scorer := cooldownTurnLoopService(store, true)

	res := runDemotionTurnLoop(t, svc, context.Background())

	assert.Equal(t, freshTurnModel, res.Decision.Model)
	require.Len(t, scorer.requests, 1)
	assert.Contains(t, scorer.requests[0].AutomaticExcludedModels, demotedPinModel)
	assert.Equal(t, map[string]time.Time{demotedPinModel: until}, res.SessionCooldownModels)
}

// A model struck for the session and still carrying a stale cooldown entry
// stays out: the turn loop excludes it and offers nothing for readmission.
func TestTurnLoopPermanentStrikeOutranksLeftoverCooldown(t *testing.T) {
	until := rateLimitTestNow.Add(30 * time.Second)
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: coolingPin(demotedPinModel, map[string]time.Time{demotedPinModel: until}, demotedPinModel),
	}}
	svc, scorer := cooldownTurnLoopService(store, true)

	res := runDemotionTurnLoop(t, svc, context.Background())

	assert.Equal(t, freshTurnModel, res.Decision.Model)
	require.Len(t, scorer.requests, 1)
	assert.Contains(t, scorer.requests[0].AutomaticExcludedModels, demotedPinModel)
	assert.Equal(t, []string{demotedPinModel}, res.SessionDemotedModels)
	assert.Nil(t, res.SessionCooldownModels, "a lifetime strike is never readmitted")
}

// Flag off, a stored cooldown is ignored entirely: the turn loop reads only
// the session-lifetime strikes, as before.
func TestTurnLoopIgnoresCooldownsWhenFlagOff(t *testing.T) {
	store := &rolePinStore{byRole: map[string]sessionpin.Pin{
		sessionpin.DefaultRole: coolingPin(demotedPinModel, map[string]time.Time{demotedPinModel: rateLimitTestNow.Add(time.Hour)}),
	}}
	svc, scorer := cooldownTurnLoopService(store, false)

	res := runDemotionTurnLoop(t, svc, context.Background())

	assert.Equal(t, demotedPinModel, res.Decision.Model)
	assert.True(t, res.StickyHit)
	for _, req := range scorer.requests {
		assert.NotContains(t, req.AutomaticExcludedModels, demotedPinModel)
	}
	assert.Empty(t, res.SessionDemotedModels)
	assert.Nil(t, res.SessionCooldownModels)
}
