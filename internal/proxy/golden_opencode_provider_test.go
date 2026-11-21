package proxy_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

const goldenOpenCodeHeadersAgent = "opencode/latest/2.3.2/cli"

const goldenOpenCodeLead = "Use plain language."
const goldenOpenCodeLate = "Summarize the result in one sentence."

const goldenOpenCodeResponses = `{"model":"auto","stream":true,"instructions":"Help with the example.","input":[` +
	`{"type":"message","role":"developer","content":"` + goldenOpenCodeLead + `"},` +
	`{"type":"message","role":"user","content":[{"type":"input_text","text":"What is in the report?"}]},` +
	`{"type":"function_call","call_id":"call_report","name":"read_report","arguments":"{}"},` +
	`{"type":"function_call_output","call_id":"call_report","output":"The report has three pages."},` +
	`{"type":"message","role":"developer","content":[{"type":"input_text","text":"` + goldenOpenCodeLate + `"}]}],` +
	`"tools":[{"type":"function","name":"read_report","parameters":{"type":"object"}}]}`

const goldenOpenCodeOpening = `{"model":"auto","stream":true,"input":[` +
	`{"role":"developer","content":"` + goldenOpenCodeLead + `"},` +
	`{"role":"user","content":"Say hello."}]}`

const goldenChatDeveloper = `{"model":"auto","stream":false,"messages":[` +
	`{"role":"developer","content":[{"type":"text","text":"` + goldenOpenCodeLead + `"}]},` +
	`{"role":"user","content":"What is in the report?"},` +
	`{"role":"assistant","content":"It has three pages."},` +
	`{"role":"developer","content":"` + goldenOpenCodeLate + `"}]}`

func goldenOpenCodeSpendCap(ctx context.Context) context.Context {
	return billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlySpendCapReached)
}

