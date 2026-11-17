package proxy_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"
	"weave-os/router/internal/router/sessionpin"
	"weave-os/router/internal/translate"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	reviewerDefaultModel = "claude-sonnet-5"
	reviewerVerdict      = "verdict: no block"
)

func autoModeReviewerBody(model string) string {
	return `{"model":"` + model + `","max_tokens":8192,` +
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

func reviewerRefusal(model string) reviewerReply {
	return reviewerReply{status: http.StatusOK, body: `{"id":"msg_1","type":"message","role":"assistant","model":"` + model + `","content":[],"stop_reason":"refusal","usage":{"input_tokens":10,"output_tokens":0}}`}
}

func reviewerModelNotFound(model string) reviewerReply {
	return reviewerReply{status: http.StatusNotFound, body: `{"type":"error","error":{"type":"not_found_error","message":"model: ` + model + `"}}`}
}

func reviewerOverloaded() reviewerReply {
	return reviewerReply{status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`}
}

// reviewerProvider answers each dispatch from a per-model script; a model whose
// script is exhausted keeps answering with its last reply.
type reviewerProvider struct {
	mu      sync.Mutex
	scripts map[string][]reviewerReply
	models  []string
	oauth   []bool
}

func (*reviewerProvider) SupportsSubscriptions() bool { return true }

func (p *reviewerProvider) Proxy(ctx context.Context, _ router.Decision, prep providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	p.mu.Lock()
	model := strings.Trim(strings.SplitN(strings.SplitN(string(prep.Body), `"model":`, 2)[1], ",", 2)[0], `" `)
	p.models = append(p.models, model)
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
	return proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: p}, nil, store, false, providers.ProviderAnthropic, reviewerDefaultModel, nil)
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

	require.NoError(t, svc.ProxyMessages(classifierSubscriptionCtx(), []byte(autoModeReviewerBody(reviewerDefaultModel)), rec, req))

	assert.Equal(t, reviewerDefaultModel, rec.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, []string{reviewerDefaultModel}, p.models)
}

// Without a subscription the reviewer is served on the model it asked for,
// not the deployment's classifier model and not a scored substitute.
func TestAutoModeReviewer_NoSubscriptionKeepsRequestedModel(t *testing.T) {
	p := &reviewerProvider{}
	svc := reviewerService(p, nil).WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	rec, req := classifierRequest(t.Context())

	require.NoError(t, svc.ProxyMessages(authedCtx("inst-reviewer"), []byte(autoModeReviewerBody(reviewerDefaultModel)), rec, req))

	assert.Equal(t, reviewerDefaultModel, rec.Header().Get(proxy.HeaderRouterModel))
	assert.Equal(t, []string{reviewerDefaultModel}, p.models)
}

// A reviewer call that cannot produce a verdict on the requested model is never
// handed back to Claude Code as an error or a refusal: Claude Code would deny
// the tool call (and, on an error, switch its reviewer to a model the API's
// safeguards refuse). The router retries transient failures on the same model
// and otherwise falls back to another reviewer model on the same credential.
func TestAutoModeReviewer_SubscriptionNeverReturnsFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		requested  string
		scripts    map[string][]reviewerReply
		wantModels []string
	}{
		"model_not_found_falls_back": {
			requested:  reviewerDefaultModel,
			scripts:    map[string][]reviewerReply{reviewerDefaultModel: {reviewerModelNotFound(reviewerDefaultModel)}},
			wantModels: []string{reviewerDefaultModel, "claude-sonnet-4-6"},
		},
		"refusal_retries_on_claude_default": {
			requested:  "claude-sonnet-5-5",
			scripts:    map[string][]reviewerReply{"claude-sonnet-5-5": {reviewerRefusal("claude-sonnet-5-5")}},
			wantModels: []string{"claude-sonnet-5-5", reviewerDefaultModel},
		},
		"overloaded_retries_same_model": {
			requested:  reviewerDefaultModel,
			scripts:    map[string][]reviewerReply{reviewerDefaultModel: {reviewerOverloaded(), reviewerOK(reviewerDefaultModel)}},
			wantModels: []string{reviewerDefaultModel, reviewerDefaultModel},
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := &reviewerProvider{scripts: tc.scripts}
			svc := reviewerService(p, nil)
			rec, req := classifierRequest(t.Context())
			ctx := billing.WithSubscriptionOnly(classifierSubscriptionCtx(), billing.SubscriptionOnlyCreditsDepleted)

			require.NoError(t, svc.ProxyMessages(ctx, []byte(autoModeReviewerBody(tc.requested)), rec, req))

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), reviewerVerdict)
			assert.Equal(t, tc.wantModels, p.models)
			for i, oauth := range p.oauth {
				assert.True(t, oauth, "attempt %d must stay on the caller's subscription", i)
			}
		})
	}
}
