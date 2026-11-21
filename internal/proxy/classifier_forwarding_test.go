package proxy_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/hmm"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	reviewerDefaultModel = "claude-sonnet-5"
	reviewerVerdict      = "verdict: no block"
)

func autoModeReviewerBody() string {
	return `{"model":"` + reviewerDefaultModel + `","max_tokens":8192,"thinking":{"type":"disabled"},"metadata":{"user_id":"synthetic"},` +
		`"system":[{"type":"text","text":"x-anthropic-billing-header: synthetic"},{"type":"text","text":"You are a security monitor for autonomous AI coding agents. Decide whether the latest action should be blocked."}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"<transcript>\n{\"Bash\":\"git push --force\"}\n</transcript>"}]}]}`
}

type reviewerReply struct {
	status int
	body   string
}

func reviewerOK(model string) reviewerReply {
	return reviewerReply{status: http.StatusOK, body: `{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"text","text":"` + reviewerVerdict + `"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`}
}

func reviewerModelNotFound(model string) reviewerReply {
	return reviewerReply{status: http.StatusNotFound, body: `{"type":"error","error":{"type":"not_found_error","message":"model: ` + model + `"}}`}
}

// reviewerProvider answers each dispatch from a per-model script; a model whose
// script is exhausted keeps answering with its last reply.
type reviewerProvider struct {
	mu      sync.Mutex
	scripts map[string][]reviewerReply
	models  []string
	oauth   []bool
	bodies  [][]byte
	betas   []string
}

func (*reviewerProvider) SupportsSubscriptions() bool { return true }

func (p *reviewerProvider) Proxy(ctx context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	p.mu.Lock()
	model := strings.Trim(strings.SplitN(strings.SplitN(string(prep.Body), `"model":`, 2)[1], ",", 2)[0], `" `)
	p.models = append(p.models, model)
	p.bodies = append(p.bodies, append([]byte(nil), prep.Body...))
	p.betas = append(p.betas, prep.Headers.Get("anthropic-beta"))
	creds := proxy.CredentialsFromContext(ctx)
	p.oauth = append(p.oauth, creds != nil && creds.OAuth)
	script, ok := p.scripts[model]
	reply := reviewerOK(model)
	if ok && len(script) > 0 {
		reply = script[0]
		if len(script) > 1 {
			p.scripts[model] = script[1:]
		}
	}
	p.mu.Unlock()
	if reply.status != http.StatusOK {
		return &providers.UpstreamErrorResponse{Status: reply.status, Body: []byte(reply.body)}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, reply.body)
	return nil
}

func (*reviewerProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return nil
}

func reviewerService(p *reviewerProvider, store *fakePinStore) *proxy.Service {
	fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: classifierScorerPickMdl, Reason: "cluster:v0.2"}}
	if store == nil {
		store = newFakePinStore()
	}
	return proxy.WithStrategyPorts(proxy.NewService(fr, dispatch.NewClients(map[string]providers.Client{providers.ProviderAnthropic: p}), nil, store, false, providers.ProviderAnthropic, reviewerDefaultModel, nil), hmm.ServingPorts())
}

// Claude Code's auto-mode reviewer picks its own model (Sonnet 5 by default)
// and fails closed on anything but a verdict. A user's /force-model choice is
// for their conversation, not for the reviewer.
func TestAutoModeReviewer_ForceModelKeepsRequestedModel(t *testing.T) {
	store := newFakePinStore()
	store.hasPin = true
	store.pin = sessionpin.Pin{
		Provider: providers.ProviderAnthropic, Model: "claude-opus-5-5",
		Reason: translate.ReasonUserForceModel, PinnedUntil: time.Now().Add(24 * time.Hour),
	}
	p := &reviewerProvider{}
	svc := reviewerService(p, store)
	rec, req := classifierRequest(t.Context())

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(autoModeReviewerBody()), rec, req))

	assert.Equal(t, reviewerDefaultModel, rec.Header().Get(requestcontext.HeaderRouterModel))
	assert.Equal(t, []string{reviewerDefaultModel}, p.models)
}

// Without a subscription the reviewer is served on the model it asked for,
// not the deployment's classifier model and not a scored substitute.
func TestAutoModeReviewer_NoSubscriptionKeepsRequestedModel(t *testing.T) {
	p := &reviewerProvider{}
	svc := reviewerService(p, nil).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	rec, req := classifierRequest(t.Context())

	require.NoError(t, svc.ProxyMessages(authedCtx("inst-reviewer"), []byte(autoModeReviewerBody()), rec, req))

	assert.Equal(t, reviewerDefaultModel, rec.Header().Get(requestcontext.HeaderRouterModel))
	assert.Equal(t, []string{reviewerDefaultModel}, p.models)
}

const reviewerClientBeta = "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27"

func reviewerRequest(ctx context.Context) (*httptest.ResponseRecorder, *http.Request) {
	rec, req := classifierRequest(ctx)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", reviewerClientBeta)
	return rec, req
}

// Classifier calls (auto-mode security review, titles, prefix checks) are
// Claude Code's own machinery: the router forwards them exactly as sent (same
// model, same bytes, same betas) and returns the upstream answer unchanged,
// on the subscription lane and on API-billed keys alike.
func TestAutoModeReviewer_ForwardedUntouched(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx       func() context.Context
		deployKey bool
		wantOAuth bool
	}{
		"subscription": {ctx: classifierSubscriptionCtx, wantOAuth: true},
		"api_billed":   {ctx: func() context.Context { return authedCtx("inst-reviewer") }, deployKey: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := &reviewerProvider{}
			svc := reviewerService(p, nil)
			if tc.deployKey {
				svc = svc.WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
			}
			body := autoModeReviewerBody()
			rec, req := reviewerRequest(t.Context())

			require.NoError(t, svc.ProxyMessages(tc.ctx(), []byte(body), rec, req))

			require.Len(t, p.bodies, 1)
			assert.Equal(t, body, string(p.bodies[0]))
			assert.Equal(t, reviewerClientBeta, p.betas[0])
			assert.Equal(t, tc.wantOAuth, p.oauth[0])
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.JSONEq(t, reviewerOK(reviewerDefaultModel).body, rec.Body.String())
		})
	}
}

