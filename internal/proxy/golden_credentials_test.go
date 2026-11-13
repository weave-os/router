package proxy_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/observability"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
)

const (
	goldenBYOKAnthropic   = "sk-ant-api03-golden-byok-anthropic-secret"
	goldenBYOKOpenAI      = "sk-proj-golden-byok-openai-secret"
	goldenBYOKFireworks   = "fw-golden-byok-fireworks-secret"
	goldenClientAnthropic = "sk-ant-api03-golden-client-supplied-secret"
	goldenClientOAuth     = "sk-ant-oat01-golden-client-oauth-secret"
	goldenClientOpenAI    = "sk-proj-golden-client-openai-secret"
	goldenCodexToken      = "golden-codex-access-token-secret"
	goldenCodexAccount    = "golden-chatgpt-account"
	goldenCodexModel      = "gpt-5.5"
	goldenOpenAIOnlyModel = "gpt-4.1"
	// goldenCodexAttemptModel is the Codex-subscription model the OAuth-only scenarios route to after goldenOpenAIOnlyModel is excluded.
	goldenCodexAttemptModel = "gpt-5.6-sol"
)

var goldenCredentialLabels = map[string]string{
	goldenSubscription:    "subscription_context",
	goldenBYOKAnthropic:   "byok_anthropic",
	goldenBYOKOpenAI:      "byok_openai",
	goldenBYOKFireworks:   "byok_fireworks",
	goldenClientAnthropic: "client_anthropic_api_key",
	goldenClientOAuth:     "client_anthropic_oauth",
	goldenClientOpenAI:    "client_openai_api_key",
	goldenCodexToken:      "codex_subscription",
}

// goldenCredential is the non-secret projection of the credential an upstream
// attempt carried; secrets are replaced by their scenario label.
type goldenCredential struct {
	Label            string `json:"label"`
	Source           string `json:"source,omitempty"`
	OAuth            bool   `json:"oauth,omitempty"`
	Account          string `json:"account,omitempty"`
	BaseURL          string `json:"base_url,omitempty"`
	CredentialFailed bool   `json:"credential_error,omitempty"`
}

func (r *goldenRecorder) credential(ctx context.Context) *goldenCredential {
	if r.credentialLabels == nil {
		return nil
	}
	creds := proxy.CredentialsFromContext(ctx)
	if creds == nil || len(creds.APIKey) == 0 {
		if creds != nil && creds.CredentialError != nil {
			return &goldenCredential{Label: "unresolved", Source: creds.Source, CredentialFailed: true}
		}
		return &goldenCredential{Label: "deployment"}
	}
	label, ok := r.credentialLabels[string(creds.APIKey)]
	if !ok {
		r.exposed.markUnlabeled()
		label = "unlabeled"
	}
	return &goldenCredential{
		Label: label, Source: creds.Source, OAuth: creds.OAuth, Account: string(creds.AccountID),
		BaseURL: creds.BaseURL, CredentialFailed: creds.CredentialError != nil,
	}
}

// goldenExposure collects every byte a credential scenario lets escape the
// service besides the trace itself: client bodies, headers, errors and logs.
type goldenExposure struct {
	mu        sync.Mutex
	logs      bytes.Buffer
	client    bytes.Buffer
	records   bytes.Buffer
	unlabeled bool
}

func (e *goldenExposure) markUnlabeled() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.unlabeled = true
}

type goldenLogSink struct{ e *goldenExposure }

func (s goldenLogSink) Write(p []byte) (int, error) {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	return s.e.logs.Write(p)
}

func (e *goldenExposure) withLogger(ctx context.Context) context.Context {
	handler := slog.NewJSONHandler(goldenLogSink{e}, &slog.HandlerOptions{Level: slog.LevelDebug})
	return observability.WithLogger(ctx, slog.New(handler))
}

func (e *goldenExposure) record(rec *httptest.ResponseRecorder, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.client.Write(rec.Body.Bytes())
	for name, values := range rec.Header() {
		e.client.WriteString(name + ": " + strings.Join(values, ",") + "\n")
	}
	if err != nil {
		e.client.WriteString(err.Error())
	}
}

