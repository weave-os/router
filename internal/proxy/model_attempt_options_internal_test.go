package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/translate"
)

func TestSwitchedAttemptOptionsResolveNewTargetEffortWithoutChangingOriginal(t *testing.T) {
	svc := WithStrategyPorts(NewService(nil, nil, nil, nil, false, providers.ProviderAnthropic, fastOpusModel, nil), hmm.ServingPorts())
	base := translate.EmitOptions{
		TargetModel:          fastOpusModel,
		TargetProvider:       providers.ProviderAnthropic,
		Capabilities:         router.Lookup(fastOpusModel),
		ForceEffort:          "low",
		ForceReasoningEffort: "low",
		AppendAutonomySystem: true,
	}
	target := router.Decision{Provider: providers.ProviderOpenAI, Model: "gpt-5.5", Effort: "xhigh"}
	opts, effort := switchedAttemptOptions(context.Background(), base, target, false, svc.resolveEffort)
	assert.Equal(t, target.Model, opts.TargetModel)
	assert.Equal(t, target.Provider, opts.TargetProvider)
	assert.Equal(t, router.Lookup(target.Model), opts.Capabilities)
	assert.True(t, opts.ModelSwitched, "signed reasoning from the old target must not replay verbatim")
	assert.Equal(t, "high", effort.Sent, "the new target clamps its own effort")
	assert.Equal(t, "max", opts.ForceReasoningEffort, "emit options retain the pre-cap level")
	assert.Equal(t, effortSourceArm, effort.Source)
	assert.True(t, opts.AppendAutonomySystem)
	assert.Equal(t, fastOpusModel, base.TargetModel)
	assert.Equal(t, "low", base.ForceReasoningEffort)
	assert.False(t, base.ModelSwitched)
}