// Errors go back to Claude Code as-is, exactly as they would without the
// router: no retry, no other model.
func TestAutoModeReviewer_SubscriptionErrorReturnedUntouched(t *testing.T) {
	p := &reviewerProvider{scripts: map[string][]reviewerReply{reviewerDefaultModel: {reviewerModelNotFound(reviewerDefaultModel)}}}
	svc := reviewerService(p, nil)
	ctx := billing.WithSubscriptionOnly(classifierSubscriptionCtx(), billing.SubscriptionOnlyCreditsDepleted)

	rec, req := reviewerRequest(t.Context())
	require.NoError(t, svc.ProxyMessages(ctx, []byte(autoModeReviewerBody()), rec, req))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.JSONEq(t, reviewerModelNotFound(reviewerDefaultModel).body, rec.Body.String())
	assert.Equal(t, []string{reviewerDefaultModel}, p.models)
	assert.Equal(t, []bool{true}, p.oauth)
}

// Models with a safety classifier (Sonnet 5.5) can answer the reviewer with a
// `refusal` and no verdict, which Claude Code turns into a block. The router
// asks Anthropic to re-serve such a turn on its fallback model, the same
// opt-in every other first-party turn carries; the rest of the request stays
// byte-identical.
func TestAutoModeReviewer_KeepsServerSideRefusalFallback(t *testing.T) {
	const refusingModel = "claude-sonnet-5-5"
	body := strings.Replace(autoModeReviewerBody(), reviewerDefaultModel, refusingModel, 1)
	for name, tc := range map[string]struct {
		ctx       func() context.Context
		deployKey bool
	}{
		"subscription": {ctx: classifierSubscriptionCtx},
		"api_billed":   {ctx: func() context.Context { return authedCtx("inst-reviewer") }, deployKey: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := &reviewerProvider{}
			svc := reviewerService(p, nil)
			if tc.deployKey {
				svc = svc.WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
			}
			rec, req := reviewerRequest(t.Context())

			require.NoError(t, svc.ProxyMessages(tc.ctx(), []byte(body), rec, req))

			require.Len(t, p.bodies, 1)
			assert.Equal(t, []string{refusingModel}, p.models)
			assert.Equal(t, `"default"`, gjson.GetBytes(p.bodies[0], "fallbacks").Raw)
			withoutFallbacks, err := sjson.DeleteBytes(p.bodies[0], "fallbacks")
			require.NoError(t, err)
			want, err := sjson.SetRaw(body, "thinking", `{"type":"between_tools"}`)
			require.NoError(t, err)
			assert.Equal(t, want, string(withoutFallbacks))
			assert.Equal(t, reviewerClientBeta+",server-side-fallback-2026-07-01", p.betas[0])
		})
	}
}

func TestAutoModeReviewer_ServerSideRefusalFallbackDisabledForwardsUntouched(t *testing.T) {
	body := strings.Replace(autoModeReviewerBody(), reviewerDefaultModel, "claude-sonnet-5-5", 1)
	body = strings.Replace(body, `"thinking":{"type":"disabled"}`, `"thinking":{"type":"between_tools"}`, 1)
	p := &reviewerProvider{}
	svc := reviewerService(p, nil).WithAnthropicServerSideFallback(false)
	rec, req := reviewerRequest(t.Context())

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(body), rec, req))

	require.Len(t, p.bodies, 1)
	assert.Equal(t, body, string(p.bodies[0]))
	assert.Equal(t, reviewerClientBeta, p.betas[0])
}

// Older Claude Code clients send the reviewer `thinking: disabled` on every
// model. Models without that switch (Sonnet 5.5) reject it with a 400, which
// Claude Code reports as "auto mode cannot determine the safety". The router
// sends the model's own off switch instead and leaves the rest of the body as is.
func TestAutoModeReviewer_DisabledThinkingSentAsTheModelsOffSwitch(t *testing.T) {
	const model = "claude-sonnet-5-5"
	body := strings.Replace(autoModeReviewerBody(), reviewerDefaultModel, model, 1)
	want, err := sjson.SetRaw(body, "thinking", `{"type":"between_tools"}`)
	require.NoError(t, err)
	for name, tc := range map[string]struct {
		ctx       func() context.Context
		deployKey bool
	}{
		"subscription": {ctx: classifierSubscriptionCtx},
		"api_billed":   {ctx: func() context.Context { return authedCtx("inst-reviewer") }, deployKey: true},
	} {
		t.Run(name, func(t *testing.T) {
			p := &reviewerProvider{}
			svc := reviewerService(p, nil).WithAnthropicServerSideFallback(false)
			if tc.deployKey {
				svc = svc.WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
			}
			rec, req := reviewerRequest(t.Context())

			require.NoError(t, svc.ProxyMessages(tc.ctx(), []byte(body), rec, req))

			require.Len(t, p.bodies, 1)
			assert.Equal(t, []string{model}, p.models)
			assert.Equal(t, want, string(p.bodies[0]))
			assert.Equal(t, reviewerClientBeta, p.betas[0])
		})
	}
}
