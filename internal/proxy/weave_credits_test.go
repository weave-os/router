package proxy_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"weave-os/router/internal/auth"
	"weave-os/router/internal/billing"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/proxy"
	"weave-os/router/internal/router"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyMessages_WeaveCreditsDenied(t *testing.T) {
	for _, tc := range []struct {
		name       string
		denied     bool
		keys       []*auth.ExternalAPIKey
		wantErr    error
		wantSource string
	}{
		{name: "denied without a customer key refuses before dispatch", denied: true, wantErr: proxy.ErrWeaveCreditsDisabled},
		{name: "denied with the customer's Anthropic key serves on it", denied: true,
			keys:       []*auth.ExternalAPIKey{{Provider: providers.ProviderAnthropic, Plaintext: []byte("synthetic-byok")}},
			wantSource: "byok"},
		{name: "allowed keeps the Weave-funded deployment key", denied: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &capturingBillingRepo{}
			fr := &fakeRouter{decision: router.Decision{Provider: providers.ProviderAnthropic, Model: "claude-sonnet-4-6"}}
			p := &fakeProvider{proxyResponse: bypassStreamResponse}
			svc := proxy.NewService(fr, map[string]providers.Client{providers.ProviderAnthropic: p}, nil, nil, false, providers.ProviderAnthropic, "claude-haiku-4-5", nil).
				WithBillingService(billing.NewService(repo))

			ctx := context.WithValue(context.Background(), proxy.ExternalIDContextKey{}, "org-credits")
			ctx = billing.WithOrganizationPrepaid(ctx)
			if tc.denied {
				ctx = billing.WithWeaveCreditsDenied(ctx)
			}
			if tc.keys != nil {
				ctx = context.WithValue(ctx, proxy.ExternalAPIKeysContextKey{}, tc.keys)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(""))
			body := []byte(`{"model":"claude-sonnet-4-6","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

			err := svc.ProxyMessages(ctx, body, httptest.NewRecorder(), req)

			if tc.wantErr != nil {
				require.True(t, errors.Is(err, tc.wantErr), "got %v", err)
				assert.Empty(t, p.proxyBodies, "a turn only Weave credits could fund must not reach the provider")
				assert.Empty(t, repo.recordedDebits(), "a refused turn must not debit Weave credits")
				class, ok := proxy.ClassifyDispatchError(err)
				require.True(t, ok)
				assert.Equal(t, http.StatusPaymentRequired, class.Status)
				return
			}
			require.NoError(t, err)
			require.Len(t, p.proxyBodies, 1)
			if tc.wantSource == "" {
				assert.True(t, p.proxyCreds[0] == nil || p.proxyCreds[0].Source == "", "allowed credits serve on the deployment key")
				return
			}
			require.NotNil(t, p.proxyCreds[0])
			assert.Equal(t, tc.wantSource, string(p.proxyCreds[0].Source))
		})
	}
}
