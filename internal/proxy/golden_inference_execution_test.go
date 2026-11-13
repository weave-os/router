package proxy_test

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/proxy/usage"
	"weave-os/router/internal/router"
)

const (
	goldenRateLimitBody   = `{"error":{"message":"rate limited"}}`
	goldenServerErrorBody = `{"type":"error","error":{"type":"api_error","message":"unavailable"}}`
)

// goldenInferenceExecutionScenarios cover retry, rescue and cross-request
// arm bookkeeping that the per-ingress matrix does not.
func goldenInferenceExecutionScenarios() []goldenScenario {
	var out []goldenScenario
	for _, ingress := range []string{ingressAnthropic, ingressOpenAI} {
		for _, stream := range []bool{true, false} {
			out = append(out, goldenRetryScenarios(ingress, stream)...)
		}
		out = append(out, goldenArmScenarios(ingress)...)
	}
	out = append(out, goldenNativeCommitScenarios()...)
	return append(out, goldenCapabilityScenarios()...)
}

// goldenRetryScenarios pin same-binding retries and the single billing row
// a recovered operation writes.
func goldenRetryScenarios(ingress string, stream bool) []goldenScenario {
	prefix := ingress + "_" + streamName(stream) + "_"
	body := ingressBody(ingress, stream)
	return []goldenScenario{
		{name: prefix + "same_binding_retry_then_success_bills_once", ingress: ingress,
			decisions: []router.Decision{goldenAnthropicDecision},
			upstreams: map[string][]goldenStep{providers.ProviderAnthropic: {
				goldenUpstreamError(http.StatusServiceUnavailable, goldenServerErrorBody),
				anthropicOK(stream, "recovered")}},
			requests: []goldenRequest{{body: body}}},
		{name: prefix + "client_cancel_before_first_byte", ingress: ingress,
			decisions: []router.Decision{goldenCompatDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderFireworks:  {{cancel: true}},
				providers.ProviderOpenRouter: {compatOK(stream, "never")}},
			requests: []goldenRequest{{body: body}}},
	}
}

func withArmDemotion(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return proxy.WithGoldenClock(svc, h.clock.Now).WithRescuedFailureArmDemotion(true)
}

func withRateLimitCooldown(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return withArmDemotion(svc, h).WithTransientRateLimit(true, 60)
}

func withGoldenClock(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return proxy.WithGoldenClock(svc, h.clock.Now)
}

// goldenArmScenarios replay a session across requests so a rescued primary's
// demotion, cooldown or turn strike shows up in the next routing call.
func goldenArmScenarios(ingress string) []goldenScenario {
	prefix := ingress + "_stream_"
	body := ingressBody(ingress, true)
	rescue := []router.Decision{goldenRescueDecision, goldenCompatDecision}
	rescuedFrom := func(status int, body string) map[string][]goldenStep {
		return map[string][]goldenStep{
			providers.ProviderAnthropic: {goldenUpstreamError(status, body)},
			providers.ProviderFireworks: {compatOK(true, "rescued")}}
	}
	serverError := rescuedFrom(http.StatusServiceUnavailable, goldenServerErrorBody)
	rateLimited := rescuedFrom(http.StatusTooManyRequests, goldenRateLimitBody)
	return []goldenScenario{
		{name: prefix + "rescued_server_error_demotes_arm", ingress: ingress, decisions: rescue, upstreams: serverError,
			requests:  []goldenRequest{{body: body}, {body: body}},
			configure: withArmDemotion},
		{name: prefix + "rescued_overload_keeps_arm", ingress: ingress, decisions: rescue,
			upstreams: rescuedFrom(529, goldenOverloadBody),
			requests:  []goldenRequest{{body: body}, {body: body}},
			configure: withArmDemotion},
		{name: prefix + "rescued_rate_limit_cooldown_expires", ingress: ingress, decisions: rescue, upstreams: rateLimited,
			requests:  []goldenRequest{{body: body}, {body: body, advance: 30 * time.Second}, {body: body, advance: 31 * time.Second}},
			configure: withRateLimitCooldown},
		{name: prefix + "rescued_rate_limit_demotes_without_cooldown", ingress: ingress, decisions: rescue, upstreams: rateLimited,
			requests:  []goldenRequest{{body: body}, {body: body, advance: 2 * time.Minute}},
			configure: withArmDemotion},
	}
}

