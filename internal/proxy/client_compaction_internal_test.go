package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"weave-os/router/internal/router"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClientRecoveryIsLimitedToInitialAssistantTurns(t *testing.T) {
	for _, tt := range []struct {
		turns int
		want  bool
	}{{0, true}, {3, true}, {4, false}} {
		t.Run(fmt.Sprintf("after_%d_assistant_turns", tt.turns), func(t *testing.T) {
			messages := []map[string]any{{"role": "user", "content": clientCompactContinuationPrefix + " Summary: continue the verified fix."}}
			for i := 0; i < tt.turns; i++ {
				messages = append(messages, map[string]any{"role": "assistant", "content": "Useful progress."}, map[string]any{"role": "user", "content": "Continue."})
			}
			body, err := json.Marshal(map[string]any{
				"model": budgetTestFable, "messages": messages,
				"tools": []any{map[string]any{"name": "Read", "input_schema": map[string]any{"type": "object"}}},
			})
			require.NoError(t, err)
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			applied, err := applyClientCompactionRecovery(context.Background(), env, smallClientBudget(), turntype.MainLoop, conversationMessagesForRouting(env))
			require.NoError(t, err)
			assert.Equal(t, tt.want, applied)
			prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: budgetTestFable, Capabilities: router.Lookup(budgetTestFable)})
			require.NoError(t, err)
			assert.Equal(t, tt.want, gjson.GetBytes(prepared.Body, "tool_choice.disable_parallel_tool_use").Bool())
			assert.Equal(t, len(messages), env.RoutingFeatures(false).MessageCount)
		})
	}
}

func TestClientRecoveryLeavesUnknownBudgetsAndUtilityTurnsUntouched(t *testing.T) {
	body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":%q}],"tools":[{"name":"Read","input_schema":{"type":"object"}}]}`, budgetTestFable, clientCompactContinuationPrefix))
	for _, tt := range []struct {
		name   string
		budget router.ClientBudget
		turn   turntype.TurnType
	}{
		{"unknown budget", router.ClientBudget{}, turntype.MainLoop},
		{"ambiguous beta", router.ClientBudget{Evidence: router.ClientBudgetAmbiguousLongContext}, turntype.ToolResult},
		{"probe", smallClientBudget(), turntype.Probe},
		{"subagent", smallClientBudget(), turntype.SubAgentDispatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env, err := translate.ParseAnthropic(body)
			require.NoError(t, err)
			before, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: budgetTestFable, Capabilities: router.Lookup(budgetTestFable)})
			require.NoError(t, err)
			applied, err := applyClientCompactionRecovery(context.Background(), env, tt.budget, tt.turn, conversationMessagesForRouting(env))
			require.NoError(t, err)
			assert.False(t, applied)
			after, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: budgetTestFable, Capabilities: router.Lookup(budgetTestFable)})
			require.NoError(t, err)
			assert.Equal(t, string(before.Body), string(after.Body))
		})
	}
}

func TestClientRecoverySeesContinuationAfterHandoverRewrite(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"model": budgetTestFable,
		"messages": []map[string]any{{
			"role": "assistant", "content": "Handover summary of prior work.",
		}},
		"tools": []any{map[string]any{"name": "Read", "input_schema": map[string]any{"type": "object"}}},
	})
	require.NoError(t, err)
	env, err := translate.ParseAnthropic(body)
	require.NoError(t, err)
	history := []router.ConversationMessage{{
		Role: "user", Text: clientCompactContinuationPrefix + " Summary: continue the verified fix.",
	}}
	applied, err := applyClientCompactionRecovery(context.Background(), env, smallClientBudget(), turntype.MainLoop, history)
	require.NoError(t, err)
	assert.True(t, applied)
	prepared, err := env.PrepareAnthropic(nil, translate.EmitOptions{TargetModel: budgetTestFable, Capabilities: router.Lookup(budgetTestFable)})
	require.NoError(t, err)
	assert.True(t, gjson.GetBytes(prepared.Body, "tool_choice.disable_parallel_tool_use").Bool())
}
