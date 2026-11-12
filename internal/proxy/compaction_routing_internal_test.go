package proxy

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/cluster"
	"weave-os/router/internal/router/policy"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

const (
	codexCompactionBody  = `{"model":"gpt-5.6-terra","messages":[{"role":"user","content":"You are performing a CONTEXT CHECKPOINT COMPACTION. Create a handoff summary."}]}`
	claudeCompactionBody = `{"model":"claude-opus-5-5","max_tokens":20000,"system":"Your task is to create a detailed summary of the conversation so far.","messages":[{"role":"user","content":"summarize"}]}`
)

type compactionScorer struct {
	decision router.Decision
	calls    *atomic.Int32
}

func (r compactionScorer) Route(context.Context, router.Request) (router.Decision, error) {
	r.calls.Add(1)
	return r.decision, nil
}

type compactionRoutingFixture struct {
	svc            *Service
	store          *forceModelMapStore
	scorerCalls    *atomic.Int32
	installationID uuid.UUID
}

// The cluster bundle's fastest-model resolver stands in for the generic
// utility hard pin; compaction must never reach it.
func newCompactionRoutingFixture(t *testing.T, scored router.Decision) compactionRoutingFixture {
	t.Helper()
	version, err := cluster.ResolveVersion(cluster.LatestVersion)
	require.NoError(t, err)
	bundle, err := cluster.LoadBundle(version)
	require.NoError(t, err)
	calls := &atomic.Int32{}
	store := newForceModelMapStore()
	svc := NewService(compactionScorer{decision: scored, calls: calls},
		map[string]providers.Client{providers.ProviderOpenAI: nil, providers.ProviderAnthropic: nil},
		nil, false, nil, store, false, providers.ProviderAnthropic, policy.PrecompactionDefaultModel, nil).
		WithHardPinResolver(func(req HardPinRequest) (string, string, bool) {
			return cluster.FastestModelForRequest(bundle.Metadata, bundle.Registry, req.EnabledProviders, req.ExcludedModels, nil,
				cluster.RequestBindings{Custom: req.CustomBindings, Gateways: req.GatewayProviders})
		})
	return compactionRoutingFixture{svc: svc, store: store, scorerCalls: calls, installationID: uuid.New()}
}

func (f compactionRoutingFixture) run(t *testing.T, ctx context.Context, env *translate.RequestEnvelope, req router.Request) (turnLoopResult, error) {
	t.Helper()
	features := env.RoutingFeatures(false)
	req.RequestedModel = features.Model
	return f.svc.runTurnLoop(ctx, env, features, "test-key", f.installationID, "", nil, req)
}

func (f compactionRoutingFixture) pinSession(t *testing.T, ctx context.Context, env *translate.RequestEnvelope, decision router.Decision) {
	t.Helper()
	role := roleForTier(catalog.TierFor(env.RoutingFeatures(false).Model))
	f.svc.writeNewPin(ctx, f.installationID, deriveSessionKeyForRequest(ctx, env, "test-key"), role, decision)
}

func openAIBundleModels(t *testing.T) map[string]struct{} {
	t.Helper()
	version, err := cluster.ResolveVersion(cluster.LatestVersion)
	require.NoError(t, err)
	bundle, err := cluster.LoadBundle(version)
	require.NoError(t, err)
	out := map[string]struct{}{}
	for _, entry := range bundle.Registry.DeployedModels {
		if entry.Provider == providers.ProviderOpenAI {
			out[entry.Model] = struct{}{}
		}
	}
	return out
}

