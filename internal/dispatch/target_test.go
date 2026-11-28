package dispatch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"weave-os/router/internal/dispatch"
	"weave-os/router/internal/providers"
	"weave-os/router/internal/router"
)

func guardedProxy(t *testing.T, upstream *fakeUpstream, decision router.Decision, body string) error {
	t.Helper()
	guarded := dispatch.GuardTarget(upstream, primary)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
	return guarded.Proxy(context.Background(), decision, providers.PreparedRequest{Body: []byte(body)}, httptest.NewRecorder(), req)
}

func TestGuardTarget_ForwardsMatchingRequest(t *testing.T) {
	for name, body := range map[string]string{
		"catalog id":  `{"model":"kimi-k2.5"}`,
		"upstream id": `{"model":"accounts/fireworks/models/kimi-k2p5"}`,
		"no model":    `{"contents":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			upstream := &fakeUpstream{name: primary.Provider}
			err := guardedProxy(t, upstream, router.Decision{Model: primary.CatalogID, Provider: primary.Provider}, body)
			require.NoError(t, err)
			assert.Equal(t, []string{primary.CatalogID}, upstream.models)
		})
	}
}

func TestGuardTarget_RejectsBeforeProviderIO(t *testing.T) {
	cases := map[string]struct {
		decision router.Decision
		body     string
	}{
		"wire model differs":        {router.Decision{Model: primary.CatalogID, Provider: primary.Provider}, `{"model":"gpt-5.6-luna"}`},
		"decision model differs":    {router.Decision{Model: "gpt-5.6-luna", Provider: primary.Provider}, `{"model":"kimi-k2.5"}`},
		"decision provider differs": {router.Decision{Model: primary.CatalogID, Provider: backup.Provider}, `{"model":"kimi-k2.5"}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			upstream := &fakeUpstream{name: primary.Provider}
			err := guardedProxy(t, upstream, tc.decision, tc.body)
			require.ErrorIs(t, err, dispatch.ErrTargetMismatch, "got %v", err)
			assert.Empty(t, upstream.models, "provider must not be called")
		})
	}
}

func TestGuardTarget_RefusesRoutingModelOnTheWire(t *testing.T) {
	for _, model := range []string{string(router.ModelAutomatic), router.ModelAutomaticDisplayName, string(router.ModelAutoEfficient), string(router.ModelAutoBeta), "WEAVE-AUTO-BETA", string(router.ModelHMMBeta)} {
		t.Run(model, func(t *testing.T) {
			target := primary
			target.UpstreamID = model
			upstream := &fakeUpstream{name: target.Provider}
			guarded := dispatch.GuardTarget(upstream, target)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/messages", nil)
			err := guarded.Proxy(context.Background(), router.Decision{Model: target.CatalogID, Provider: target.Provider},
				providers.PreparedRequest{Body: []byte(`{"model":"` + model + `"}`)}, httptest.NewRecorder(), req)
			require.ErrorIs(t, err, dispatch.ErrRoutingModelOnWire, "got %v", err)
			assert.Empty(t, upstream.models, "provider must not be called")
		})
	}
}
