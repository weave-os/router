package dispatch_test

import (
	"context"
	"errors"
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
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
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
			require.True(t, errors.Is(err, dispatch.ErrTargetMismatch), "got %v", err)
			assert.Empty(t, upstream.models, "provider must not be called")
		})
	}
}