func (e *goldenExposure) requireNoSecrets(t *testing.T, trace []byte) {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	surfaces := map[string][]byte{
		"trace": trace, "client": e.client.Bytes(), "logs": e.logs.Bytes(), "records": e.records.Bytes(),
	}
	for secret, label := range goldenCredentialLabels {
		for surface, raw := range surfaces {
			if bytes.Contains(raw, []byte(secret)) {
				t.Errorf("credential %s leaked into %s", label, surface)
			}
		}
	}
	if e.unlabeled {
		t.Errorf("an attempt carried a credential the scenario never supplied")
	}
}

// recordRaw keeps every string and byte slice of a telemetry or billing row
// before goldenValue drops volatile fields and elides raw bytes.
func (r *goldenRecorder) recordRaw(v any) {
	if r.credentialLabels != nil {
		r.exposed.recordRaw(v)
	}
}

func (e *goldenExposure) recordRaw(v any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	writeRawText(&e.records, reflect.ValueOf(v), 0)
}

func writeRawText(buf *bytes.Buffer, v reflect.Value, depth int) {
	if !v.IsValid() || depth > 16 {
		return
	}
	switch v.Kind() {
	case reflect.String:
		buf.WriteString(v.String())
		buf.WriteByte(0)
	case reflect.Slice, reflect.Array:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			buf.Write(v.Bytes())
			buf.WriteByte(0)
			return
		}
		for i := 0; i < v.Len(); i++ {
			writeRawText(buf, v.Index(i), depth+1)
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			writeRawText(buf, v.Elem(), depth+1)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			writeRawText(buf, v.Field(i), depth+1)
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			writeRawText(buf, iter.Key(), depth+1)
			writeRawText(buf, iter.Value(), depth+1)
		}
	}
}

func byokKey(provider, secret string, priority auth.FundingPriority) *auth.ExternalAPIKey {
	return &auth.ExternalAPIKey{ID: "golden-ext-" + provider, Provider: provider, Plaintext: []byte(secret), FundingPriority: priority}
}

func withBYOK(keys ...*auth.ExternalAPIKey) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		return context.WithValue(ctx, proxy.ExternalAPIKeysContextKey{}, keys)
	}
}

func goldenCodexCtx(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, proxy.OpenAISubscriptionContextKey{}, goldenCodexToken)
	return context.WithValue(ctx, proxy.OpenAIAccountIDContextKey{}, goldenCodexAccount)
}

func goldenUnkeyedCtx(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, proxy.APIKeyIDContextKey{}, "")
	return context.WithValue(ctx, proxy.InstallationIDContextKey{}, "")
}

func goldenCreditsDeniedCtx(ctx context.Context) context.Context {
	return billing.WithWeaveCreditsDenied(ctx)
}

func chainCtx(fns ...func(context.Context) context.Context) func(context.Context) context.Context {
	return func(ctx context.Context) context.Context {
		for _, fn := range fns {
			ctx = fn(ctx)
		}
		return ctx
	}
}

func withoutDeploymentKey(provider string) func(*proxy.Service, *goldenHarness) *proxy.Service {
	return func(svc *proxy.Service, _ *goldenHarness) *proxy.Service {
		keyed := map[string]struct{}{}
		for _, p := range []string{providers.ProviderAnthropic, providers.ProviderOpenAI, providers.ProviderFireworks} {
			if p != provider {
				keyed[p] = struct{}{}
			}
		}
		return svc.WithDeploymentKeyedProviders(keyed)
	}
}

