package proxy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/observability"
	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"
)

func TestSlowStartNotice_CodexSubagentContinuations(t *testing.T) {
	for _, test := range []struct {
		name       string
		agentPath  string
		toolResult bool
		wantTurn   turntype.TurnType
		wantNotice bool
	}{
		{"parent prompt", "/root", false, turntype.MainLoop, true},
		{"parent tool result", "/root", true, turntype.ToolResult, true},
		{"child prompt", "/root/reviewer", false, turntype.SubAgentDispatch, false},
		{"child tool result", "/root/reviewer", true, turntype.SubAgentDispatch, false},
		{"nested child tool result", "/root/reviewer/checker", true, turntype.SubAgentDispatch, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, envelope := codexRoutingEnvelope(t, test.agentPath, test.toolResult)
			ctx = observability.WithClientSessionID(ctx, ClientIdentityFrom(ctx).SessionID)
			features := envelope.RoutingFeatures(false)
			turn := turntype.Detect(envelope, features, subAgentHintForRequest(ctx, ""), openCodeCaller(ClientIdentityFrom(ctx)))
			require.Equal(t, test.wantTurn, turn)

			tracker, now := slowStartAfterBaseline(t)
			trace := slowStartTrace(now.Add(-41*time.Second), now.Add(-41*time.Second))
			ctx = context.WithValue(ctx, attemptTraceContextKey{}, trace)
			muteSlowStartFor(ctx, turnLoopResult{TurnType: turn})
			_, shown := slowStartNotice(ctx, trace, tracker, now)
			assert.Equal(t, test.wantNotice, shown)

			if !test.wantNotice {
				parentTrace := slowStartTrace(now.Add(-41*time.Second), now.Add(-41*time.Second))
				_, parentShown := slowStartNotice(ctx, parentTrace, tracker, now)
				assert.True(t, parentShown, "a hidden child notice must not consume the parent's cooldown")
			}
		})
	}
}

func TestSlowStartNotice_ReplayedCodexToolLoop(t *testing.T) {
	const notice = "\u2063\u2060\u2063\u2060↳ OpenAI took 41s to first output (usually ~3s; client tool wait excluded)\n\n"
	for _, test := range []struct {
		name       string
		role       translate.EscalationRole
		nextPrompt bool
		wantNotice bool
	}{
		{"same user turn on another worker", translate.EscalationRoleAssistant, false, false},
		{"next user prompt", translate.EscalationRoleAssistant, true, true},
		{"user quoting a notice", translate.EscalationRoleUser, false, true},
		{"tool quoting a notice", translate.EscalationRoleTool, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := []map[string]any{
				{"type": "message", "role": translate.EscalationRoleUser, "content": "Check the synthetic parser"},
				{"type": "message", "role": test.role, "content": []map[string]string{{"type": "output_text", "text": notice}}},
				{"type": "function_call", "call_id": "synthetic-read", "name": "read_file", "arguments": `{}`},
				{"type": "function_call_output", "call_id": "synthetic-read", "output": "Synthetic parser source"},
			}
			if test.nextPrompt {
				input = append(input, map[string]any{"type": "message", "role": translate.EscalationRoleUser, "content": "Now check the synthetic serializer"})
			}
			inbound, err := json.Marshal(map[string]any{"input": input})
			require.NoError(t, err)
			tracker, now := slowStartAfterBaseline(t)
			trace := slowStartTrace(now.Add(-41*time.Second), now.Add(-41*time.Second))
			trace.inbound = inbound
			ctx := slowStartCtx("synthetic-session", "synthetic-key", "codex/0.166.0")
			ctx = context.WithValue(ctx, ClientIdentityContextKey{}, ClientIdentity{ClientApp: ClientAppCodex, UserAgent: "codex/0.166.0"})
			_, shown := slowStartNotice(ctx, trace, tracker, now)
			assert.Equal(t, test.wantNotice, shown)
		})
	}
}

func TestSlowStartNotice_TurnVisibility(t *testing.T) {
	for _, test := range []struct {
		name       string
		turn       turnLoopResult
		wantNotice bool
	}{
		{"automatic subagent", turnLoopResult{TurnType: turntype.SubAgentDispatch}, false},
		{"hard-pinned subagent", turnLoopResult{TurnType: turntype.SubAgentDispatch, HardPinned: true}, false},
		{"hard-pinned compaction", turnLoopResult{TurnType: turntype.Compaction, HardPinned: true}, false},
		{"suggestion", turnLoopResult{TurnType: turntype.MainLoop, SuggestionMode: true}, false},
		{"recap", turnLoopResult{TurnType: turntype.Recap}, false},
		{"caller passthrough", turnLoopResult{TurnType: turntype.MainLoop, CallerModelPassthrough: true}, false},
		{"parent prompt", turnLoopResult{TurnType: turntype.MainLoop}, true},
		{"parent tool result", turnLoopResult{TurnType: turntype.ToolResult}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker, now := slowStartAfterBaseline(t)
			trace := slowStartTrace(now.Add(-41*time.Second), now.Add(-41*time.Second))
			ctx := context.WithValue(slowStartCtx("synthetic-session", "synthetic-key", "claude-cli/2.1.302 (external, cli)"), attemptTraceContextKey{}, trace)
			muteSlowStartFor(ctx, test.turn)
			_, shown := slowStartNotice(ctx, trace, tracker, now)
			assert.Equal(t, test.wantNotice, shown)
		})
	}
}
