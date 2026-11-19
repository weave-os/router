package candidates_test

import (
	"fmt"
	"testing"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/candidates"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
)

const (
	textOnlyForcedModel    = "qwen/qwen3-coder-next"
	outputLimitFirstModel  = "claude-sonnet-5"
	outputLimitSecondModel = "claude-opus-5"
)

func automaticallyDisabledRequest(model string) router.Request {
	return router.Request{AutomaticExcludedModels: map[string]struct{}{model: {}}}
}

func TestAutomaticPinEligible_RejectsDisabledModelButForcedPinSurvives(t *testing.T) {
	pin := sessionpin.Pin{Model: "claude-opus-5", Provider: providers.ProviderAnthropic}
	req := automaticallyDisabledRequest("claude-opus-5")

	assert.False(t, candidates.AutomaticPinEligible(pin, req))
	assert.True(t, candidates.ForcedPinEligible(pin, req),
		"a user-forced pin is the escape hatch that keeps a disabled model reachable")
}

func TestReadmitForcedImageLimitCopiesExcludedModels(t *testing.T) {
	const model = "xiaomi/mimo-v2.6-pro"
	excluded := map[string]struct{}{model: {}, "other-model": {}}
	req := router.Request{
		ImageCount:               17,
		ExcludedModels:           excluded,
		ImageLimitExcludedModels: map[string]struct{}{model: {}},
	}

	readmitted := candidates.ReadmitForcedImageLimit(req, model)

	assert.Zero(t, readmitted.ImageCount)
	assert.NotContains(t, readmitted.ExcludedModels, model)
	assert.Contains(t, readmitted.ExcludedModels, "other-model")
	assert.Contains(t, req.ExcludedModels, model, "readmission must not mutate the caller's exclusion set")
	assert.Equal(t, 17, req.ImageCount, "readmission must not mutate the caller's request")
}

// ForcedPinEligible is the gate for the hard-pinned-turn fast path, which is a
// separate branch from the one above and needs the same guard.
func TestForcedPinEligible_RejectsTextOnlyModelOnImageTurn(t *testing.T) {
	pin := sessionpin.Pin{Provider: providers.ProviderFireworks, Model: textOnlyForcedModel}

	assert.False(t, candidates.ForcedPinEligible(pin, router.Request{HasImages: true}),
		"text-only forced pin must be ineligible for an image-bearing turn")
	assert.True(t, candidates.ForcedPinEligible(pin, router.Request{HasImages: false}),
		"same pin stays eligible without images")
	assert.True(t, candidates.ForcedPinEligible(
		sessionpin.Pin{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-6"},
		router.Request{HasImages: true}),
		"an image-capable forced pin stays eligible on an image turn")
}

func TestMaxedOutServedModel_RequiresMatchingTerminalEvidence(t *testing.T) {
	ended := time.Unix(100, 0)
	for _, output := range []int{128, 8192, 32000, 64000} {
		for _, tc := range []struct {
			name   string
			marker time.Time
			want   string
		}{
			{"legacy unknown", time.Time{}, ""},
			{"older writer replaced usage", ended.Add(-time.Second), ""},
			{"confirmed cap", ended, outputLimitFirstModel},
		} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, output), func(t *testing.T) {
				pin := sessionpin.Pin{
					Model: outputLimitSecondModel, LastServedModel: outputLimitFirstModel + ":high",
					LastOutputTokens: output, LastTurnEndedAt: ended, LastOutputLimitAt: tc.marker,
				}
				assert.Equal(t, tc.want, candidates.MaxedOutServedModel(pin))
				pin.LastServedModel = ""
				want := ""
				if tc.want != "" {
					want = outputLimitSecondModel
				}
				assert.Equal(t, want, candidates.MaxedOutServedModel(pin), "legacy serving identity falls back to the anchor")
			})
		}
	}
}

// ExcludedModels / SafetyExcludedModels are keyed on bare catalog IDs;
// leaving effort on would silently disable loop-breaking for effort-carrying turns.
func TestMaxedOutServedModel_StripsEffortSoExclusionMatches(t *testing.T) {
	pin := sessionpin.Pin{
		LastServedModel:   "claude-opus-5:xhigh",
		LastOutputTokens:  8192,
		LastTurnEndedAt:   time.Unix(100, 0),
		LastOutputLimitAt: time.Unix(100, 0),
	}

	assert.Equal(t, "claude-opus-5", candidates.MaxedOutServedModel(pin),
		"exclusion keys are bare catalog IDs")
}

func TestMaxedOutServedModel_BareIdentityUnchanged(t *testing.T) {
	pin := sessionpin.Pin{
		LastServedModel:   "claude-opus-5",
		LastOutputTokens:  8192,
		LastTurnEndedAt:   time.Unix(100, 0),
		LastOutputLimitAt: time.Unix(100, 0),
	}

	assert.Equal(t, "claude-opus-5", candidates.MaxedOutServedModel(pin))
}

func TestBaseModelOf(t *testing.T) {
	assert.Equal(t, "claude-opus-5", candidates.BaseModelOf("claude-opus-5:xhigh"))
	assert.Equal(t, "claude-opus-5", candidates.BaseModelOf("claude-opus-5"))
	assert.Empty(t, candidates.BaseModelOf(""))
}
