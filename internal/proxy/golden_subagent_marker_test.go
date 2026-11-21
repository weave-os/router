package proxy_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

const goldenSubagentBillingHeader = "x-anthropic-billing-header: cc_version=2.1.141.bb8; cc_entrypoint=cli; cc_is_subagent=true; cch=00000;"

func goldenClaudeCodeSubagentBody(stream bool) string {
	return fmt.Sprintf(`{"model":"claude-sonnet-4-5","max_tokens":4096,"stream":%t,`+
		`"system":[{"type":"text","text":"%s\nYou are a synthetic search agent."}],`+
		`"messages":[{"role":"user","content":"Find the synthetic parser"}]}`, stream, goldenSubagentBillingHeader)
}

func goldenCodexSubagentBody(stream bool) string {
	return fmt.Sprintf(`{"model":"gpt-5","stream":%t,"input":[`+
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"Repair a synthetic parser"}]},`+
		`{"type":"agent_message","author":"/root","recipient":"/root/reviewer","content":[{"type":"input_text","text":"Message Type: NEW_TASK\nTask name: /root/reviewer\nSender: /root\nPayload:\nReview the synthetic parser"}]}]}`, stream)
}

var goldenSubagentTypeHeader = map[string]string{"x-weave-subagent-type": "general-purpose"}

// goldenSubagentMarkerScenarios pin that subagent turns carry no model-selection
// marker on any surface, including after a sibling rescue; clients render
// subagent output inline and hand it back to the parent as a tool result.
func goldenSubagentMarkerScenarios() []goldenScenario {
	var out []goldenScenario
	for _, stream := range []bool{true, false} {
		suffix := "_" + streamName(stream) + "_subagent_hides_model_marker"
		out = append(out,
			goldenScenario{name: ingressAnthropic + suffix, ingress: ingressAnthropic, decisions: []router.Decision{goldenAnthropicDecision},
				upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(stream, "found it")}},
				requests:  []goldenRequest{{body: goldenClaudeCodeSubagentBody(stream), decorateContext: goldenClaudeCodeCtx}}},
			goldenScenario{name: ingressOpenAI + suffix, ingress: ingressOpenAI, decisions: []router.Decision{goldenCompatDecision},
				upstreams: map[string][]goldenStep{providers.ProviderFireworks: {compatOK(stream, "found it")}},
				requests:  []goldenRequest{{body: goldenOpenAIBody(stream), headers: goldenSubagentTypeHeader}}},
			goldenScenario{name: ingressResponses + "_" + streamName(stream) + "_codex_subagent_hides_model_marker", ingress: ingressResponses,
				decisions: []router.Decision{goldenAnthropicDecision},
				upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {anthropicOK(stream, "found it")}},
				requests:  []goldenRequest{{body: goldenCodexSubagentBody(stream), headers: goldenCodexHeaders}}},
			goldenScenario{name: ingressGemini + suffix, ingress: ingressGemini, decisions: []router.Decision{goldenGeminiDecision},
				upstreams: map[string][]goldenStep{providers.ProviderGoogle: {geminiOK(stream, "found it")}},
				requests:  []goldenRequest{{body: goldenGeminiBody(), path: goldenGeminiPath(stream), headers: goldenSubagentTypeHeader}}},
		)
	}
	out = append(out, goldenScenario{name: ingressAnthropic + "_stream_subagent_sibling_rescue_hides_model_marker", ingress: ingressAnthropic,
		decisions: []router.Decision{goldenRescueDecision},
		upstreams: map[string][]goldenStep{
			providers.ProviderAnthropic: {goldenUpstreamError(529, goldenOverloadBody)},
			providers.ProviderFireworks: {compatOK(true, "found it")}},
		requests: []goldenRequest{{body: goldenClaudeCodeSubagentBody(true), decorateContext: goldenClaudeCodeCtx}}})
	return out
}

func TestGoldenSubagentTurnsHideModelSelectionMarker(t *testing.T) {
	for _, sc := range goldenSubagentMarkerScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			h := newGoldenHarness(t, sc)
			for _, req := range sc.requests {
				trace, err := json.Marshal(h.serve(t, sc.ingress, req))
				require.NoError(t, err)
				require.Contains(t, string(trace), "found it")
				require.NotContains(t, string(trace), "Weave Router")
			}
		})
	}
}
