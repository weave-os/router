package proxy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForceParentRoutesChildrenAndSupportsScopeTransitions(t *testing.T) {
	const apiKeyID = "parent-scope-test-key"
	installationID := uuid.New()
	ctx := context.WithValue(context.Background(), ClientIdentityContextKey{}, ClientIdentity{SessionID: "shared-parent-session"})
	ctx = router.WithStrategy(ctx, router.StrategyCluster)
	parent, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"max_tokens":4096,"messages":[{"role":"user","content":"Implement a synthetic feature"}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	child, err := translate.ParseAnthropic([]byte(fmt.Sprintf(`{"model":%q,"max_tokens":4096,"messages":[{"role":"user","content":"Research a synthetic helper"}]}`, catalog.ModelIDClaudeOpus48.String())))
	require.NoError(t, err)
	parentKey := deriveSessionKeyForRequest(ctx, parent, apiKeyID)
	childKey := deriveSessionKeyForRequest(ctx, child, apiKeyID)
	controlKey := deriveForceModelSessionKeyForRequest(ctx, parent, apiKeyID, parentKey)
	require.NotEqual(t, parentKey, childKey)
	require.Equal(t, controlKey, deriveForceModelSessionKeyForRequest(ctx, child, apiKeyID, childKey))
	store := newForceModelMapStore()
	scorer := &tierProbeRouter{available: map[string]struct{}{catalog.ModelIDClaudeHaiku45.String(): {}}}
	svc := NewService(scorer, nil, nil, false, nil, store, false, providers.ProviderAnthropic, catalog.ModelIDClaudeHaiku45.String(), nil)
	apply := func(command translate.ForceModelResult) string {
		t.Helper()
		_, ack, err := svc.applyForceModelCommand(ctx, command, installationID, parentKey, controlKey)
		require.NoError(t, err)
		return ack.render(parent.SourceFormat())
	}
	route := func(env *translate.RequestEnvelope) turnLoopResult {
		t.Helper()
		features := env.RoutingFeatures(false)
		routed, err := svc.runTurnLoop(ctx, env, features, apiKeyID, installationID, "", nil, router.Request{RequestedModel: features.Model})
		require.NoError(t, err)
		return routed
	}
	apply(translate.ForceModelResult{Model: "opus-4-8"})
	require.Equal(t, catalog.ModelIDClaudeOpus48.String(), route(child).Decision.Model, "force-model retains conversation scope")
	// Reproduce a legacy child force, which must not revive when narrowing scope.
	require.NoError(t, store.Upsert(ctx, sessionpin.Pin{
		SessionKey: childKey, Role: roleForTier(catalog.TierHigh), InstallationID: installationID,
		Model: catalog.ModelIDClaudeOpus48.String(), Provider: providers.ProviderAnthropic,
		Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(time.Hour),
	}))
	ack := apply(translate.ForceModelResult{Model: "opus-4-8:high", ParentOnly: true})
	assert.Contains(t, ack, "force-parent applied:")
	assert.Contains(t, ack, "Subagents use automatic routing")
	features := parent.RoutingFeatures(false)
	inheritedChild, err := svc.runTurnLoop(ctx, parent, features, apiKeyID, installationID, "explore", nil, router.Request{RequestedModel: features.Model})
	require.NoError(t, err)
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), inheritedChild.Decision.Model, "identified subagents must route even when they inherit the parent transcript")
	parentRoute := route(parent)
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), parentRoute.Decision.Model)
	assert.Equal(t, "high", parentRoute.Decision.Effort)
	childRoute := route(child)
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), childRoute.Decision.Model)
	assert.NotEqual(t, translate.ReasonUserForceModel, childRoute.Decision.Reason)
	require.NotEmpty(t, scorer.captured, "child must reach automatic routing")
	stored, found, err := store.Get(ctx, controlKey, forceModelSessionRole)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, sessionKeyHex(parentKey), stored.ForcedThreadKey)
	assert.Contains(t, apply(translate.ForceModelResult{Model: "not-a-catalog-model", ParentOnly: true}), "isn't a recognized model")
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), route(parent).Decision.Model, "rejected commands preserve the active scope")
	apply(translate.ForceModelResult{Model: "opus-4-8"})
	assert.Equal(t, catalog.ModelIDClaudeOpus48.String(), route(child).Decision.Model, "force-model widens scope again")
	apply(translate.ForceModelResult{Model: "opus-4-8", ParentOnly: true})
	apply(translate.ForceModelResult{Clear: true})
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), route(parent).Decision.Model, "clear resumes parent automatic routing")
	assert.Equal(t, catalog.ModelIDClaudeHaiku45.String(), route(child).Decision.Model, "clear keeps children automatic")
}