// goldenNativeCommitScenarios cut a native Anthropic stream on either side of
// the commit boundary and strike the rescued arm for the agent turn.
func goldenNativeCommitScenarios() []goldenScenario {
	prefix := ingressAnthropic + "_stream_"
	eventStreamHeaders := map[string]string{"Content-Type": "text/event-stream"}
	native := goldenAnthropicStream(goldenSiblingModel, "partial")
	rescue := []router.Decision{goldenRescueDecision, goldenCompatDecision}
	return []goldenScenario{
		{name: prefix + "native_cut_after_message_start_rescued", ingress: ingressAnthropic, decisions: rescue,
			upstreams: map[string][]goldenStep{
				providers.ProviderAnthropic: {{status: http.StatusOK, headers: eventStreamHeaders, frames: native[:1], err: errGoldenUpstreamReset}},
				providers.ProviderFireworks: {compatOK(true, "rescued")}},
			requests: []goldenRequest{{body: goldenAnthropicBody(true)}}},
		{name: prefix + "native_cut_after_content_with_rescue_candidates", ingress: ingressAnthropic, decisions: rescue,
			upstreams: map[string][]goldenStep{
				providers.ProviderAnthropic: {{status: http.StatusOK, headers: eventStreamHeaders, frames: native[:3], err: errGoldenUpstreamReset}},
				providers.ProviderFireworks: {compatOK(true, "never")}},
			requests:  []goldenRequest{{body: goldenAnthropicBody(true)}, {body: goldenAnthropicBody(true)}},
			configure: withArmDemotion},
		{name: prefix + "tool_turn_rescue_strikes_arm_for_turn", ingress: ingressAnthropic, decisions: rescue,
			upstreams: map[string][]goldenStep{
				providers.ProviderAnthropic: {goldenUpstreamError(http.StatusServiceUnavailable, goldenServerErrorBody)},
				providers.ProviderFireworks: {compatOK(true, "rescued")}},
			requests: []goldenRequest{
				{body: goldenToolResultBody, decorateContext: goldenClaudeCodeCtx},
				{body: goldenToolResultBody, decorateContext: goldenClaudeCodeCtx}},
			configure: withGoldenClock},
	}
}

const goldenGatewayModel = "gpt-5.4-mini"

const goldenGatewayToolTurn = `{"model":"` + goldenGatewayModel + `","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"list files"}],` +
	`"tools":[{"name":"read_file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]}`

const goldenCodexModel = "gpt-5.6-sol"

func withCodexQuotaMemory(svc *proxy.Service, h *goldenHarness) *proxy.Service {
	return withGoldenClock(svc, h).WithUsageObserver(usage.NewObserver([]byte("golden-salt"), 10*time.Minute, h.clock.Now))
}

func goldenResponsesOK(text string) goldenStep {
	return goldenOK("text/event-stream", []string{
		goldenSSE("", `{"type":"response.created","response":{"id":"resp_golden","status":"in_progress"}}`),
		goldenSSE("", `{"type":"response.output_text.delta","delta":"`+text+`"}`),
		goldenSSE("", `{"type":"response.completed","response":{"id":"resp_golden","status":"completed","usage":{"input_tokens":12,"output_tokens":7}}}`),
	})
}

func goldenCodexCtx(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, proxy.OpenAISubscriptionContextKey{}, "eyJhbGciOiJIUzI1NiJ9.golden-codex-jwt.signature")
	return context.WithValue(ctx, proxy.OpenAIAccountIDContextKey{}, "acct_golden")
}

// goldenCapabilityScenarios cover per-endpoint and per-plan memory a failed
// attempt leaves for the next request.
func goldenCapabilityScenarios() []goldenScenario {
	codexSpent := `{"error":{"type":"usage_limit_reached","message":"plan spent","resets_at":` +
		strconv.FormatInt(goldenEpoch.Add(time.Hour).Unix(), 10) + `}}`
	return []goldenScenario{
		{name: ingressAnthropic + "_stream_gateway_without_responses_remembered", ingress: ingressAnthropic,
			decisions: []router.Decision{{Provider: providers.ProviderOpenAIGateway, Model: goldenGatewayModel, Reason: "golden_route"}},
			upstreams: map[string][]goldenStep{providers.ProviderOpenAIGateway: {
				goldenUpstreamError(http.StatusNotFound, `{"message":"unknown path"}`),
				compatOK(true, "chat"), compatOK(true, "chat again")}},
			requests:  []goldenRequest{{body: goldenGatewayToolTurn}, {body: goldenGatewayToolTurn}},
			configure: withGoldenClock},
		{name: ingressOpenAI + "_stream_codex_plan_spent_fails_over_to_deployment_key", ingress: ingressOpenAI,
			decisions: []router.Decision{{Provider: providers.ProviderOpenAI, Model: goldenCodexModel, Reason: "golden_route"}},
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {
				goldenUpstreamError(http.StatusTooManyRequests, codexSpent),
				goldenResponsesOK("on key"), goldenResponsesOK("still on key")}},
			requests: []goldenRequest{
				{body: ingressBody(ingressOpenAI, true), decorateContext: goldenCodexCtx},
				{body: ingressBody(ingressOpenAI, true), decorateContext: goldenCodexCtx, advance: time.Minute}},
			configure: withCodexQuotaMemory},
	}
}