func goldenStrictClaude(stream bool) goldenStep {
	step := anthropicOK(stream, "example response")
	step.rejectBody = func(body []byte) error {
		for _, role := range gjson.GetBytes(body, "messages.#.role").Array() {
			if role.Str != "user" && role.Str != "assistant" && role.Str != "system" {
				return goldenUpstreamError(http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: Unexpected role \"developer\"."}}`).err
			}
		}
		return nil
	}
	return step
}

func goldenOpenCodeProviderScenarios() []goldenScenario {
	headers := map[string]string{"User-Agent": goldenOpenCodeHeadersAgent, "X-OpenCode-Session": "ses_golden"}
	bothSeatsAtCap := chainCtx(goldenOpenCodeSpendCap, goldenCodexAndClaudeSeatsCtx)
	codexReply := goldenOK("text/event-stream", goldenResponsesStream(goldenCodexModel, "example response"))
	claude := func(name, ingress, body string, stream bool) goldenScenario {
		return goldenScenario{
			name: name, ingress: ingress, decisions: []router.Decision{goldenAnthropicDecision},
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {goldenStrictClaude(stream)}},
			requests:  []goldenRequest{{body: body, headers: headers}},
		}
	}
	claudeCap := claude("opencode_claude_seat_at_spend_cap", ingressResponses, goldenOpenCodeResponses, true)
	claudeCap.configure = withCodexAndClaudeSeats
	claudeCap.requests[0].decorateContext = bothSeatsAtCap
	claudeCap.credentials = map[string]string{"golden-seat-claude-seat": "claude-seat"}
	claudeBYOK := claude("opencode_claude_key_at_spend_cap", ingressResponses, goldenOpenCodeOpening, true)
	claudeBYOK.requests[0].decorateContext = chainCtx(goldenOpenCodeSpendCap,
		withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityKeyFirst)))
	claudeBYOK.credentials = map[string]string{goldenBYOKAnthropic: "claude-key"}
	subscriptionModelsOnly := func(model string) func(context.Context) context.Context {
		return chainCtx(bothSeatsAtCap, func(ctx context.Context) context.Context {
			return proxy.WithSubscriptionStateModels(ctx, []string{model}, []string{model})
		})
	}
	forceModelHeaders := func(model string) map[string]string {
		return map[string]string{"User-Agent": goldenOpenCodeHeadersAgent, "X-OpenCode-Session": "ses_golden", requestcontext.ForceModelHeader: model}
	}
	return []goldenScenario{
		claude("opencode_claude_opening", ingressResponses, goldenOpenCodeOpening, true),
		claude("opencode_claude_buffered_tool_history", ingressResponses, strings.Replace(goldenOpenCodeResponses, `"stream":true`, `"stream":false`, 1), false),
		claude("opencode_chat_claude_developer_parts", ingressOpenAIHTTP, goldenChatDeveloper, false),
		claudeCap,
		claudeBYOK,
		{
			name: "opencode_switch_codex_to_claude_at_spend_cap", ingress: ingressResponses,
			decisions: []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel), goldenAnthropicDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderOpenAI:    {codexReply},
				providers.ProviderAnthropic: {goldenStrictClaude(true)},
			},
			requests: []goldenRequest{
				{body: goldenOpenCodeOpening, headers: forceModelHeaders(goldenCodexModel), decorateContext: subscriptionModelsOnly(goldenCodexModel)},
				{body: goldenOpenCodeResponses, headers: forceModelHeaders(goldenAnthropicModel), decorateContext: subscriptionModelsOnly(goldenAnthropicModel)},
			},
			configure:   withCodexAndClaudeSeats,
			credentials: map[string]string{"golden-seat-codex-seat": "codex-seat", "golden-seat-claude-seat": "claude-seat"},
		},
		{
			name: "opencode_codex_seat_at_spend_cap", ingress: ingressResponses,
			decisions:   []router.Decision{decision(providers.ProviderOpenAI, goldenCodexModel)},
			upstreams:   map[string][]goldenStep{providers.ProviderOpenAI: {codexReply}},
			requests:    []goldenRequest{{body: goldenOpenCodeResponses, headers: headers, decorateContext: bothSeatsAtCap}},
			configure:   withCodexAndClaudeSeats,
			credentials: map[string]string{"golden-seat-codex-seat": "codex-seat"},
		},
		{
			name: "opencode_gemini_tool_history", ingress: ingressResponses,
			decisions: []router.Decision{goldenGeminiDecision},
			upstreams: map[string][]goldenStep{providers.ProviderGoogle: {geminiOK(true, "example response")}},
			requests:  []goldenRequest{{body: goldenOpenCodeResponses, headers: headers}},
		},
		{
			name: "opencode_compat_chat_tool_history", ingress: ingressResponses,
			decisions: []router.Decision{goldenCompatDecision},
			upstreams: map[string][]goldenStep{providers.ProviderFireworks: {compatOK(true, "example response")}},
			requests:  []goldenRequest{{body: goldenOpenCodeResponses, headers: headers}},
		},
	}
}

func TestOpenCodeProviderReplay(t *testing.T) {
	t.Parallel()
	for _, sc := range goldenOpenCodeProviderScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			h := newGoldenHarness(t, sc)
			for _, req := range sc.requests {
				trace := h.serve(t, sc.ingress, req)
				if model := req.headers[requestcontext.ForceModelHeader]; model != "" {
					require.Len(t, trace.Attempts, 1)
					require.Equal(t, model, trace.Attempts[0].Model)
				}
				checkOpenCodeProviderReplay(t, trace, req.body, sc.credentials != nil)
			}
		})
	}
}

func checkOpenCodeProviderReplay(t *testing.T, trace goldenRequestTrace, inbound string, atCap bool) {
	t.Helper()
	require.Equal(t, http.StatusOK, trace.Response.Status)
	if trace.Stream {
		require.NotEmpty(t, trace.Response.Events)
		require.Equal(t, "response.completed", trace.Response.Events[len(trace.Response.Events)-1].Event)
	}
	require.Len(t, trace.Attempts, 1)
	attempt := trace.Attempts[0]
	require.Equal(t, "ok", attempt.Outcome)
	raw, err := json.Marshal(attempt.Body)
	require.NoError(t, err)
	require.Contains(t, string(raw), goldenOpenCodeLead)
	if strings.Contains(inbound, goldenOpenCodeLate) {
		require.Contains(t, string(raw), goldenOpenCodeLate)
	}
	switch attempt.Provider {
	case providers.ProviderAnthropic:
		require.Contains(t, gjson.GetBytes(raw, "system.#.text").Raw, goldenOpenCodeLead)
		require.NotContains(t, gjson.GetBytes(raw, "messages.#.role").Raw, "developer")
		if strings.Contains(inbound, goldenOpenCodeLate) {
			require.Contains(t, gjson.GetBytes(raw, "messages").Raw, goldenOpenCodeLate)
		}
	case providers.ProviderGoogle:
		system := gjson.GetBytes(raw, "systemInstruction.parts.0.text").Str
		require.Contains(t, system, goldenOpenCodeLead)
		require.Contains(t, system, goldenOpenCodeLate)
	case providers.ProviderOpenAI:
		require.Equal(t, "responses", attempt.Endpoint)
		require.Contains(t, gjson.GetBytes(raw, "input.#.role").Raw, "developer")
	case providers.ProviderFireworks:
		require.Contains(t, gjson.GetBytes(raw, "messages.#.role").Raw, "developer")
	}
	if atCap {
		require.NotNil(t, attempt.Resolved)
		require.NotEqual(t, "deployment", attempt.Resolved.Label)
		require.Len(t, trace.Billing, 1)
		debit, err := json.Marshal(trace.Billing[0])
		require.NoError(t, err)
		require.Equal(t, "byok_fee", gjson.GetBytes(debit, "FeeEntryType").Str)
		require.Zero(t, gjson.GetBytes(debit, "DeltaUsdMicros").Int())
		require.Zero(t, gjson.GetBytes(debit, "FeeUsdMicros").Int())
		response, err := json.Marshal(trace.Response)
		require.NoError(t, err)
		require.Contains(t, string(response), "spend cap has been reached")
		if attempt.Resolved.Label == "claude-key" {
			require.Contains(t, string(response), "your own provider API key")
		} else if attempt.Provider == providers.ProviderAnthropic {
			require.Contains(t, string(response), "Anthropic subscription")
			require.NotContains(t, string(response), "Codex) subscription")
		} else {
			require.Contains(t, string(response), "ChatGPT (Codex) subscription")
		}
	}
}
