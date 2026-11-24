package routingpolicy

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/continuity"
	"weave-os/router/internal/router/sessionpin"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogSwitchShadowRecordsTheEvictionEconomicsWithoutChangingTheServedTurn(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
	stayPin := sessionpin.Pin{
		Model: "claude-opus-4-8", Provider: providers.ProviderAnthropic, LastServedModel: "claude-opus-4-8", LastTurnEndedAt: now.Add(-20 * time.Second),
		LastInputTokens: 1_000, LastCachedReadTokens: 399_000,
	}
	gate := continuity.CostGateInput{
		Fresh:                router.Decision{Model: "claude-sonnet-4-6"},
		EstimatedInputTokens: 401_000,
		HMMHistory: sessionpin.Pin{ModelCaches: sessionpin.ModelCaches{
			"claude-opus-4-8":   {ServedAt: now.Add(-20 * time.Second), PromptTokens: 400_000, MessageCount: 300, WriteTTLSeconds: 3600},
			"claude-sonnet-4-6": {ServedAt: now.Add(-10 * time.Minute), PromptTokens: 350_000, MessageCount: 260, WriteTTLSeconds: 300},
		}},
	}
	res := TurnRoute{
		Decision:   router.Decision{Model: "claude-sonnet-4-6"},
		cacheShape: TurnCacheShape{MessageCount: 302, UserPrompt: true},
	}

	logSwitchShadow(ctx, switchShadowTurn{StayPin: stayPin, Gate: gate, Now: now}, res)

	var line map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &line))
	assert.Equal(t, "router.switch_shadow", line["msg"])
	assert.Equal(t, "claude-opus-4-8", line["would_serve"])
	assert.Equal(t, string(SwitchReasonDowngradePending), line["would_reason"])
	assert.Equal(t, true, line["would_diverge"])
	assert.Equal(t, true, line["downgrade"])
	assert.InDelta(t, switchDowngradeHorizonTurns, line["horizon_turns"], 0)
	assert.Greater(t, line["eviction_usd"], line["move_value_usd"])
	assert.Greater(t, line["move_value_usd"], 0.0)
	assert.Equal(t, true, line["fresh_cache_known"])
	assert.InDelta(t, 42, line["fresh_messages_since_served"], 0)
	assert.InDelta(t, 600, line["fresh_seconds_since_served"], 0)
	assert.InDelta(t, 300, line["fresh_cache_ttl_seconds"], 0)
	assert.InDelta(t, 400_000, line["prompt_tokens"], 0)
	assert.InDelta(t, 401_000, line["estimated_input_tokens"], 0)
}

func TestFreshCacheViewFallsBackToTheProviderTTLWhenNoneWasRecorded(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	view := FreshCacheView(SwitchInput{
		Fresh: "claude-sonnet-4-6", Now: now, MessageCount: 12,
		ModelCaches: sessionpin.ModelCaches{"claude-sonnet-4-6": {ServedAt: now.Add(-time.Minute), PromptTokens: 9_000, MessageCount: 10}},
	})
	assert.True(t, view.Known)
	assert.Equal(t, 2, view.MessagesSinceServed)
	assert.Equal(t, time.Minute, view.SinceServed)
	assert.Positive(t, view.TTL)
	assert.Equal(t, SwitchCacheView{}, FreshCacheView(SwitchInput{Fresh: "claude-sonnet-4-6", Now: now}))
}

func TestLogSwitchShadowPricesTheRewriteAtTheRequestedWriteTTL(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	evictionAt := func(ttl time.Duration) float64 {
		var logs bytes.Buffer
		ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
		stayPin := sessionpin.Pin{
			Model: "claude-sonnet-4-6", Provider: providers.ProviderAnthropic, LastServedModel: "claude-sonnet-4-6", LastTurnEndedAt: now.Add(-20 * time.Second),
			LastInputTokens: 1_000, LastCachedReadTokens: 299_000,
		}
		gate := continuity.CostGateInput{Fresh: router.Decision{Model: "claude-opus-4-8"}, EstimatedInputTokens: 300_000}
		logSwitchShadow(ctx, switchShadowTurn{StayPin: stayPin, Gate: gate, Now: now}, TurnRoute{cacheShape: TurnCacheShape{MessageCount: 120, CacheWriteTTL: ttl}})
		var line map[string]any
		require.NoError(t, json.Unmarshal(logs.Bytes(), &line))
		eviction, ok := line["eviction_usd"].(float64)
		require.True(t, ok)
		return eviction
	}

	assert.Less(t, evictionAt(5*time.Minute), evictionAt(time.Hour))
}

