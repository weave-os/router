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

	logSwitchShadow(ctx, stayPin, gate, now, res)

	assert.Equal(t, "claude-sonnet-4-6", res.Decision.Model)
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