var (
	goldenOpenAIDecision     = decision(providers.ProviderOpenAI, goldenCodexModel)
	goldenOpenAIOnlyDecision = decision(providers.ProviderOpenAI, goldenOpenAIOnlyModel)
	goldenModelDeniedBody    = `{"type":"error","error":{"type":"not_found_error","message":"model: claude-haiku-4-5"}}`
	goldenUnauthorizedBody   = `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
	errGoldenCredential      = errors.New("golden: stored credential could not be decrypted")
)

type credentialCase struct {
	name      string
	ingress   string
	stream    bool
	decisions []router.Decision
	upstreams map[string][]goldenStep
	ctx       func(context.Context) context.Context
	headers   map[string]string
	configure func(*proxy.Service, *goldenHarness) *proxy.Service
	turns     int
	// refused marks cases rejected before a request row is emitted.
	refused bool
	// supplies lists the secrets the case hands the service; an attempt carrying
	// any other credential is recorded as unlabeled and fails the case.
	supplies []string
}

func (c credentialCase) scenario() goldenScenario {
	ingress := c.ingress
	if ingress == "" {
		ingress = ingressAnthropic
	}
	turns := c.turns
	if turns == 0 {
		turns = 1
	}
	requests := make([]goldenRequest, turns)
	for i := range requests {
		requests[i] = goldenRequest{body: ingressBody(ingress, c.stream), decorateContext: c.ctx, headers: c.headers, noTelemetry: c.refused}
	}
	allowed := map[string]string{}
	for _, secret := range c.supplies {
		allowed[secret] = goldenCredentialLabels[secret]
	}
	return goldenScenario{
		name: ingress + "_" + streamName(c.stream) + "_credentials_" + c.name, ingress: ingress,
		decisions: c.decisions, upstreams: c.upstreams, requests: requests,
		configure: c.configure, credentials: allowed,
	}
}

func anthropicUp(steps ...goldenStep) map[string][]goldenStep {
	return map[string][]goldenStep{providers.ProviderAnthropic: steps}
}

func responsesOK() goldenStep {
	return goldenOK("text/event-stream", []string{
		"data: " + `{"type":"response.output_text.delta","output_index":0,"delta":"ok"}` + "\n\n",
		"data: " + `{"type":"response.completed","response":{"id":"golden","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n",
	})
}

