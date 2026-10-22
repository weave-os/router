package sessionpin_test

import (
	"testing"
	"time"

	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	anthropicFamily = sessionpin.TokenizerFamilyAnthropic
	openAIFamily    = sessionpin.TokenizerFamilyOpenAI
)

func TestTokenizerCalibration_MeasuresBothDirectionsFromCloseObservations(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cal := sessionpin.TokenizerCalibration{}.
		Observe(anthropicFamily, 860_000, t0).
		Observe(openAIFamily, 500_000, t0.Add(20*time.Second))

	toAnthropic, ok := cal.Ratio(openAIFamily, anthropicFamily)
	require.True(t, ok)
	assert.InDelta(t, 1.72, toAnthropic, 1e-9)
	toOpenAI, ok := cal.Ratio(anthropicFamily, openAIFamily)
	require.True(t, ok)
	assert.InDelta(t, 500_000.0/860_000, toOpenAI, 1e-9)
}

func TestTokenizerCalibration_NoRatioWithoutComparableEvidence(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for name, cal := range map[string]sessionpin.TokenizerCalibration{
		"one family only": sessionpin.TokenizerCalibration{}.Observe(openAIFamily, 500_000, t0),
		"observations too far apart": sessionpin.TokenizerCalibration{}.
			Observe(anthropicFamily, 860_000, t0).
			Observe(openAIFamily, 500_000, t0.Add(sessionpin.TokenizerCalibrationMaxGap+time.Second)),
		"history too small to separate tokenizer from overhead": sessionpin.TokenizerCalibration{}.
			Observe(anthropicFamily, 8_000, t0).
			Observe(openAIFamily, 4_000, t0.Add(time.Second)),
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := cal.Ratio(openAIFamily, anthropicFamily)
			assert.False(t, ok)
		})
	}
}

func TestTokenizerCalibration_RatioOutlivesItsObservations(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cal := sessionpin.TokenizerCalibration{}.
		Observe(anthropicFamily, 860_000, t0).
		Observe(openAIFamily, 500_000, t0.Add(time.Second)).
		Observe(openAIFamily, 700_000, t0.Add(time.Hour))

	ratio, ok := cal.Ratio(openAIFamily, anthropicFamily)
	require.True(t, ok, "a thread keeps its measured ratio while it stays on one family")
	assert.InDelta(t, 1.72, ratio, 1e-9)
}

func TestTokenizerCalibration_RemeasuresOnEachSwitch(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	cal := sessionpin.TokenizerCalibration{}.
		Observe(anthropicFamily, 860_000, t0).
		Observe(openAIFamily, 500_000, t0.Add(time.Second)).
		Observe(anthropicFamily, 960_000, t0.Add(30*time.Second))

	ratio, ok := cal.Ratio(openAIFamily, anthropicFamily)
	require.True(t, ok)
	assert.InDelta(t, 1.92, ratio, 1e-9)
}

func TestTokenizerCalibration_ObserveDoesNotMutateReceiver(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	base := sessionpin.TokenizerCalibration{}.Observe(anthropicFamily, 860_000, t0)
	_ = base.Observe(openAIFamily, 500_000, t0.Add(time.Second))
	assert.Len(t, base.Observations, 1)
	assert.Empty(t, base.Ratios)
}

func TestMergeTokenizerCalibrations_KeepsNewestEvidence(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	older := sessionpin.TokenizerCalibration{}.
		Observe(anthropicFamily, 800_000, t0).
		Observe(openAIFamily, 500_000, t0.Add(time.Second))
	newer := sessionpin.TokenizerCalibration{}.
		Observe(anthropicFamily, 900_000, t0.Add(time.Hour)).
		Observe(openAIFamily, 500_000, t0.Add(time.Hour+time.Second))

	merged := sessionpin.MergeTokenizerCalibrations(older, newer, sessionpin.TokenizerCalibration{})
	ratio, ok := merged.Ratio(openAIFamily, anthropicFamily)
	require.True(t, ok)
	assert.InDelta(t, 1.8, ratio, 1e-9)
	assert.Equal(t, 900_000, merged.Observations[anthropicFamily].Prompt)
}

func TestTokenizerCalibration_SameFamilyIsIdentity(t *testing.T) {
	ratio, ok := sessionpin.TokenizerCalibration{}.Ratio(openAIFamily, openAIFamily)
	assert.True(t, ok)
	assert.Equal(t, 1.0, ratio)
}
