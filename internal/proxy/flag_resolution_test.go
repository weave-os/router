package proxy_test

import (
	"context"
	"testing"

	"weave-os/router/internal/flags"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"

	"github.com/stretchr/testify/assert"
)

// newFlagTestService builds a Service with no providers wired: these tests only
// exercise flag resolution, which touches nothing but the Service's flag fields
// and the request context.
func newFlagTestService(embedOnly bool) *proxy.Service {
	return proxy.NewService(
		nil, map[string]providers.Client{}, nil, embedOnly, nil, nil,
		false, providers.ProviderAnthropic, "claude-haiku-4-5", nil,
	)
}

func TestResolversFallBackToDeploymentDefault(t *testing.T) {
	svc := newFlagTestService(true).
		WithStruggleShadowConfig(true).
		WithSpiralShadowConfig(false).
		WithLoopEscalationConfig(true, 10).
		WithSiblingFailover(false)

	ctx := context.Background()
	assert.True(t, svc.ResolveStruggleShadowEnabled(ctx))
	assert.False(t, svc.ResolveSpiralShadowEnabled(ctx))
	assert.True(t, svc.ResolveLoopEscalationEnabled(ctx))
	assert.Equal(t, 10, svc.ResolveLoopEscalationHoldoutPct(ctx))
	assert.False(t, svc.ResolveSiblingFailover(ctx))
	assert.True(t, svc.ResolveEmbedOnlyUserMessage(ctx))
}

func TestPerOrgOverrideBeatsDeploymentDefault(t *testing.T) {
	// The motivating case: the deployment ships the struggle detector on, and one
	// organization turns it off (and vice versa for a default-off flag).
	svc := newFlagTestService(true).
		WithStruggleShadowConfig(true).
		WithSiblingFailover(false).
		WithLoopEscalationConfig(true, 10)

	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{
			flags.KeyStruggleShadowEnabled: false,
			flags.KeySiblingFailover:       true,
		},
		Ints: map[flags.Key]int{flags.KeyLoopEscalationHoldoutPct: 100},
	})

	assert.False(t, svc.ResolveStruggleShadowEnabled(ctx), "org override should turn a default-on flag off")
	assert.True(t, svc.ResolveSiblingFailover(ctx), "org override should turn a default-off flag on")
	assert.Equal(t, 100, svc.ResolveLoopEscalationHoldoutPct(ctx))

	// An unoverridden flag on the same context keeps its deployment default.
	assert.True(t, svc.ResolveLoopEscalationEnabled(ctx))
}

func TestStringFlagOverride(t *testing.T) {
	svc := newFlagTestService(true).
		WithCyberRefusalRepin(true).
		WithCyberRefusalFallbackModel("claude-sonnet-5")

	assert.Equal(t, "claude-sonnet-5", svc.ResolveCyberRefusalFallbackModel(context.Background()))

	ctx := flags.WithOverrides(context.Background(), flags.Overrides{
		Strings: map[flags.Key]string{flags.KeyCyberRefusalFallback: "claude-opus-5"},
	})
	assert.Equal(t, "claude-opus-5", svc.ResolveCyberRefusalFallbackModel(ctx))
}

func TestHeaderOverrideBeatsPerOrgOverride(t *testing.T) {
	// embed_only_user_message is the one registered flag that also has a header
	// override, so it pins the full precedence chain:
	// header > per-org > deployment default.
	svc := newFlagTestService(false)

	orgOn := flags.WithOverrides(context.Background(), flags.Overrides{
		Bools: map[flags.Key]bool{flags.KeyEmbedOnlyUserMessage: true},
	})
	assert.True(t, svc.ResolveEmbedOnlyUserMessage(orgOn), "per-org override beats the deployment default")

	headerOff := context.WithValue(orgOn, proxy.EmbedOnlyUserMessageContextKey{}, false)
	assert.False(t, svc.ResolveEmbedOnlyUserMessage(headerOff), "header override beats the per-org override")
}
