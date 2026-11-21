package candidates_test

import (
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/catalog"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
)

func TestJudgeForceModelPinServesPastTheContextWindowEstimate(t *testing.T) {
	pin := sessionpin.Pin{Provider: "anthropic", Model: string(catalog.ModelIDClaudeOpus47)}
	var req router.Request
	req.Candidates.ExcludedModels = map[string]struct{}{pin.Model: {}}
	req.Candidates.ContextWindowExcludedModels = map[string]struct{}{pin.Model: {}}

	verdict := candidates.JudgeForceModelPin(pin, req)

	assert.True(t, verdict.Serve)
	assert.True(t, verdict.PastContextEstimate)
}

func TestJudgeForceModelPinDropsADisabledProviderWithoutATierFloor(t *testing.T) {
	pin := sessionpin.Pin{Provider: "anthropic", Model: string(catalog.ModelIDClaudeOpus47)}
	req := router.Request{EnabledProviders: map[string]struct{}{"openai": {}}}

	verdict := candidates.JudgeForceModelPin(pin, req)

	assert.False(t, verdict.Serve)
	assert.Equal(t, "provider_not_enabled", verdict.DropReason)
	assert.Equal(t, catalog.TierUnknown, verdict.TierFloor)
	assert.False(t, verdict.ExcludeModel)
}

func TestJudgeUserForcedPinFloorsAnExcludedModelAtItsTier(t *testing.T) {
	pin := sessionpin.Pin{Provider: "anthropic", Model: string(catalog.ModelIDClaudeOpus47)}
	var req router.Request
	req.Candidates.ExcludedModels = map[string]struct{}{pin.Model: {}}
	req.Candidates.ContextWindowExcludedModels = map[string]struct{}{pin.Model: {}}

	verdict := candidates.JudgeUserForcedPin(pin, req)

	assert.False(t, verdict.Serve)
	assert.Equal(t, "excluded", verdict.DropReason)
	assert.Equal(t, catalog.TierFor(pin.Model), verdict.TierFloor)
	assert.False(t, verdict.ExcludeModel)
}

func TestJudgeUserForcedPinExcludesATextOnlyModelFromAnImageTurn(t *testing.T) {
	pin := sessionpin.Pin{Provider: "openrouter", Model: textOnlyForcedModel}
	req := router.Request{HasImages: true, ImageCount: 1}

	verdict := candidates.JudgeUserForcedPin(pin, req)

	assert.Equal(t, "not_image_capable", verdict.DropReason)
	assert.True(t, verdict.ExcludeModel)
	assert.Equal(t, catalog.TierFor(pin.Model), verdict.TierFloor)
}
