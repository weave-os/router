package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/providers/anthropic"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	safeguardsBeta   = "claude-code-20250219,dangerous-tool-use-2026-09-03"
	safeguardsField  = `{"type":"dangerous_tool_use","classifier_context":{"v":1,"permission_mode":"auto","rules":{"allow":["Bash(node:*)"],"deny":[],"ask":[]}}}`
	safeguardsToolID = "toolu_synthetic01"
	safeguardsResult = `[{"type":"dangerous_tool_use","status":{"type":"available","tool_uses":{"` + safeguardsToolID + `":{"type":"evaluated","outcome":"not_flagged","cited_rule":null}}}}]`
)

func safeguardsUpstreamSSE(model string) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"" + model + "\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + safeguardsToolID + "\",\"name\":\"Bash\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"node a.js\\\"}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null,\"safeguard_results\":" + safeguardsResult + "},\"usage\":{\"output_tokens\":12}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func safeguardsUpstreamJSON(model string) string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"tool_use","id":"` + safeguardsToolID + `","name":"Bash","input":{"command":"node a.js"}}],"stop_reason":"tool_use","safeguard_results":` + safeguardsResult + `,"usage":{"input_tokens":10,"output_tokens":12}}`
}

// Claude Code's server-side auto-mode review rides on the main turn: the
// request carries a top-level `safeguards` field plus the dangerous-tool-use
// beta, and the response carries `safeguard_results` keyed by tool-use id.
// When results are missing the client falls back to its own classifier calls,
// so every Anthropic-served turn must keep all three intact on both arms.
func TestAnthropicServedTurn_KeepsServerSideAutoModeReview(t *testing.T) {
	for _, arm := range []struct {
		name  string
		arm   auth.BlindExperimentArm
		model string
	}{
		{"router_on_haiku", auth.BlindExperimentArmRouterOn, "claude-haiku-5-5"},
		{"router_on_opus", auth.BlindExperimentArmRouterOn, "claude-opus-5-5"},
		{"passthrough", auth.BlindExperimentArmPassthrough, "claude-sonnet-5-5"},
	} {
		for _, stream := range []bool{true, false} {
			name := arm.name + map[bool]string{true: "_stream", false: "_json"}[stream]
			t.Run(name, func(t *testing.T) {
				var upstreamBody []byte
				var upstreamBeta string
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamBody, _ = io.ReadAll(r.Body)
					upstreamBeta = r.Header.Get("anthropic-beta")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, safeguardsUpstreamSSE(arm.model))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, safeguardsUpstreamJSON(arm.model))
				}))
				defer srv.Close()

				client := anthropic.NewClient("synthetic-api-key", srv.URL)
				svc := withHMMPorts(NewService(staticRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: arm.model, Reason: "test"}}, dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: client}), nil, nil, false, providers.ProviderAnthropic, arm.model, nil)).
					WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
				ctx := context.WithValue(context.Background(), requestcontext.InstallationIDContextKey{}, "inst-safeguards")
				ctx = context.WithValue(ctx, requestcontext.APIKeyIDContextKey{}, "key-safeguards")
				ctx = context.WithValue(ctx, auth.BlindExperimentContextKey{}, auth.BlindExperimentState{Active: true, Enabled: true, Arm: arm.arm})

				streamField := "false"
				if stream {
					streamField = "true"
				}
				body := `{"model":"claude-sonnet-5-5","max_tokens":1024,"stream":` + streamField + `,"safeguards":[` + safeguardsField + `],` +
					`"tools":[{"name":"Bash","description":"run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],` +
					`"messages":[{"role":"user","content":"run node a.js"}]}`
				req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", strings.NewReader(body))
				req.Header.Set("anthropic-version", "2023-06-01")
				req.Header.Set("anthropic-beta", safeguardsBeta)
				rec := httptest.NewRecorder()

				require.NoError(t, svc.ProxyMessages(ctx, []byte(body), rec, req))

				var upstream map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(upstreamBody, &upstream))
				assert.JSONEq(t, "["+safeguardsField+"]", string(upstream["safeguards"]))
				assert.Contains(t, upstreamBeta, "dangerous-tool-use-2026-09-03")
				assert.Contains(t, rec.Body.String(), `"safeguard_results"`)
				assert.Contains(t, rec.Body.String(), `"`+safeguardsToolID+`":{"type":"evaluated"`)
				assert.Contains(t, rec.Body.String(), `"id":"`+safeguardsToolID+`"`)
			})
		}
	}
}