// A resumed Codex thread whose first request is a compaction has no session
// pin yet. With the cluster bundle's OpenAI models excluded and only OpenAI
// enabled, compaction used to fail with a retryable "cluster scorer failed"
// 503 on every retry instead of asking the scorer.
func TestCompactionWithoutSessionPinUsesScorer(t *testing.T) {
	scored := router.Decision{Provider: providers.ProviderOpenAI, Model: compactionStrategyModel, Reason: "cluster_scored"}
	f := newCompactionRoutingFixture(t, scored)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	env, err := translate.ParseOpenAI([]byte(codexCompactionBody))
	require.NoError(t, err)

	turn, err := f.run(t, ctx, env, router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
		ExcludedModels:   openAIBundleModels(t),
	})

	require.NoError(t, err)
	assert.NotErrorIs(t, err, cluster.ErrClusterUnavailable)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.Equal(t, compactionStrategyModel, turn.Decision.Model)
	assert.Equal(t, int32(1), f.scorerCalls.Load())
	assert.False(t, turn.HardPinned)
}

// Claude Code compaction without a pin takes the scorer's pick rather than a
// deployment-configured summarizer.
func TestClaudeCodeCompactionWithoutSessionPinUsesScorer(t *testing.T) {
	scored := router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna, Reason: "cluster_scored"}
	f := newCompactionRoutingFixture(t, scored)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppClaudeCode})
	env, err := translate.ParseAnthropic([]byte(claudeCompactionBody))
	require.NoError(t, err)

	turn, err := f.run(t, ctx, env, router.Request{})

	require.NoError(t, err)
	assert.Equal(t, turntype.Compaction, turn.TurnType)
	assert.Equal(t, catalog.ModelGPT6Luna, turn.Decision.Model)
	assert.Equal(t, int32(1), f.scorerCalls.Load())
}

// Every client's compaction stays on the model serving the conversation.
func TestCompactionStaysOnSessionPin(t *testing.T) {
	for _, test := range []struct {
		name   string
		client string
		body   string
		parse  func([]byte) (*translate.RequestEnvelope, error)
		pin    router.Decision
	}{
		{"codex", ClientAppCodex, codexCompactionBody, translate.ParseOpenAI,
			router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}},
		{"codex on a low-tier model", ClientAppCodex, codexCompactionBody, translate.ParseOpenAI,
			router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.4-mini"}},
		{"claude code", ClientAppClaudeCode, claudeCompactionBody, translate.ParseAnthropic,
			router.Decision{Provider: providers.ProviderAnthropic, Model: policy.PrecompactionLargeWindowModel}},
		{"claude code on another vendor", ClientAppClaudeCode, claudeCompactionBody, translate.ParseAnthropic,
			router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCompactionRoutingFixture(t, router.Decision{Provider: providers.ProviderOpenAI, Model: compactionStrategyModel})
			ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: test.client})
			env, err := test.parse([]byte(test.body))
			require.NoError(t, err)
			f.pinSession(t, ctx, env, test.pin)

			turn, err := f.run(t, ctx, env, router.Request{})

			require.NoError(t, err)
			assert.Equal(t, turntype.Compaction, turn.TurnType)
			assert.Equal(t, test.pin.Model, turn.Decision.Model)
			assert.Equal(t, test.pin.Provider, turn.Decision.Provider)
			assert.Zero(t, f.scorerCalls.Load(), "a usable session pin skips the scorer")
		})
	}
}

// A pin the request can no longer use falls through to the scorer, not to the
// cluster utility model.
func TestCompactionIneligibleSessionPinUsesScorer(t *testing.T) {
	scored := router.Decision{Provider: providers.ProviderOpenAI, Model: compactionStrategyModel, Reason: "cluster_scored"}
	f := newCompactionRoutingFixture(t, scored)
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex})
	env, err := translate.ParseOpenAI([]byte(codexCompactionBody))
	require.NoError(t, err)
	f.pinSession(t, ctx, env, router.Decision{Provider: providers.ProviderOpenAI, Model: catalog.ModelGPT6Luna})

	turn, err := f.run(t, ctx, env, router.Request{
		EnabledProviders: map[string]struct{}{providers.ProviderOpenAI: {}},
		ExcludedModels:   map[string]struct{}{catalog.ModelGPT6Luna: {}},
	})

	require.NoError(t, err)
	assert.Equal(t, compactionStrategyModel, turn.Decision.Model)
	assert.Equal(t, int32(1), f.scorerCalls.Load())
}