func TestLogSwitchShadowHoldsTheUpgradeTheHMMGateWasNotConfidentIn(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
	stayPin := sessionpin.Pin{Model: "claude-haiku-5-5", Provider: providers.ProviderAnthropic, LastServedModel: "claude-haiku-5-5", LastTurnEndedAt: now.Add(-20 * time.Second)}
	shadow := switchShadowTurn{
		StayPin: stayPin,
		Gate:    continuity.CostGateInput{Fresh: router.Decision{Model: "claude-opus-5-5"}, EstimatedInputTokens: 10_000},
		Served:  continuity.CostGateServe{UpgradeConfidence: 0.3, UpgradeConfidenceLow: true},
		Now:     now,
	}

	logSwitchShadow(ctx, shadow, TurnRoute{Decision: router.Decision{Model: "claude-haiku-5-5"}})

	var line map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &line))
	assert.Equal(t, "claude-haiku-5-5", line["would_serve"])
	assert.Equal(t, string(SwitchReasonUpgradeConfidenceLow), line["would_reason"])
	assert.Equal(t, false, line["would_diverge"])
	assert.InDelta(t, 0.3, line["upgrade_confidence"], 1e-6)
	assert.Equal(t, true, line["upgrade_confidence_low"])
}

func TestLogSwitchShadowLogsTheCurrentModelDecideSwitchUsed(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
	shadow := switchShadowTurn{
		Gate: continuity.CostGateInput{
			ActivePin: sessionpin.Pin{Model: "claude-sonnet-5-5"},
			Fresh:     router.Decision{Model: "claude-opus-5-5"}, EstimatedInputTokens: 10_000,
		},
		Now: now,
	}

	logSwitchShadow(ctx, shadow, TurnRoute{Decision: router.Decision{Model: "claude-opus-5-5"}, PriorServedModel: "claude-sonnet-5-5", PinTier: "hmm_fresh_unpinned"})

	var line map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &line))
	assert.Equal(t, string(SwitchReasonFirstTurn), line["would_reason"])
	assert.Empty(t, line["current_model"])
}

func TestLogSwitchShadowMarksCacheStateFromASiblingThread(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
	shadow := switchShadowTurn{
		StayPin: sessionpin.Pin{Model: "gpt-6-luna", Provider: providers.ProviderOpenAI, LastServedModel: "gpt-6-luna", LastTurnEndedAt: now.Add(-20 * time.Second)},
		Gate: continuity.CostGateInput{
			Fresh: router.Decision{Model: "gpt-6.1-sol"}, EstimatedInputTokens: 40_000,
			HMMHistory: sessionpin.Pin{ModelCaches: sessionpin.ModelCaches{
				"gpt-6.1-sol": {ServedAt: now.Add(-time.Minute), PromptTokens: 790_000, MessageCount: 590},
			}},
		},
		Served: continuity.CostGateServe{UpgradeConfidence: 0.9},
		Now:    now,
	}

	logSwitchShadow(ctx, shadow, TurnRoute{Decision: router.Decision{Model: "gpt-6.1-sol"}, cacheShape: TurnCacheShape{MessageCount: 40}})

	var line map[string]any
	require.NoError(t, json.Unmarshal(logs.Bytes(), &line))
	assert.Equal(t, false, line["fresh_cache_known"])
	assert.Equal(t, true, line["fresh_cache_other_thread"])
	assert.InDelta(t, 0, line["fresh_messages_since_served"], 0)
}

func TestLogSwitchShadowKeepsAPrefixCacheWarmAcrossARouterTrim(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	logLine := func(prefixBroken bool) map[string]any {
		var logs bytes.Buffer
		ctx := observability.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&logs, nil)))
		shadow := switchShadowTurn{
			StayPin: sessionpin.Pin{Model: "gpt-6-luna", Provider: providers.ProviderOpenAI, LastServedModel: "gpt-6-luna", LastTurnEndedAt: now.Add(-20 * time.Second), LastInputTokens: 300_000},
			Gate: continuity.CostGateInput{
				Fresh: router.Decision{Model: "gpt-6.1-sol"}, EstimatedInputTokens: 300_000, PrefixBroken: prefixBroken,
				HMMHistory: sessionpin.Pin{ModelCaches: sessionpin.ModelCaches{
					"gpt-6-luna":  {ServedAt: now.Add(-20 * time.Second), PromptTokens: 300_000, MessageCount: 400},
					"gpt-6.1-sol": {ServedAt: now.Add(-2 * time.Minute), PromptTokens: 290_000, MessageCount: 390},
				}},
			},
			Served: continuity.CostGateServe{UpgradeConfidence: 0.9},
			Now:    now,
		}
		logSwitchShadow(ctx, shadow, TurnRoute{Decision: router.Decision{Model: "gpt-6-luna"}, cacheShape: TurnCacheShape{MessageCount: 404}})
		var line map[string]any
		require.NoError(t, json.Unmarshal(logs.Bytes(), &line))
		return line
	}

	trimmed, untrimmed := logLine(true), logLine(false)

	assert.Equal(t, true, trimmed["prefix_broken"])
	assert.Equal(t, false, trimmed["cache_cold"])
	assert.InDelta(t, untrimmed["eviction_usd"], trimmed["eviction_usd"], 1e-9)
	assert.Equal(t, untrimmed["would_reason"], trimmed["would_reason"])
}