func goldenCredentialScenarios() []goldenScenario {
	ok := anthropicOK(false, "ok")
	openaiOK := responsesOK()
	anthropicDecisions := []router.Decision{goldenAnthropicDecision}
	openaiDecisions := []router.Decision{goldenOpenAIDecision}
	cases := []credentialCase{
		// Source precedence.
		{name: "deployment_key", decisions: anthropicDecisions, upstreams: anthropicUp(ok)},
		{name: "byok_plan_first", supplies: []string{goldenBYOKAnthropic}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityPlanFirst))},
		{name: "byok_key_first_over_subscription", supplies: []string{goldenSubscription, goldenBYOKAnthropic}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: chainCtx(goldenSubscriptionCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityKeyFirst)))},
		{name: "subscription_over_plan_first_byok", supplies: []string{goldenSubscription, goldenBYOKAnthropic}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: chainCtx(goldenSubscriptionCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityPlanFirst)))},
		{name: "subscription_context", supplies: []string{goldenSubscription}, stream: true, decisions: anthropicDecisions, upstreams: anthropicUp(anthropicOK(true, "ok")),
			ctx: goldenSubscriptionCtx},
		{name: "byok_other_provider_unused", supplies: []string{goldenBYOKOpenAI}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: withBYOK(byokKey(providers.ProviderOpenAI, goldenBYOKOpenAI, auth.FundingPriorityKeyFirst))},
		{name: "byok_base_url", supplies: []string{goldenBYOKAnthropic}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: withBYOK(&auth.ExternalAPIKey{ID: "golden-ext-gateway", Provider: providers.ProviderAnthropic,
				Plaintext: []byte(goldenBYOKAnthropic), BaseURL: "https://gateway.example.test"})},
		{name: "byok_credential_error", decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: withBYOK(&auth.ExternalAPIKey{ID: "golden-ext-broken", Provider: providers.ProviderAnthropic, CredentialError: errGoldenCredential})},
		{name: "byok_compat_provider", supplies: []string{goldenBYOKFireworks}, decisions: []router.Decision{goldenCompatDecision},
			upstreams: map[string][]goldenStep{providers.ProviderFireworks: {compatOK(false, "ok")}},
			ctx:       withBYOK(byokKey(providers.ProviderFireworks, goldenBYOKFireworks, auth.FundingPriorityPlanFirst))},

		// Inbound client credentials.
		{name: "router_keyed_oauth_bearer", supplies: []string{goldenClientOAuth}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			headers: map[string]string{"Authorization": "Bearer " + goldenClientOAuth}},
		{name: "router_keyed_api_key_not_forwarded", supplies: []string{goldenClientAnthropic}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			headers: map[string]string{"X-Api-Key": goldenClientAnthropic}},
		{name: "unkeyed_api_key_forwarded", supplies: []string{goldenClientAnthropic}, refused: true, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: goldenUnkeyedCtx, headers: map[string]string{"X-Api-Key": goldenClientAnthropic}},

		// Subscription suppression and paid fallback.
		{name: "subscription_exhausted_paid_fallback", supplies: []string{goldenSubscription}, stream: true, decisions: anthropicDecisions,
			upstreams: anthropicUp(anthropicOK(true, "ok")), ctx: goldenSubscriptionCtx, configure: exhaustSubscription},
		{name: "subscription_exhausted_byok_fallback", supplies: []string{goldenSubscription, goldenBYOKAnthropic}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx:       chainCtx(goldenSubscriptionCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityPlanFirst))),
			configure: exhaustSubscription},
		{name: "subscription_not_sent_to_compat", supplies: []string{goldenSubscription}, decisions: []router.Decision{goldenCompatDecision},
			upstreams: map[string][]goldenStep{providers.ProviderFireworks: {compatOK(false, "ok")}}, ctx: goldenSubscriptionCtx},
		{name: "subscription_overload_rescue_uses_deployment", supplies: []string{goldenSubscription}, decisions: []router.Decision{goldenRescueDecision, goldenCompatDecision},
			upstreams: map[string][]goldenStep{
				providers.ProviderAnthropic: {goldenUpstreamError(529, goldenOverloadBody)},
				providers.ProviderFireworks: {compatOK(false, "rescued")}},
			ctx: goldenSubscriptionCtx},
		{name: "subscription_model_denied", supplies: []string{goldenSubscription}, decisions: anthropicDecisions, turns: 2,
			upstreams: anthropicUp(goldenUpstreamError(404, goldenModelDeniedBody), ok, ok), ctx: goldenSubscriptionCtx},
		{name: "subscription_unauthorized", supplies: []string{goldenSubscription}, decisions: anthropicDecisions, turns: 2,
			upstreams: anthropicUp(goldenUpstreamError(401, goldenUnauthorizedBody), ok, ok), ctx: goldenSubscriptionCtx},

		// Funding authorization.
		{name: "credits_denied_deployment_refused", refused: true, decisions: anthropicDecisions, upstreams: anthropicUp(ok), ctx: goldenCreditsDeniedCtx},
		{name: "credits_denied_byok_funded", supplies: []string{goldenBYOKAnthropic}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: chainCtx(goldenCreditsDeniedCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityPlanFirst)))},
		{name: "credits_denied_subscription_funded", supplies: []string{goldenSubscription}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: chainCtx(goldenCreditsDeniedCtx, goldenSubscriptionCtx)},
		{name: "subscription_only_served_by_subscription", supplies: []string{goldenSubscription}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: chainCtx(goldenSubscriptionOnlyCtx, goldenSubscriptionCtx)},
		{name: "subscription_only_exhausted_paid_refused", supplies: []string{goldenSubscription}, refused: true, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: chainCtx(goldenSubscriptionOnlyCtx, goldenSubscriptionCtx), configure: exhaustSubscription},
		{name: "subscription_only_key_first_byok", supplies: []string{goldenBYOKAnthropic}, refused: true, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: chainCtx(goldenSubscriptionOnlyCtx, withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityKeyFirst)))},
		{name: "unkeyed_oauth_bearer", supplies: []string{goldenClientOAuth}, refused: true, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx: goldenUnkeyedCtx, headers: map[string]string{"Authorization": "Bearer " + goldenClientOAuth}},
		{name: "byok_only_without_key_refused", refused: true, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			configure: func(svc *proxy.Service, _ *goldenHarness) *proxy.Service { return svc.WithByokOnly(true) }},
		{name: "byok_only_with_key", supplies: []string{goldenBYOKAnthropic}, decisions: anthropicDecisions, upstreams: anthropicUp(ok),
			ctx:       withBYOK(byokKey(providers.ProviderAnthropic, goldenBYOKAnthropic, auth.FundingPriorityPlanFirst)),
			configure: func(svc *proxy.Service, _ *goldenHarness) *proxy.Service { return svc.WithByokOnly(true) }},

		// OpenAI and Codex lanes.
		{name: "deployment_key", ingress: ingressOpenAI, decisions: openaiDecisions,
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {openaiOK}}},
		{name: "byok_key_first", supplies: []string{goldenBYOKOpenAI}, ingress: ingressOpenAI, decisions: openaiDecisions,
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {openaiOK}},
			ctx:       withBYOK(byokKey(providers.ProviderOpenAI, goldenBYOKOpenAI, auth.FundingPriorityKeyFirst))},
		{name: "router_keyed_bearer_not_forwarded", supplies: []string{goldenClientOpenAI}, ingress: ingressOpenAI, decisions: openaiDecisions,
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {openaiOK}},
			headers:   map[string]string{"Authorization": "Bearer " + goldenClientOpenAI}},
		{name: "codex_subscription_not_used_on_chat", supplies: []string{goldenCodexToken}, ingress: ingressOpenAI, decisions: openaiDecisions,
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {openaiOK}}, ctx: goldenCodexCtx},
		{name: "codex_oauth_only_excludes_api_models", supplies: []string{goldenCodexToken},
			decisions: []router.Decision{goldenOpenAIOnlyDecision, decision(providers.ProviderOpenAI, goldenCodexAttemptModel)},
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {openaiOK}}, ctx: goldenCodexCtx,
			configure: func(svc *proxy.Service, h *goldenHarness) *proxy.Service {
				return withoutDeploymentKey(providers.ProviderOpenAI)(svc, h).WithByokOnly(true)
			}},
		{name: "codex_oauth_only_on_chat_serves_codex_model", supplies: []string{goldenCodexToken}, ingress: ingressOpenAI,
			decisions: []router.Decision{goldenOpenAIOnlyDecision, decision(providers.ProviderOpenAI, goldenCodexAttemptModel)},
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {openaiOK}},
			ctx:       goldenCodexCtx, configure: withoutDeploymentKey(providers.ProviderOpenAI)},
		{name: "codex_oauth_with_byok_keeps_api_models", supplies: []string{goldenCodexToken, goldenBYOKOpenAI}, ingress: ingressOpenAI,
			decisions: []router.Decision{goldenOpenAIOnlyDecision, goldenOpenAIDecision},
			upstreams: map[string][]goldenStep{providers.ProviderOpenAI: {openaiOK}},
			ctx:       chainCtx(goldenCodexCtx, withBYOK(byokKey(providers.ProviderOpenAI, goldenBYOKOpenAI, auth.FundingPriorityPlanFirst))),
			configure: withoutDeploymentKey(providers.ProviderOpenAI)},
		{name: "anthropic_subscription_cross_format", supplies: []string{goldenSubscription}, ingress: ingressOpenAI, decisions: anthropicDecisions,
			upstreams: anthropicUp(ok), ctx: goldenSubscriptionCtx},
	}
	out := make([]goldenScenario, 0, len(cases))
	for _, c := range cases {
		out = append(out, c.scenario())
	}
	return out
}

func goldenSubscriptionOnlyCtx(ctx context.Context) context.Context {
	return billing.WithSubscriptionOnly(ctx, billing.SubscriptionOnlyCreditsDepleted)
}
