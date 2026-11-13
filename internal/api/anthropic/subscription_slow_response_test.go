package anthropic_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/requestcontext"
	"weave-os/router/internal/router"
)

const slowSubscriptionStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"slow subscription answer\"}}\n\n" +
	"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

// slowSubscriptionProvider answers after a delay unless its request context
// ends first, recording which credential source each call used.
type slowSubscriptionProvider struct {
	delay   time.Duration
	sources []string
}

func (p *slowSubscriptionProvider) SupportsSubscriptions() bool { return true }

func (p *slowSubscriptionProvider) Proxy(ctx context.Context, _ router.Decision, _ providers.PreparedRequest, w http.ResponseWriter, _ *http.Request) error {
	source := ""
	if creds := requestcontext.CredentialsFromContext(ctx); creds != nil {
		source = creds.Source
	}
	p.sources = append(p.sources, source)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(p.delay):
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, err := w.Write([]byte(slowSubscriptionStream))
	return err
}

func (p *slowSubscriptionProvider) Passthrough(context.Context, providers.PreparedRequest, http.ResponseWriter, *http.Request) error {
	return providers.ErrNotImplemented
}

func TestMessagesHandler_LinkedSubscriptionSlowFirstResponseSucceeds(t *testing.T) {
	provider := &slowSubscriptionProvider{delay: 12 * time.Second}
	// The service owns background cache janitors that never exit, so it is built
	// outside the bubble; every request-path timer is still created inside it.
	svc := newTestService(&fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-5", Reason: "test"}}, providers.ProviderAnthropic, provider).
		WithDeploymentKeyedProviders(map[string]struct{}{providers.ProviderAnthropic: {}})
	engine := messagesEngine(svc)
	synctest.Test(t, func(t *testing.T) {
		body := `{"model":"claude-sonnet-4-5","stream":true,"messages":[{"role":"user","content":"synthetic slow turn"}],"max_tokens":4096}`
		ctx := context.WithValue(proxy.WithManagedSubscriptionUsage(context.Background()), proxy.AnthropicSubscriptionContextKey{}, "sk-ant-oat01-synthetic-linked-token")
		rec := httptest.NewRecorder()

		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(body))).WithContext(ctx))

		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "slow subscription answer")
		assert.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop"))
		assert.NotContains(t, rec.Body.String(), "Upstream call failed")
		assert.Equal(t, []string{requestcontext.SourceSubscription}, provider.sources, "served once on the linked subscription, with no paid rescue")
	})
}
