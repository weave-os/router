package conversation_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"weave-os/router/internal/conversation"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStripClientHistoryRetiredBetaNoticeKeepsThinkingHistory(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-5",
		"max_tokens": 128,
		"messages": []any{
			map[string]any{"role": "user", "content": "/beta\nWhich router is active?"},
			map[string]any{"role": "assistant", "content": "✦ **Weave Router** → Select a router with `wv router set-model weave-hmm-beta`, or `wv router set-model weave-auto`, then start a new session."},
			map[string]any{"role": "user", "content": "continue"},
		},
	})
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)

	ctx, err := conversation.StripClientHistory(context.Background(), slog.New(slog.DiscardHandler), env, "anthropic")
	require.NoError(t, err)

	assert.False(t, conversation.BetaArtifactHistoryFromContext(ctx),
		"a routing-model notice did not switch models, so signed thinking must survive")
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: "claude-sonnet-5"})
	require.NoError(t, err)
	assert.NotContains(t, string(prepared.Body), "Select a router")
	assert.Contains(t, string(prepared.Body), "Which router is active?")
}
