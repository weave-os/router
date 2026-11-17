package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"weave-os/router/internal/router/turntype"
	"weave-os/router/internal/translate"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Synthetic stand-in for a Claude Code auto-mode stage-2 security-monitor
// call: a long system prompt, a single user transcript message, a large
// max_tokens budget and no tools or stop sequences.
const classifierStage2SystemPrompt = `You are a security monitor for an autonomous coding agent. Review the transcript and score the latest action. Respond with <severity>N</severity> only.`

func classifierStage2Body(thinking string) string {
	body := `{"model":"claude-sonnet-5-5","max_tokens":10240,`
	if thinking != "" {
		body += `"thinking":` + thinking + `,`
	}
	return body + `"system":[{"type":"text","text":"x-anthropic-billing-header: synthetic"},{"type":"text","text":"` + classifierStage2SystemPrompt + `"}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n"},{"type":"text","text":"{\"user\":\"run node a.js\"}\n{\"Bash\":\"node a.js\"}\n"},{"type":"text","text":"</transcript>\n"}]}],` +
		`"metadata":{"user_id":"synthetic-user"}}`
}

const classifierClientBeta = "claude-code-20250219,context-management-2025-06-27"

func runClassifierBypass(t *testing.T, body, respBody string) (*bypassFakeProvider, *bypassCaptureTelemetry) {
	t.Helper()
	upstream := &bypassFakeProvider{respBody: respBody}
	svc := newBypassService(upstream)
	svc.anthropicServerSideFallback = true
	sink := newBypassCaptureTelemetry()
	svc.telemetry = sink

	env, err := translate.ParseAnthropic([]byte(body))
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+classifierTestSubToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", classifierClientBeta)
	ctx := context.WithValue(context.Background(), InstallationIDContextKey{}, uuid.New().String())

	require.NoError(t, svc.bypassToAnthropic(ctx, env, env.RoutingFeatures(), false, time.Now(), "req-cls-s2", "ext-cls-s2", turntype.Classifier, reasonClassifierPassthrough, req, rec))
	return upstream, sink
}

// A classifier turn on the subscription bypass lane reaches Anthropic exactly
// as the subscription passthrough arm would send it: no server-side fallback,
// no thinking/effort rewrite, no added cache breakpoint, no max_tokens change.
// An empty or refused verdict here makes the client fail closed and deny the
// tool call, so the router must not alter the monitor's request.
func TestBypass_ClassifierTurn_ForwardedLikePassthrough(t *testing.T) {
	for name, thinking := range map[string]string{
		"no_thinking":       "",
		"thinking_disabled": `{"type":"disabled"}`,
		"thinking_enabled":  `{"type":"enabled","budget_tokens":1024}`,
	} {
		t.Run(name, func(t *testing.T) {
			body := classifierStage2Body(thinking)
			upstream, _ := runClassifierBypass(t, body, `{}`)

			env, err := translate.ParseAnthropic([]byte(body))
			require.NoError(t, err)
			in := http.Header{}
			in.Set("anthropic-version", "2023-06-01")
			in.Set("anthropic-beta", classifierClientBeta)
			want, err := env.PrepareAnthropicPassthrough(in)
			require.NoError(t, err)

			assert.JSONEq(t, string(want.Body), string(upstream.capturedBody))
			assert.Equal(t, want.Headers.Get("anthropic-beta"), upstream.capturedHdr.Get("anthropic-beta"))
			assert.NotContains(t, string(upstream.capturedBody), `"fallbacks"`)
			assert.NotContains(t, string(upstream.capturedBody), `"output_config"`)
			assert.NotContains(t, string(upstream.capturedBody), `"cache_control"`)
			assert.NotContains(t, upstream.capturedHdr.Get("anthropic-beta"), "server-side-fallback")
		})
	}
}

// A refused or empty non-streaming classifier verdict on the bypass lane is
// persisted with its stop_reason so it is countable in telemetry.
func TestBypass_ClassifierTurn_RecordsStopReason(t *testing.T) {
	for name, tc := range map[string]struct {
		resp, wantStop, wantClass string
	}{
		"refusal": {
			resp:      `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"stop_reason":"refusal","usage":{"input_tokens":10,"output_tokens":0}}`,
			wantStop:  "refusal",
			wantClass: string(TurnErrorRefusal),
		},
		"end_turn": {
			resp:     `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"<severity>5</severity>"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":7}}`,
			wantStop: "end_turn",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, sink := runClassifierBypass(t, classifierStage2Body(""), tc.resp)
			select {
			case <-sink.notify:
			case <-time.After(2 * time.Second):
				t.Fatal("classifier bypass never persisted a telemetry row")
			}
			sink.mu.Lock()
			defer sink.mu.Unlock()
			require.Len(t, sink.rows, 1)
			row := sink.rows[0]
			require.NotNil(t, row.StopReason)
			assert.Equal(t, tc.wantStop, *row.StopReason)
			require.NotNil(t, row.UpstreamFinishReason)
			assert.Equal(t, tc.wantStop, *row.UpstreamFinishReason)
			assert.Equal(t, tc.wantClass, string(row.ErrorClass))
		})
	}
}
